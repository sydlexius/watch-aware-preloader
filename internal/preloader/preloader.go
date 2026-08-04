// Package preloader warms the page cache for a ranked list of targets within a
// byte budget, sizing each read by playback duration.
package preloader

import (
	"context"
	"log/slog"
	"os"

	"github.com/doxazo-net/watch-aware-preloader/internal/container"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/pagecache"
	"github.com/doxazo-net/watch-aware-preloader/internal/pathmap"
)

// Safety caps for the parsed resume regions, so a bogus SeekHead pointer or
// large front attachments can never warm an unbounded amount.
const (
	maxFrontBytes = 16 << 20 // cap the front-metadata window
	maxTailBytes  = 64 << 20 // cap the cue tail window
)

// Config controls duration-based sizing and the tail read.
type Config struct {
	TargetSeconds int
	MinHeadBytes  int64
	MaxHeadBytes  int64
	TailBytes     int64
}

// FS abstracts file metadata for testability.
type FS interface {
	Stat(path string) (size int64, err error)
}

type osFS struct{}

func (osFS) Stat(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// DefaultFS returns an FS backed by the real filesystem.
func DefaultFS() FS { return osFS{} }

// WarmedRange is a byte range that was warmed into the page cache during a run.
type WarmedRange struct {
	Path   string
	Offset int64
	Length int64
}

// RunStats summarizes a preload pass.
type RunStats struct {
	Preloaded   int
	Skipped     int
	Missing     int
	BytesWarmed int64
	// ByTier and ByUser count only items actually preloaded this pass, so their
	// values each sum to Preloaded (not Preloaded+Skipped). Already-resident
	// items that were skipped are reflected only in the Skipped total.
	ByTier map[core.Tier]int
	ByUser map[string]int
	Warmed []WarmedRange
}

// Preloader executes preload passes.
type Preloader struct {
	cfg     Config
	cache   pagecache.Cache
	mapper  *pathmap.Mapper
	fs      FS
	log     *slog.Logger
	inspect func(path string, size int64) (container.Layout, bool)
	// poolResident reports whether a host path lives on a pool (cache/NVMe)
	// rather than a spinning array disk. Nil means "unknown", which sizes every
	// item as if it were on the array - the conservative answer, and the one a
	// non-Unraid host or a containerised deployment gets.
	poolResident func(hostPath string) bool
}

// Option configures optional Preloader behavior.
type Option func(*Preloader)

// WithPoolResident supplies a predicate reporting whether a host path lives on a
// pool. A file on a pool needs no spin-up allowance, because that disk never
// spun down (#113).
//
// The predicate is deliberately a plain function rather than a concrete
// resolver: sizing must not depend on Unraid specifics, and a caller that cannot
// resolve placement simply does not pass one.
func WithPoolResident(fn func(hostPath string) bool) Option {
	return func(p *Preloader) { p.poolResident = fn }
}

// New builds a Preloader.
func New(cfg Config, cache pagecache.Cache, mapper *pathmap.Mapper, fs FS, log *slog.Logger, opts ...Option) *Preloader {
	p := &Preloader{cfg: cfg, cache: cache, mapper: mapper, fs: fs, log: log, inspect: container.Inspect}
	for _, o := range opts {
		o(p)
	}

	return p
}

// ToHost maps a server-reported path to its host path via the configured path
// rules, reporting whether it mapped. Exposed so the sweep can reuse the same
// normalization for the library-scope filter.
func (p *Preloader) ToHost(serverPath string) (string, bool) {
	return p.mapper.ToHost(serverPath)
}

// HeadPlan is the outcome of sizing an item's head read: the bytes to warm, and
// whether the byte clamp overrode the requested duration.
//
// The distinction matters because MaxHeadBytes is denominated in BYTES while
// target_seconds is denominated in TIME, so the coverage the clamp permits
// shrinks as bitrate rises. Past roughly 84 Mbps at the default 20s/250MB the
// clamp binds first, and from there raising target_seconds changes nothing: the
// dial that appears to control coverage silently stops controlling it, on
// exactly the high-bitrate content where a stall is most noticeable. Reporting
// it is what makes that visible rather than inferable (#112).
type HeadPlan struct {
	// Bytes is the head size to warm.
	Bytes int64
	// CoveredSeconds is how much playback Bytes actually covers at this item's
	// bitrate. It equals the configured target in the ordinary case, but is
	// LARGER when MinHeadBytes raised a small request, SMALLER when Truncated,
	// and falls back to the configured target when the bitrate is unknown -
	// there is nothing to divide by, so it is the request rather than a
	// measurement. Read it as "what this head buys", not "what was asked for".
	CoveredSeconds float64
	// Truncated reports that MaxHeadBytes cut the request short.
	Truncated bool
}

// HeadBytes computes the duration-based head size for an item, clamped.
func HeadBytes(cfg Config, it core.MediaItem) int64 {
	return PlanHead(cfg, it).Bytes
}

// PlanHead sizes an item's head read and reports whether the byte clamp
// overrode the requested duration. See HeadPlan.CoveredSeconds for the cases
// where the coverage differs from the configured target in either direction.
func PlanHead(cfg Config, it core.MediaItem) HeadPlan {
	bps := it.BitrateBps
	if bps <= 0 && it.Runtime > 0 {
		bps = int64(float64(it.SizeBytes) / it.Runtime.Seconds() * 8)
	}
	want := int64(cfg.TargetSeconds) * bps / 8
	out := HeadPlan{Bytes: want, CoveredSeconds: float64(cfg.TargetSeconds)}
	if want < cfg.MinHeadBytes {
		// The floor raising a small request is not truncation - it grants MORE
		// coverage than asked for, so nothing is being silently lost.
		out.Bytes = cfg.MinHeadBytes
	}
	if want > cfg.MaxHeadBytes {
		out.Bytes = cfg.MaxHeadBytes
		out.Truncated = true
	}
	if bps > 0 {
		out.CoveredSeconds = float64(out.Bytes) * 8 / float64(bps)
	}

	return out
}

// Run warms targets in order until the budget is exhausted.
func (p *Preloader) Run(ctx context.Context, targets []core.PreloadTarget, budgetBytes int64) RunStats {
	stats := RunStats{ByTier: map[core.Tier]int{}, ByUser: map[string]int{}}
	var used int64
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		hostPath, ok := p.mapper.ToHost(t.Item.ServerPath)
		if !ok {
			stats.Missing++
			p.log.Warn("no path mapping", "server_path", t.Item.ServerPath)
			continue
		}
		size, err := p.fs.Stat(hostPath)
		if err != nil {
			stats.Missing++
			p.log.Warn("stat failed", "path", hostPath, "err", err)
			continue
		}

		pl := p.planWarm(t, hostPath, size)

		// Skip only when the front metadata, content window, and cue tail are
		// all resident; any cold region can force a disk spin-up on open/seek.
		if p.resident(hostPath, 0, pl.front) && p.resident(hostPath, pl.offset, pl.head) && p.resident(hostPath, pl.tailOffset, pl.tail) {
			stats.Skipped++
			continue
		}

		// Charge only the unique bytes actually warmed.
		cost := pl.front + pl.head + pl.tail
		if used+cost > budgetBytes {
			break // budget exhausted; remaining lower-priority targets dropped
		}

		// Warm the content window first: it is the fatal, budget-critical range.
		// Only if it succeeds do we warm the best-effort front metadata and cue
		// tail, so a head-warm failure never leaves front/tail bytes warmed but
		// uncharged (budget under-accounting) or unrecorded (a blind -verify).
		if err := p.cache.Warm(hostPath, pl.offset, pl.head); err != nil {
			stats.Missing++
			p.log.Warn("warm failed", "path", hostPath, "err", err)
			continue
		}
		if pl.front > 0 {
			if err := p.cache.Warm(hostPath, 0, pl.front); err != nil {
				p.log.Warn("front-metadata warm failed", "path", hostPath, "err", err)
			}
		}
		if pl.tail > 0 {
			if err := p.cache.Warm(hostPath, pl.tailOffset, pl.tail); err != nil {
				p.log.Warn("tail warm failed", "path", hostPath, "err", err)
			}
		}
		used += cost
		stats.Preloaded++
		stats.BytesWarmed += cost
		stats.ByTier[t.Tier]++
		stats.ByUser[t.Item.UserID]++
		// Record every warmed range in offset order so -verify can probe the
		// front metadata and cue tail, not just the content window.
		if pl.front > 0 {
			stats.Warmed = append(stats.Warmed, WarmedRange{Path: hostPath, Offset: 0, Length: pl.front})
		}
		stats.Warmed = append(stats.Warmed, WarmedRange{Path: hostPath, Offset: pl.offset, Length: pl.head})
		if pl.tail > 0 {
			stats.Warmed = append(stats.Warmed, WarmedRange{Path: hostPath, Offset: pl.tailOffset, Length: pl.tail})
		}
		p.log.Info("preloaded", "name", t.Item.Name, "tier", t.Tier.String(),
			"user", t.Item.UserID, "offset", pl.offset, "bytes", pl.head)
	}
	return stats
}

