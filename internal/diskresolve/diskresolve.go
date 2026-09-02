// Package diskresolve resolves a file on Unraid's /mnt/user union share to the
// physical disk or pool that actually holds it (B1, issue #5).
//
// Unraid's shfs presents /mnt/user as a union over /mnt/disk1../mnt/diskN plus
// any named pools, and deliberately hides which member holds a given file. The
// media server reports a /mnt/user path (or a container path that maps to one),
// so neither the API nor the share path names the backing disk.
//
// The resolution used here is deliberately the dumbest one that can work:
// shfs mirrors the share-relative path onto whichever member holds the file, so
// /mnt/user/Media/x.mkv is /mnt/<member>/Media/x.mkv for exactly one member.
// Probing the members for that relative path finds it, with no shfs internals,
// no xattrs, and no Unraid version coupling - all of which would be sources of
// silent breakage across releases.
//
// Correctness rests on identity, not on the path matching: a candidate counts
// only when it is the SAME inode on the SAME device as the union path. A
// same-named file on another disk (the split-level and duplicate cases shfs
// itself has to arbitrate) would otherwise resolve to the wrong disk and, in the
// caller, to the wrong spin-up profile.
package diskresolve

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotUnionPath is returned for a path that is not under the user share, so
// there is nothing to resolve - a file already addressed as /mnt/disk3/... or
// /mnt/cache/... names its member directly.
var ErrNotUnionPath = errors.New("not a /mnt/user path")

// ErrUnresolved is returned when no member holds the file. That is a real
// answer, not a failure: the union mount may be stale, the file may have been
// moved by the mover mid-sweep, or the path may simply not exist.
var ErrUnresolved = errors.New("no array member holds this file")

// Location is a resolved backing store for a union-share file.
type Location struct {
	// Member is the mount point holding the file, e.g. "/mnt/disk3" or
	// "/mnt/cache".
	Member string
	// Path is the file's real path under Member.
	Path string
	// Pool reports whether Member is a named pool (cache/NVMe) rather than a
	// numbered array disk. Pools do not spin down, so a caller sizing a preload
	// for spin-up latency should treat these as needing no spin-up buffer at
	// all - which is the single largest win available from resolving at all.
	Pool bool
}

// FS abstracts the filesystem calls so the resolver is testable without an
// Unraid array. Stat mirrors os.Stat; ReadDir mirrors os.ReadDir.
//
// IsMountpoint reports whether a path is the root of a mounted filesystem. It
// is part of this interface rather than a free function because the real
// implementation needs st_dev, which os.FileInfo does not expose portably.
//
// The bool/error split carries meaning: an error means UNDETERMINED, never
// "no". Callers must resolve an undetermined answer toward the array (see
// isPool), because a wrong "not a pool" only spends cache budget while a wrong
// "pool" silently reintroduces the spin-up stall.
type FS interface {
	Stat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]os.DirEntry, error)
	IsMountpoint(name string) (bool, error)
}

type osFS struct{}

func (osFS) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

// IsMountpoint compares a directory's device number against its parent's. A
// mount root sits on a different device from the directory it is mounted over,
// which is the same test mountpoint(1) makes and needs no privileges, no /proc,
// and no /sys - so it works in a container and on a non-Unraid host.
//
// It FOLLOWS symlinks (os.Stat, not os.Lstat), which mountpoint(1) also does. A
// symlink's own inode lives on the directory's filesystem, so lstat-ing one
// compares the link against its parent and reports false for a member that
// symlinks to a genuine mount root - classifying a real pool as array-backed.
// Discover does not admit symlinks today (ReadDir's IsDir is false for one), so
// nothing on the shipped path reaches this, but New accepts members from any
// caller and following is both correct and free.
//
// The root directory is always a mountpoint and is reported as such without
// consulting a parent, since filepath.Dir("/") is "/".
func (osFS) IsMountpoint(name string) (bool, error) {
	st, err := os.Stat(name)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(name)
	if parent == name {
		return true, nil
	}
	pst, err := os.Stat(parent)
	if err != nil {
		return false, err
	}
	dev, ok := deviceOf(st)
	if !ok {
		return false, fmt.Errorf("device number unavailable for %s", name)
	}
	pdev, ok := deviceOf(pst)
	if !ok {
		return false, fmt.Errorf("device number unavailable for %s", parent)
	}
	return dev != pdev, nil
}

// OS is the real filesystem.
var OS FS = osFS{}

