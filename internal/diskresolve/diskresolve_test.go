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
	return New(OS, full)
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

func TestIsPool(t *testing.T) {
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
		if got := isPool(tc.member); got != tc.pool {
			t.Errorf("isPool(%q) = %v, want %v", tc.member, got, tc.pool)
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
	return New(OS, full, WithUnionRoot(tr.union))
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
	cfs := &countingFS{FS: OS}
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
	cfs := &countingFS{FS: OS}
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
	cfs := &countingFS{FS: OS}
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
