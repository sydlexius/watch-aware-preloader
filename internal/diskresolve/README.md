# B1 spike: per-drive spin-up profiling vs worst-case sizing

Research note for [issue #5](https://github.com/sydlexius/watch-aware-preloader/issues/5)
(backlog B1). Deliverable was: (1) measure the spin-up spread on a real array,
(2) prototype the `/mnt/user` to `/mnt/diskN` resolver and report its
reliability, (3) recommend worst-case sizing (a) or per-disk profiling (b).

**Recommendation: neither, as posed. Adopt (a) with a corrected default, and use
the resolver for a different and larger win - skipping the spin-up buffer
entirely for pool-resident files.** Reasoning below.

## 1. Spin-up spread, measured

From the Phase 1 verification on the maintainer's server (four array disks, cold
read after a confirmed full standby):

| Drive | Class | Cold read from full standby |
|---|---|---|
| disk1 | 5400 RPM, 8 TB | 9881 ms |
| disk2 | 5400 RPM, 8 TB | 9829 ms |
| disk5 | 7200 RPM, 18 TB | 8615 ms |
| disk8 | 7200 RPM, 18 TB | 8460 ms |

Spread is **1421 ms, about 17%**, and it clusters tightly by RPM class rather
than varying per drive. Within a class the two drives differ by 52 ms and 155 ms
- noise next to the class difference.

This is the central finding for the (a)-vs-(b) question. Per-disk profiling
optimises against a distribution with two clusters and almost no within-cluster
variance. The whole benefit of (b) over (a) is the RAM saved by not
over-buffering the fast disks, and here that is at most 1.4 s of buffer on half
the array.

## 2. The resolver works, and is cheap

`internal/diskresolve` is a working prototype, tested against a real temporary
filesystem rather than a mock (the property under test is inode identity, which a
mock would only fake).

**Method.** shfs mirrors the share-relative path onto whichever member holds the
file, so `/mnt/user/Media/x.mkv` is `/mnt/<member>/Media/x.mkv` for exactly one
member. Probing members for that relative path finds it. No shfs internals, no
extended attributes, no Unraid version coupling - all of which would break
silently across releases.

**Reliability.** The one real hazard is a same-named file on more than one
member, which shfs itself has to arbitrate (split level, duplicates left by a
failed move). A path-only probe resolves those to whichever member is searched
first, which can be the wrong disk and therefore the wrong spin-up profile. The
resolver therefore matches on `os.SameFile` - same inode, same device - against
the union path, not on the path. `TestIdentityBeatsAMatchingPathOnAnotherDisk`
pins this; neutralising the identity check makes the resolver pick the decoy.

**Cost.** One `stat` on the union path plus at most one `stat` per member
probed. The original prototype probed every member including array disks,
which is safe only under an assumption - "already-hot dentry cache" - that was
never verified on-host and is undermined by this plugin's own
`preload.ram_percent = 50` default, which deliberately fills half of RAM with
page cache and raises reclaim pressure on exactly the dentry cache the claim
depended on. A negative lookup against a cold dentry can force XFS to read
metadata from the platter, spinning up the very disk this plugin exists to
keep asleep.

The wired consumer (`IsPool`, #113 wiring) fixes this by construction rather
than by relying on the cache assumption: it probes only the members already
known to be pools, never array members, so the cost is a handful of stats
against disks that, on a stock array, never spin down regardless of dentry-cache
state. "Known to be pools" means the member is named unlike an array disk AND is the
root of a mounted filesystem, so a stray `/mnt` directory that was never mounted
is now rejected before it is ever probed (#120). That covers only the
non-mountpoint case: a bind mount aliasing array content is a genuine mount
root, and an HDD-backed pool is one too, so both still classify as pools and can
still spin a disk up; see the assumption stated in full on `isPool`. Startup
logs the pool members by name so those remaining cases are visible rather than
silent.
`Resolve` still probes the full member list when a caller genuinely
needs to know which array disk holds a file; that path retains the original,
now-unverified cost claim and should not be used from a placement-only check.

**Limits, stated honestly.**

- Requires host-level access to `/mnt/diskN`. The native plugin has it; a
  containerised deployment (#60) generally would not, so any consumer must
  degrade gracefully rather than assume resolution succeeds.
- Resolution is a point-in-time answer. The mover can relocate a file between
  resolution and read. For sizing a preload that is harmless - a stale answer
  costs a slightly wrong buffer, not a wrong file - but it must not be cached as
  durable placement metadata.
- Returns `ErrUnresolved` rather than guessing when no member matches. That is
  the honest answer and callers must handle it.

## 3. Why the recommendation is not (b)

Three findings, in increasing order of importance.

**The spread does not justify per-file sizing.** 1.4 s across two RPM classes,
with negligible within-class variance. If per-disk sizing were adopted, the
useful form is per-RPM-class, not per-drive.

**Measuring spin-up per drive is disruptive.** A true measurement means forcing
the disk down (`mdcmd spindown N`) and timing a cold read. Doing that
automatically, on a schedule, on a live media server is exactly the latency this
project exists to remove. The passive alternative - inferring from observed cold
reads - only produces a sample when a read misses cache, which the preloader is
built to prevent, so the profile is starved precisely when it is working.

**The clamp binds before `target_seconds` does, on the highest-risk content.**
This is the finding that changes the answer:

| content | bitrate | head at t=20s | covered at the 250 MB clamp |
|---|---|---|---|
| SD / 480p | 3 Mbps | 8 MB | not clamped |
| HD 1080p | 10 Mbps | 24 MB | not clamped |
| HD high 1080p | 25 Mbps | 60 MB | not clamped |
| 4K remux typical | 60 Mbps | 143 MB | not clamped |
| 4K remux high | 100 Mbps | 238 MB | not clamped |
| 4K remux peak | 128 Mbps | 250 MB | **16 s** |

`HeadBytes` clamps to `MaxHeadMB` (250 MB default). At peak 4K bitrates that cap
lands at about **16 s of playback - below the 9.9 s spin-up plus seek and
transfer margin the same server measured**. Raising `target_seconds` cannot fix
it, because the clamp binds first. Tuning `target_seconds` per disk optimises a
parameter that is not the binding constraint on the content that most needs the
buffer.

## 4. Recommended actions

1. **Keep single global `target_seconds` (strategy (a)),** but set the default
   from the measured worst case rather than the current assumption. The docs
   assume 8-10 s; the slowest measured drive is 9.9 s before seek and transfer.
   A default of 20 s covers it for everything except peak-bitrate 4K, where the
   clamp binds anyway.
2. **Revisit `MaxHeadMB`.** It is the real limit on high-bitrate content. Either
   raise it, or make it bitrate-aware so the cap is expressed in seconds of
   coverage rather than bytes. Worth its own issue.
3. **Use the resolver for pool detection, not per-disk sizing.** A file on a
   cache or NVMe pool needs **no spin-up buffer at all** - the disk never spun
   down. That is a 100% saving on those files, against 17% from per-disk tuning
   of array files, and it needs no profiling, no forced spin-down, and no stored
   per-disk state. `Location.Pool` reports it.
4. **Do not build per-drive profiling** unless a future array shows materially
   wider spread. The measurement to justify it is cheap to repeat: the numbers in
   section 1 came from four `hdparm`-forced cold reads.

## Status

The resolver ships as a tested package but has **no consumer yet** - wiring it
into `HeadBytes` is action 3 above and belongs in its own change, so that this
spike lands as evidence rather than as an unreviewed behavior change.