// Resolver resolves union paths against a set of array members.
type Resolver struct {
	fs        FS
	members   []string
	unionRoot string
	// pools is the set of members that classify as pools, computed ONCE in New.
	//
	// Classification now touches the filesystem (isPool confirms the member is
	// a mount root), and both IsPool and Resolve are per-target calls, so
	// classifying on demand would put two Lstat calls per member into the
	// per-file path - on a 13-member array roughly 26 extra syscalls for every
	// preload target. Placement is fixed for the life of a sweep, so this is
	// decided at construction and read thereafter. #120 is explicit that device
	// resolution must not reach the per-file path.
	pools map[string]bool
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithUnionRoot overrides the union share root, which defaults to "/mnt/user".
// It exists so the resolver can be exercised against a temporary tree; a
// deployment has no reason to set it.
func WithUnionRoot(root string) Option {
	return func(r *Resolver) { r.unionRoot = root }
}

// New builds a Resolver over an explicit member list. Members are mount points
// such as "/mnt/disk1" or "/mnt/cache".
func New(fs FS, members []string, opts ...Option) *Resolver {
	r := &Resolver{fs: fs, members: append([]string(nil), members...), unionRoot: "/mnt/user"}
	for _, o := range opts {
		o(r)
	}
	// Classify once, after options are applied so a test's FS is in place.
	r.pools = make(map[string]bool, len(r.members))
	for _, m := range r.members {
		r.pools[m] = isPool(fs, m)
	}
	return r
}

// Discover enumerates array members by listing /mnt: numbered diskN mounts and
// any other directory that is not the union share itself or a known non-member.
// Callers on a non-Unraid host get an empty member list and every resolution
// then reports ErrUnresolved, which is the honest answer rather than a guess.
func Discover(fs FS, mntRoot string) ([]string, error) {
	entries, err := fs.ReadDir(mntRoot)
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", mntRoot, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// "user" and "user0" are the union views themselves; "addons" and
		// "remotes" are not array members. "disks" holds unassigned devices,
		// which are never part of a user share.
		switch name {
		case "user", "user0", "addons", "remotes", "disks", "rootshare":
			continue
		}
		out = append(out, filepath.Join(mntRoot, name))
	}
	sort.Strings(out)
	return out, nil
}

// isPool reports whether a member is a named pool rather than a numbered array
// disk. Array disks are diskN; anything else assigned to a share is a pool
// (cache, or a user-named NVMe pool).
//
// This is a name-based classification and rests on an assumption that is true
// for the common case but not universal: that a member which is not "diskN" is
// backed by solid-state storage that never spins down. Two shapes break it, and
// both size a spinning disk with no spin-up allowance - a silent undersized head
// on real content:
//
//   - An HDD-backed pool. Unraid permits a pool of spinning disks (e.g. a
//     btrfs/ZFS pool named "archive"), which spins down exactly like an array
//     disk. Nothing in the name says so.
//   - ANY other directory under /mnt that reaches array content. Discover admits
//     every directory it does not explicitly exclude, so a bind mount, an alias,
//     or a leftover mount point that shadows array files becomes a "pool"
//     member. The inode-identity check does NOT catch this: an alias of an array
//     file genuinely IS the same inode, so it matches.
//
// The SECOND shape is now PARTLY rejected: a member that is not the root of a
// mounted filesystem cannot be a pool, whatever it is named. That check needs no
// /sys walk and no device mapping, only a comparison of device numbers against
// the parent directory, so it is cheap enough to run at startup. It was observed
// on a real array, where /mnt/RecycleBin - a plain directory on rootfs - was
// classified as a pool purely because its name is not "diskN".
//
// It rejects only the NON-MOUNTPOINT case. A bind mount or any other genuine
// mount that reaches array content IS a mount root, so it still passes here and
// is still misclassified. Rejecting that needs the rotational status of the
// backing devices, the same thing the HDD-backed pool below needs.
//
// The FIRST shape (an HDD-backed pool) still stands: a pool of spinning disks
// is a genuine mountpoint, so this check passes it through and it remains
// misclassified. Rejecting it needs the rotational status of the backing
// devices, which is the larger change #120 tracks. There is deliberately no
// config escape hatch. The mitigation stays visibility: PoolMembers is logged
// at startup (see cmd/preloadd/placement.go), so an operator sees exactly which
// members were classified as pools and can spot that shape in one line.
//
// UNCERTAINTY RESOLVES TOWARD THE ARRAY. An unreadable path, a filesystem that
// cannot report device numbers, a container without the member mounted - all
// answer "not a pool", never "pool". A wrong "not a pool" only spends cache
// budget on a file that did not need it; a wrong "pool" silently reintroduces
// the spin-up stall this project exists to remove, and that asymmetry decides
// every uncertain case.
func isPool(fs FS, member string) bool {
	if !nameLooksLikePool(member) {
		return false
	}
	// A pool is a mounted filesystem. A plain directory that merely sits under
	// the mount root is not, however it is named.
	mounted, err := fs.IsMountpoint(member)
	if err != nil || !mounted {
		return false
	}
	return true
}

