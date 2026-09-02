package diskresolve

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The tests build a real directory tree in t.TempDir() rather than a mock FS,
// because the property under test is inode identity (os.SameFile). A mock would
// have to fake st_dev/st_ino and would then be asserting its own fake rather
// than the behavior that matters on a real array.
//
// Layout mirrors Unraid: <root>/user is the union view, <root>/diskN and
// <root>/cache are the members. Hard links stand in for what shfs does - the
// union path and the member path are genuinely the same inode.

type tree struct {
	root  string
	union string
}

func newTree(t *testing.T) tree {
	t.Helper()
	root := t.TempDir()
	tr := tree{root: root, union: filepath.Join(root, "user")}
	for _, d := range []string{"user", "disk1", "disk2", "disk3", "cache"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return tr
}

// place writes a file on a member and links it into the union view, which is
// how a real shfs share presents it.
func (tr tree) place(t *testing.T, member, rel, content string) {
	t.Helper()
	onDisk := filepath.Join(tr.root, member, rel)
	if err := os.MkdirAll(filepath.Dir(onDisk), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(onDisk, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	view := filepath.Join(tr.union, rel)
	if err := os.MkdirAll(filepath.Dir(view), 0o755); err != nil {
		t.Fatalf("mkdir view: %v", err)
	}
	if err := os.Link(onDisk, view); err != nil {
		t.Fatalf("link: %v", err)
	}
}

// writeOnly puts a file on a member WITHOUT linking it into the union - the
// decoy case, where two members hold the same relative path.
func (tr tree) writeOnly(t *testing.T, member, rel, content string) {
	t.Helper()
	p := filepath.Join(tr.root, member, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func (tr tree) resolver(members ...string) *Resolver {
	full := make([]string, len(members))
	for i, m := range members {
		full[i] = filepath.Join(tr.root, m)
	}
	// "cache" is the tree's stand-in for a pool. It is a plain directory, so it
	// must be presented as a mount root for the pool path to be reachable at
	// all; see mountedFS for why that one fact is faked and nothing else is.
	return New(tr.asPool(OS, "cache"), full)
}

func (tr tree) resolve(r *Resolver, rel string) (Location, error) {
	return r.resolveUnder(filepath.Join(tr.union, rel), tr.union)
}

func TestResolvesToTheHoldingDisk(t *testing.T) {
	tr := newTree(t)
	tr.place(t, "disk2", "Media/Movies/a.mkv", "x")
	r := tr.resolver("disk1", "disk2", "disk3", "cache")

	got, err := tr.resolve(r, "Media/Movies/a.mkv")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(tr.root, "disk2"); got.Member != want {
		t.Errorf("Member = %q, want %q", got.Member, want)
	}
	if got.Pool {
		t.Error("Pool = true for a numbered array disk")
	}
}

func TestIdentityBeatsAMatchingPathOnAnotherDisk(t *testing.T) {
	// The case that makes a naive prefix probe wrong: disk1 holds a DIFFERENT
	// file at the same relative path, and is searched first. Matching on the
	// path alone would bind the caller to disk1 and to disk1's spin-up profile.
	tr := newTree(t)
	tr.writeOnly(t, "disk1", "Media/Movies/a.mkv", "decoy")
	tr.place(t, "disk3", "Media/Movies/a.mkv", "real")
	r := tr.resolver("disk1", "disk2", "disk3")

	got, err := tr.resolve(r, "Media/Movies/a.mkv")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(tr.root, "disk3"); got.Member != want {
		t.Errorf("Member = %q, want %q - the decoy on disk1 was accepted", got.Member, want)
	}
}

func TestPoolIsFlagged(t *testing.T) {
	// The most valuable distinction for the caller: a pool never spins down, so
	// a file there needs no spin-up buffer at all.
	tr := newTree(t)
	tr.place(t, "cache", "Media/Movies/b.mkv", "x")
	r := tr.resolver("disk1", "cache")

	got, err := tr.resolve(r, "Media/Movies/b.mkv")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Pool {
		t.Errorf("Pool = false for %q, want true", got.Member)
	}
}

func TestUnresolvedWhenNoMemberHoldsIt(t *testing.T) {
	tr := newTree(t)
	tr.place(t, "disk1", "Media/a.mkv", "x")
	// Resolver is told about disk2 only, so the file is genuinely not findable.
	r := tr.resolver("disk2")

	_, err := tr.resolve(r, "Media/a.mkv")
	if !errors.Is(err, ErrUnresolved) {
		t.Errorf("err = %v, want ErrUnresolved", err)
	}
}

func TestNonUnionPathIsRejected(t *testing.T) {
	tr := newTree(t)
	r := tr.resolver("disk1")
	// Already addressed by member: there is nothing to resolve.
	if _, err := r.Resolve("/mnt/disk3/Media/a.mkv"); !errors.Is(err, ErrNotUnionPath) {
		t.Errorf("err = %v, want ErrNotUnionPath", err)
	}
	// A prefix that merely starts with the union root must not be accepted:
	// /mnt/user0 is a DIFFERENT view (array only, bypassing pools).
	if _, err := r.Resolve("/mnt/user0/Media/a.mkv"); !errors.Is(err, ErrNotUnionPath) {
		t.Errorf("/mnt/user0 err = %v, want ErrNotUnionPath", err)
	}
	// The root itself is not a file.
	if _, err := r.Resolve("/mnt/user"); !errors.Is(err, ErrNotUnionPath) {
		t.Errorf("root err = %v, want ErrNotUnionPath", err)
	}
}

func TestMissingFileReportsTheStatError(t *testing.T) {
	tr := newTree(t)
	r := tr.resolver("disk1")
	_, err := tr.resolve(r, "Media/absent.mkv")
	if err == nil {
		t.Fatal("want an error for a file that is not in the union view")
	}
	if errors.Is(err, ErrUnresolved) {
		t.Error("a missing union path should report the stat failure, not ErrUnresolved")
	}
}

// The NAME pass alone. These paths deliberately do not exist: the property here
// is purely how a member's name reads, which is the cheap first pass isPool
// makes before confirming against the filesystem. The mountpoint half is
// covered by TestPlainDirectoryIsNotAPool and TestUndeterminedMountpointIsNotAPool.
func TestNameLooksLikePool(t *testing.T) {
	for _, tc := range []struct {
		member string
		pool   bool
	}{
		{"/mnt/disk1", false},
		{"/mnt/disk12", false},
		{"/mnt/cache", true},
		{"/mnt/nvme", true},
		{"/mnt/diskfoo", true}, // a pool a user happened to name "diskfoo"
		{"/mnt/disk", true},    // not a numbered array disk
	} {
		if got := nameLooksLikePool(tc.member); got != tc.pool {
			t.Errorf("nameLooksLikePool(%q) = %v, want %v", tc.member, got, tc.pool)
		}
	}
}

func TestDiscoverSkipsTheUnionViewsAndNonMembers(t *testing.T) {
	tr := newTree(t)
	for _, d := range []string{"user0", "addons", "remotes", "disks"} {
		if err := os.MkdirAll(filepath.Join(tr.root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	if err := os.WriteFile(filepath.Join(tr.root, "notadir"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := Discover(OS, tr.root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{"cache", "disk1", "disk2", "disk3"}
	if len(got) != len(want) {
		t.Fatalf("Discover = %v, want %d members", got, len(want))
	}
	for i, w := range want {
		if filepath.Base(got[i]) != w {
			t.Errorf("member[%d] = %q, want %q", i, filepath.Base(got[i]), w)
		}
	}
}

func TestDiscoverOnAHostWithNoMntReportsTheError(t *testing.T) {
	// A non-Unraid host must not silently yield an empty member list that then
	// looks like "resolved nothing" - the caller needs to know why.
	if _, err := Discover(OS, filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("want an error listing a nonexistent mount root")
	}
}

// poolResolver builds a Resolver whose union root is the temp tree's, so the
// public IsPool predicate can be exercised at its real signature rather than
// through the unexported resolveUnder seam.
func (tr tree) poolResolver(members ...string) *Resolver {
	full := make([]string, len(members))
	for i, m := range members {
		full[i] = filepath.Join(tr.root, m)
	}
	return New(tr.asPool(OS, "cache"), full, WithUnionRoot(tr.union))
}

func TestIsPoolPredicateTrueOnAPool(t *testing.T) {
	tr := newTree(t)
	tr.place(t, "cache", "Media/b.mkv", "x")
	r := tr.poolResolver("disk1", "cache")

	if !r.IsPool(filepath.Join(tr.union, "Media/b.mkv")) {
		t.Error("IsPool = false for a pool-resident file, want true")
	}
}

func TestIsPoolPredicateFalseOnAnArrayDisk(t *testing.T) {
	// The direction that matters most: sizing down an array-resident file
	// reintroduces the spin-up stall this project exists to remove.
	tr := newTree(t)
	tr.place(t, "disk2", "Media/a.mkv", "x")
	r := tr.poolResolver("disk1", "disk2", "cache")

	if r.IsPool(filepath.Join(tr.union, "Media/a.mkv")) {
		t.Error("IsPool = true for an array-resident file, want false")
	}
}

func TestIsPoolPredicateFalseOnUncertainty(t *testing.T) {
	// Every error collapses to false, because false means "size for the array"
	// and that is the safe answer for all of them.
	tr := newTree(t)
	tr.place(t, "disk1", "Media/a.mkv", "x")
	r := tr.poolResolver("disk2") // told about a member that does not hold it

	for _, tc := range []struct {
		name string
		path string
	}{
		{"unresolved", filepath.Join(tr.union, "Media/a.mkv")},
		{"absent", filepath.Join(tr.union, "Media/gone.mkv")},
		{"not a union path", filepath.Join(tr.root, "disk1", "Media/a.mkv")},
		{"empty", ""},
	} {
		if r.IsPool(tc.path) {
			t.Errorf("%s: IsPool = true, want false", tc.name)
		}
	}
}

// mountedFS presents a chosen set of directories as mount roots.
//
// A unit test cannot mount a real filesystem, and the members in newTree are
// plain subdirectories of one t.TempDir(), so they all share a device number
// and IsMountpoint correctly reports false for every one of them. That is the
// right answer for the tree as built, but it leaves no way to exercise the
// pool path at all. This fake supplies the one fact the tree cannot: which
// members are mount roots. Everything else - inode identity, the union
// linkage, the stat sequence - stays real, so only the unrepresentable fact
// is faked.
type mountedFS struct {
	FS
	mounts map[string]bool
}

func (m *mountedFS) IsMountpoint(name string) (bool, error) {
	return m.mounts[name], nil
}

// asPool makes the named members read as mount roots, so a resolver built on
// the returned FS classifies them as pools.
func (tr tree) asPool(base FS, members ...string) FS {
	mounts := make(map[string]bool, len(members))
	for _, m := range members {
		mounts[filepath.Join(tr.root, m)] = true
	}
	return &mountedFS{FS: base, mounts: mounts}
}

// countingFS wraps a real FS and records every path passed to Stat, so a test
// can assert which members were (and were not) probed.
type countingFS struct {
	FS
	stats []string
}

func (c *countingFS) Stat(name string) (os.FileInfo, error) {
	c.stats = append(c.stats, name)
	return c.FS.Stat(name)
}

func (c *countingFS) statted(path string) bool {
	for _, s := range c.stats {
		if s == path {
			return true
		}
	}
	return false
}

func TestIsPoolDoesNotStatArrayMembers(t *testing.T) {
	// The spin-up hazard this guards against: a stat against an array member
	// that does NOT hold the file is a negative lookup, and on a dentry-cache
	// miss that can force the disk to spin up - exactly the cost this plugin
	// exists to remove. IsPool must never touch array members at all.
	tr := newTree(t)
	tr.place(t, "cache", "Media/b.mkv", "x")
	cfs := &countingFS{FS: tr.asPool(OS, "cache")}
	r := New(cfs, []string{
		filepath.Join(tr.root, "disk1"),
		filepath.Join(tr.root, "disk2"),
		filepath.Join(tr.root, "disk3"),
		filepath.Join(tr.root, "cache"),
	}, WithUnionRoot(tr.union))

	if !r.IsPool(filepath.Join(tr.union, "Media/b.mkv")) {
		t.Fatal("IsPool = false for a pool-resident file, want true")
	}

	for _, m := range []string{"disk1", "disk2", "disk3"} {
		want := filepath.Join(tr.root, m, "Media/b.mkv")
		if cfs.statted(want) {
			t.Errorf("IsPool statted array member path %q, want array members never probed", want)
		}
	}
	wantPool := filepath.Join(tr.root, "cache", "Media/b.mkv")
	if !cfs.statted(wantPool) {
		t.Errorf("IsPool never statted the pool member path %q", wantPool)
	}
}

func TestIsPoolDoesNotStatArrayMembersForAnArrayResidentFile(t *testing.T) {
	// Same guarantee on the false-answer path: a file that is actually on an
	// array disk must still resolve to false without the array ever being
	// probed by IsPool (Resolve, used elsewhere, is unaffected).
	tr := newTree(t)
	tr.place(t, "disk2", "Media/a.mkv", "x")
	cfs := &countingFS{FS: tr.asPool(OS, "cache")}
	r := New(cfs, []string{
		filepath.Join(tr.root, "disk1"),
		filepath.Join(tr.root, "disk2"),
		filepath.Join(tr.root, "disk3"),
		filepath.Join(tr.root, "cache"),
	}, WithUnionRoot(tr.union))

	if r.IsPool(filepath.Join(tr.union, "Media/a.mkv")) {
		t.Fatal("IsPool = true for an array-resident file, want false")
	}

	for _, m := range []string{"disk1", "disk2", "disk3"} {
		want := filepath.Join(tr.root, m, "Media/a.mkv")
		if cfs.statted(want) {
			t.Errorf("IsPool statted array member path %q, want array members never probed", want)
		}
	}
}

func TestResolveStillProbesAllMembers(t *testing.T) {
	// The narrowing lives entirely inside IsPool; Resolve must keep behaving
	// exactly as before, including probing array members when that is what a
	// caller genuinely needs (which member holds the file).
	tr := newTree(t)
	tr.place(t, "disk2", "Media/a.mkv", "x")
	cfs := &countingFS{FS: tr.asPool(OS, "cache")}
	r := New(cfs, []string{
		filepath.Join(tr.root, "disk1"),
		filepath.Join(tr.root, "disk2"),
		filepath.Join(tr.root, "disk3"),
	}, WithUnionRoot(tr.union))

	got, err := r.Resolve(filepath.Join(tr.union, "Media/a.mkv"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join(tr.root, "disk2"); got.Member != want {
		t.Errorf("Member = %q, want %q", got.Member, want)
	}
	// disk1 is searched before disk2, so Resolve's ordinary probe order must
	// still reach it.
	want := filepath.Join(tr.root, "disk1", "Media/a.mkv")
	if !cfs.statted(want) {
		t.Errorf("Resolve did not stat array member path %q; narrowing must not affect Resolve", want)
	}
}

func TestIsPoolPredicateFalseWithNoMembers(t *testing.T) {
	// The non-Unraid shape: discovery found nothing, so nothing resolves.
	tr := newTree(t)
	tr.place(t, "cache", "Media/b.mkv", "x")
	r := tr.poolResolver()

	if r.IsPool(filepath.Join(tr.union, "Media/b.mkv")) {
		t.Error("IsPool = true with no members, want false")
	}
}

// A member that is not a mountpoint cannot be a pool. This is the phantom-member
// shape observed on a real array (#120): /mnt/RecycleBin was a plain directory
// on rootfs, admitted by Discover and then classified as a pool purely because
// its name is not "diskN". Classifying it as a pool sizes anything reached
// through it without the spin-up allowance - the silent undersized head that
// #113's safety property exists to prevent.
//
// The tree here reproduces exactly that: a directory under the root that was
// never mounted. Every member in newTree is a plain directory, so this asserts
// the property against the same shape the real host presented.
func TestPlainDirectoryIsNotAPool(t *testing.T) {
	tr := newTree(t)
	phantom := filepath.Join(tr.root, "RecycleBin")
	if err := os.MkdirAll(phantom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if got := isPool(OS, phantom); got {
		t.Errorf("isPool(%s) = true, want false: a plain directory is not a mountpoint "+
			"and therefore cannot be a pool", phantom)
	}
}

// errMountFS reports every mountpoint probe as UNDETERMINED.
type errMountFS struct{ FS }

func (errMountFS) IsMountpoint(string) (bool, error) {
	return false, errors.New("no /sys, no device numbers, nothing")
}

// An undetermined mountpoint answer must resolve toward the ARRAY, never toward
// the pool. This is the asymmetry the whole placement path is built on: a wrong
// "not a pool" spends cache budget on a file that did not need it, while a wrong
// "pool" sizes a spinning disk with no spin-up allowance and silently
// reintroduces the stall the plugin exists to remove.
//
// The shapes that land here are real: a container without the member mounted, a
// filesystem that cannot report device numbers, a path that vanished between
// discovery and classification.
func TestUndeterminedMountpointIsNotAPool(t *testing.T) {
	tr := newTree(t)
	// Name it like a pool AND make it a genuine mount root, so the ONLY reason
	// to answer false is the probe failing. Without this the test would pass
	// for the wrong reason.
	member := filepath.Join(tr.root, "cache")

	if got := isPool(tr.asPool(OS, "cache"), member); !got {
		t.Fatalf("precondition: isPool(%s) = false with a working probe, so this "+
			"test cannot show the error path is what rejects it", member)
	}

	if got := isPool(errMountFS{FS: OS}, member); got {
		t.Errorf("isPool(%s) = true when the mountpoint probe errored, want false: "+
			"an undetermined answer must resolve toward the array", member)
	}
}

// countingMountFS records every IsMountpoint probe.
type countingMountFS struct {
	FS
	probes int
}

func (c *countingMountFS) IsMountpoint(name string) (bool, error) {
	c.probes++
	return c.FS.IsMountpoint(name)
}

// Pool classification must happen ONCE, at construction, and never from the
// per-file path. #120 is explicit that device resolution must not reach the
// per-target call: IsPool and Resolve run per preload target, so classifying on
// demand would add two Lstat calls per member to every one of them - on a
// 13-member array roughly 26 extra syscalls per target, against disks the
// plugin exists to leave asleep.
//
// Placement is fixed for the life of a sweep, so a cached answer is also the
// correct one.
func TestPoolClassificationHappensOncePerResolver(t *testing.T) {
	tr := newTree(t)
	tr.place(t, "cache", "Media/b.mkv", "x")
	cfs := &countingMountFS{FS: tr.asPool(OS, "cache")}
	r := New(cfs, []string{
		filepath.Join(tr.root, "disk1"),
		filepath.Join(tr.root, "cache"),
	}, WithUnionRoot(tr.union))

	afterNew := cfs.probes
	if afterNew == 0 {
		t.Fatal("New probed no members, so classification is not happening at construction")
	}

	for range 5 {
		if !r.IsPool(filepath.Join(tr.union, "Media/b.mkv")) {
			t.Fatal("IsPool = false for a pool-resident file, want true")
		}
	}

	if cfs.probes != afterNew {
		t.Errorf("IsPool made %d mountpoint probes across 5 calls, want 0: "+
			"classification must not reach the per-file path",
			cfs.probes-afterNew)
	}
}