// warmRanges holds the byte ranges to warm for a single target: the front
// metadata window (container header, read on open), the content/head window
// (sized by playback duration, at the resume offset for in-progress items),
// and the EOF/cue tail, clamped so the three never overlap.
type warmRanges struct {
	front            int64 // [0, front) front metadata; 0 = none
	offset, head     int64
	tailOffset, tail int64
}

// planWarm computes the front-metadata, content (head), and tail ranges for an
// item against its file size. For a seeking (resume) target it warms the exact
// cue index and front metadata when the container parser can locate them,
// falling back to the flat TailBytes tail otherwise. hostPath is the mapped
// on-host path used to inspect the container.
func (p *Preloader) planWarm(t core.PreloadTarget, hostPath string, size int64) warmRanges {
	cfg := p.cfg
	// A file on a pool needs no SPIN-UP allowance: that disk never spun down, so
	// the only costs left on the playback path are seek and transfer, both orders
	// of magnitude smaller (#113). Dropping to the floor frees budget for
	// array-resident items, where the allowance actually earns its place.
	//
	// This is strictly an optimisation, so it applies ONLY on a positive answer.
	// No resolver, an unresolvable path, or anything else uncertain sizes for the
	// array - a wrong small head silently reintroduces the stall this project
	// exists to remove, which is far worse than a wrong large one.
	pooled := p.poolResident != nil && p.poolResident(hostPath)
	if pooled {
		cfg.TargetSeconds = 0 // the floor (MinHeadBytes) then governs
	}
	hp := PlanHead(cfg, t.Item)
	head := hp.Bytes
	if pooled {
		p.log.Debug("pool-resident: sized without a spin-up allowance",
			"path", hostPath, "head_bytes", head)
	}
	if hp.Truncated {
		// Surfaced per item rather than counted: the operator needs to know WHICH
		// content is under-covered, and by how much, to judge whether raising
		// max_head_mb is worth the budget. A bare count would not say that.
		p.log.Warn("head truncated by max_head_mb",
			"path", hostPath,
			"covered_seconds", int64(hp.CoveredSeconds),
			"target_seconds", p.cfg.TargetSeconds,
			"max_head_bytes", p.cfg.MaxHeadBytes)
	}
	offset := int64(0)
	seeking := t.Tier == core.TierResume
	if seeking {
		offset = resumeOffsetBytes(t.Item)
	}
	if offset >= size {
		offset = 0
	}
	if offset+head > size {
		head = size - offset
	}

	front, tailOffset, tail, parsed := p.inspectRanges(seeking, hostPath, size, offset)
	if !parsed && p.cfg.TailBytes > 0 {
		// Flat fallback tail: non-seeking tiers, or a parse failure.
		tailOffset, tail = flatTail(size, p.cfg.TailBytes)
	}
	// The tail must not overlap the content window (keeps the budget accurate).
	tailOffset, tail = clampTailToContent(tailOffset, tail, offset, head, size)
	return warmRanges{front: front, offset: offset, head: head, tailOffset: tailOffset, tail: tail}
}

