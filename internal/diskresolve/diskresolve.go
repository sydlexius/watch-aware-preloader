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
type FS interface {
	Stat(name string) (os.FileInfo, error)
	ReadDir(name string) ([]os.DirEntry, error)
}

type osFS struct{}

func (osFS) Stat(name string) (os.FileInfo, error)      { return os.Stat(name) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error) { return os.ReadDir(name) }

// OS is the real filesystem.
var OS FS = osFS{}

// Resolver resolves union paths against a set of array members.
type Resolver struct {
	fs      FS
	members []string
}

// New builds a Resolver over an explicit member list. Members are mount points
// such as "/mnt/disk1" or "/mnt/cache".
func New(fs FS, members []string) *Resolver {
	return &Resolver{fs: fs, members: append([]string(nil), members...)}
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
func isPool(member string) bool {
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
	return r.resolveUnder(unionPath, "/mnt/user")
}

func (r *Resolver) resolveUnder(unionPath, unionRoot string) (Location, error) {
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

	for _, m := range r.members {
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
		return Location{Member: m, Path: cand, Pool: isPool(m)}, nil
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