// nameLooksLikePool reports whether a member's NAME is not that of a numbered
// array disk. It is the cheap first pass: array disks are diskN, so anything
// else is a pool CANDIDATE, which isPool then confirms against the filesystem.
func nameLooksLikePool(member string) bool {
	base := filepath.Base(member)
	suffix, ok := strings.CutPrefix(base, "disk")
	if !ok || suffix == "" {
		return true // not "diskN" at all, or the bare word "disk"
	}
	// An array disk is "disk" followed by digits ONLY; "diskfoo" is a pool a
	// user happened to name that way.
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

// Resolve finds the member holding unionPath.
//
// unionPath must be under /mnt/user (or the configured union root). The returned
// Location names the member, the real path, and whether that member is a pool.
func (r *Resolver) Resolve(unionPath string) (Location, error) {
	return r.resolveUnder(unionPath, r.unionRoot)
}

// IsPool reports whether unionPath lives on a pool rather than a spinning array
// disk. Its signature matches preloader.WithPoolResident, so it binds directly
// as a method value.
//
// The argument must be a path under the UNION SHARE (/mnt/user/..., or whatever
// WithUnionRoot set), not a member path. A path that already names its member -
// /mnt/cache/Media/x.mkv - is ErrNotUnionPath and therefore FALSE, so a caller
// passing one silently loses the optimisation on every item rather than failing
// loudly. Callers map server paths to the union share before asking.
//
// Every error collapses to false: ErrUnresolved, ErrNotUnionPath, a stat
// failure, an empty member list. That is not laziness about error handling - it
// is the safety property. False means "size for the spin-up allowance", and the
// cost asymmetry is stark: a wrong SMALL head silently reintroduces the 8.5-9.9 s
// stall this project exists to remove, while a wrong large one only spends
// budget. Uncertainty therefore resolves toward the conservative side.
//
// The probe is deliberately narrowed to pool members only - Resolve's full
// member list (array disks included) is never consulted here. The predicate
// only needs to know WHETHER the file is on A pool, not WHICH member holds it,
// and restricting the search to pool members gives the identical answer:
// a file on a pool matches and is true; a file on an array disk matches no pool
// member and is false, which is also the correct (and already conservative)
// answer; a file that resolves to nothing is false either way. The reason this
// matters is not just correctness: stat-ing an array member that does NOT hold
// the file is a negative lookup, and on a dentry-cache miss XFS resolves that by
// reading metadata from the platter - spinning up exactly the idle disk this
// plugin exists to keep asleep. Narrowing the probe to pool members means no
// array MEMBER path is ever stat-ed. The union path itself is still stat-ed
// once, to establish the identity every candidate is matched against; shfs may
// resolve that through the holding member, so this bounds the array-touching
// work at one lookup rather than eliminating it.
//
// The answer is point-in-time and is deliberately not cached. The mover can
// relocate a file between this call and the read; a stale answer costs a
// slightly wrong buffer, but caching it would make that staleness durable.
func (r *Resolver) IsPool(unionPath string) bool {
	loc, err := r.resolveAmong(unionPath, r.unionRoot, r.poolMembers())
	if err != nil {
		return false
	}
	return loc.Pool
}

// poolMembers returns the subset of r.members that are named pools, in the
// same order they were configured.
func (r *Resolver) poolMembers() []string {
	var pools []string
	for _, m := range r.members {
		if r.pools[m] {
			pools = append(pools, m)
		}
	}
	return pools
}

// PoolMembers returns the subset of members that classify as named pools, in
// the same order they were configured. It exists for startup logging: naming
// the pool members lets an operator with an HDD-backed pool (see isPool's doc
// comment on that misclassification) spot the problem from a log line instead
// of an unexplained stall, and lets an operator on an all-array deployment see
// at a glance that pool-resident sizing has nothing to act on.
func (r *Resolver) PoolMembers() []string {
	return r.poolMembers()
}

func (r *Resolver) resolveUnder(unionPath, unionRoot string) (Location, error) {
	return r.resolveAmong(unionPath, unionRoot, r.members)
}

func (r *Resolver) resolveAmong(unionPath, unionRoot string, members []string) (Location, error) {
	clean := filepath.Clean(unionPath)
	rel, ok := relUnder(clean, unionRoot)
	if !ok {
		return Location{}, fmt.Errorf("%q: %w", unionPath, ErrNotUnionPath)
	}

	// The union path is the identity to match against. Without it the resolver
	// would accept any same-named file, which is exactly the duplicate case that
	// makes a naive prefix probe wrong.
	want, err := r.fs.Stat(clean)
	if err != nil {
		return Location{}, fmt.Errorf("stat %s: %w", clean, err)
	}

	for _, m := range members {
		cand := filepath.Join(m, rel)
		got, err := r.fs.Stat(cand)
		if err != nil {
			continue // not on this member, or unreadable - both mean "keep looking"
		}
		if !os.SameFile(want, got) {
			// Same relative path, different file. shfs itself arbitrates these;
			// treating one as the answer would bind the caller to the wrong disk.
			continue
		}
		return Location{Member: m, Path: cand, Pool: r.pools[m]}, nil
	}
	return Location{}, fmt.Errorf("%q: %w", unionPath, ErrUnresolved)
}

// relUnder returns p's path relative to root, and whether p is under root at
// all. It compares whole path elements so "/mnt/user0/x" is not read as being
// under "/mnt/user".
func relUnder(p, root string) (string, bool) {
	root = filepath.Clean(root)
	if p == root {
		return "", false // the root itself is not a file to resolve
	}
	if !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", false
	}
	return strings.TrimPrefix(p, root+string(filepath.Separator)), true
}
