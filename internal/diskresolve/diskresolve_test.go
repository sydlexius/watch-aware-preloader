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
