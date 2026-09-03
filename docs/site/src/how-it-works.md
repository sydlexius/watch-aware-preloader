# How it works

The plugin never serves, transcodes, or modifies media. It reads byte ranges so
the Linux kernel caches them, and the media server later reads those same bytes
out of the shared page cache instead of off a platter. **The page cache is the
product.**

## Watch state, not modification time

Preload decisions come from the media server's API rather than from filesystem
timestamps. That is the difference that matters: modification time can find
recently-added files, but it cannot know that someone is three episodes into a
series, or that they stopped 40 minutes into a film last night.

Each sweep asks the server for:

| Signal | What it means |
|---|---|
| **Resume points** | Titles someone stopped partway through |
| **Next up** | The next unwatched episode of a series in progress |
| **Recently added** | New arrivals in the libraries you selected |

Items in an **active playback session** are excluded. That disk is already
spinning, so warming it buys nothing.

## Duration-based sizing

A fixed byte count is the wrong unit. 50 MB of a 4K remux is a couple of
seconds of playback; the same 50 MB of an SD episode is minutes. Sizing by
bytes means one of the two is always wrong.

The plugin sizes each preload in **seconds of playback**, converted to bytes
using the bitrate the server already reports. The warmed window therefore covers
the spin-up gap regardless of resolution, and `target_seconds` means the same
thing for every title in your library.

Floors and ceilings still apply, because a bitrate can be unreported or absurd -
see [Configuration](configuration.md).

## Resume reads from the offset, and the tail

For an in-progress title the plugin warms at the **resume byte offset**, not the
file head. Continuing a film is then as instant as starting one.

That alone is not enough. To seek, the player must first read the container's
**cue index**, which lives at the *end* of the file. If that region is cold, the
disk spins up anyway and the resume stalls - measured at about 8.5 seconds, the
exact failure this plugin exists to prevent.

So resume targets get the tail warmed too:

- **MKV**: the container is parsed and the cue region is warmed. That is
  more reliable than a fixed guess, and not necessarily smaller - on a large
  remux whose cue index sits well before EOF the parsed region can be several
  times `tail_mb`, which is the point: a guess that is too small leaves the
  player reading off a cold platter anyway. (A cue index larger than the tail
  cap is currently warmed only in part - see
  [#143](https://github.com/sydlexius/watch-aware-preloader/issues/143).)
- **Everything else**, and any parse failure: a flat tail read, sized by
  `tail_mb`.

## Placement: pools do not spin down

Not every file needs a spin-up allowance. Content on an SSD-backed cache pool is
already instantly available, so warming it against a spin-up window would spend
cache budget for no benefit.

The plugin resolves each file to the array member holding it and asks whether
that member is a pool. A member counts as a pool only when all three hold: it is
not named `diskN`, it is the root of a mounted filesystem, and **every backing
device reports non-rotational**. Anything undeterminable resolves toward the
array.

That asymmetry is deliberate. A wrong "not a pool" spends a little extra cache
budget; a wrong "pool" silently sizes a spinning disk with no allowance and
brings the stall back.

## Residency is probed, never remembered

There is deliberately **no ledger** of what was warmed. Each sweep probes actual
page-cache residency for the ranges it cares about.

That costs a little work per sweep and buys correctness: if something evicts the
cache - memory pressure, a `drop_caches`, a reboot - the next sweep sees reality
and re-warms. A remembered warm set would confidently skip files that are no
longer resident.

It is also why a healthy system reports warming almost nothing: everything worth
warming already is.

## Budgeted, and safe under pressure

You cap the warm set as a share of RAM. Page cache is **reclaimable**, so the
kernel evicts cached pages under memory pressure rather than denying memory to
an application: an over-large budget cannot cause an out-of-memory failure.

That is not the same as no impact. Warmed pages compete for the same cache as
everything else on the box, so a large or frequent warm set can evict pages
another application was relying on, and the reads themselves compete for I/O
while a sweep runs. The budget is the control for that - lower it if a sweep is
noticeable, rather than assuming the default suits every machine.

## Run model

The plugin runs as a **cron-invoked one-shot** (`preloadd -once`), the same
model Fix Common Problems and the Mover use. Each run is a complete fresh sweep,
so library and watch-state changes are picked up every interval without any
long-lived process or stored state.

An optional resident `--daemon` mode adds sub-minute reaction for those who want
it. Most installations do not need it.