// inspectRanges parses the container front (for a seeking/resume target) to
// locate the exact front-metadata and cue-tail ranges. parsed is false when
// the target isn't seeking, no inspector is configured, or the parse failed;
// callers then fall back to the flat tail.
func (p *Preloader) inspectRanges(seeking bool, hostPath string, size, offset int64) (front, tailOffset, tail int64, parsed bool) {
	if !seeking || p.inspect == nil {
		return 0, 0, 0, false
	}
	layout, ok := p.inspect(hostPath, size)
	if !ok {
		return 0, 0, 0, false
	}
	if layout.FrontEnd > 0 {
		front = layout.FrontEnd
		if front > maxFrontBytes {
			front = maxFrontBytes
		}
		// Never overlap the content window. When offset < FrontEnd (an early
		// resume), truncating front to offset relies on the head window covering
		// the rest of the metadata: it does because container.frontReadCap (the
		// cap on FrontEnd) is <= the smallest head (MinHeadBytes floor), so
		// offset+head >= head >= FrontEnd. Keep that invariant if either changes.
		if front > offset {
			front = offset
		}
	}
	// A trailing cue index needs its own tail warm; a front-placed cue index
	// is already covered by the front-metadata window.
	if layout.CueStart >= front && layout.CueStart < size {
		tailOffset = layout.CueStart
		if tailOffset < size-maxTailBytes {
			tailOffset = size - maxTailBytes
		}
		tail = size - tailOffset
	}
	return front, tailOffset, tail, true
}

// flatTail computes the fixed-size tail window from the end of the file.
func flatTail(size, tailBytes int64) (tailOffset, tail int64) {
	tailOffset = size - tailBytes
	if tailOffset < 0 {
		tailOffset = 0
	}
	tail = size - tailOffset
	return tailOffset, tail
}

// clampTailToContent pulls the tail forward so it never overlaps the content
// (head) window, keeping the budget accounting free of double-counted bytes.
func clampTailToContent(tailOffset, tail, offset, head, size int64) (int64, int64) {
	if tail > 0 && tailOffset < offset+head {
		tailOffset = offset + head
		tail = size - tailOffset
		if tail < 0 {
			tail = 0
		}
	}
	return tailOffset, tail
}

// resident reports whether [offset, length) is already fully page-cache resident.
// A zero-length range is trivially resident; unknown residency counts as not.
func (p *Preloader) resident(path string, offset, length int64) bool {
	if length == 0 {
		return true
	}
	r, known, err := p.cache.Resident(path, offset, length)
	return err == nil && known && r >= length
}

func resumeOffsetBytes(it core.MediaItem) int64 {
	if it.ResumeOffset <= 0 {
		return 0
	}
	// Mirror HeadBytes: derive bitrate from size/runtime when the API omits it,
	// so resume items still warm from the saved position instead of the head.
	bps := it.BitrateBps
	if bps <= 0 && it.Runtime > 0 {
		bps = int64(float64(it.SizeBytes) / it.Runtime.Seconds() * 8)
	}
	if bps <= 0 {
		return 0
	}
	return int64(it.ResumeOffset.Seconds()) * bps / 8
}
