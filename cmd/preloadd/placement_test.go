package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/diskresolve"
)

// mntTree builds a fake /mnt with the given subdirectories.
func mntTree(t *testing.T, dirs ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	return root
}

// capturingLog returns a logger writing to buf, so a test can assert the
// distinguishing content of a log line rather than just the resulting option
// count - the log wording is itself the deploy-time verification signal an
// operator reads.
func capturingLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

// mountedFS presents chosen directories as mount roots.
//
// diskresolve classifies a member as a pool only when it is BOTH named unlike
// an array disk AND is the root of a mounted filesystem (#120: a plain
// directory under /mnt is not a pool, however it is named). mntTree builds
// plain directories inside one t.TempDir(), so nothing in it is a real mount
// root and no member would classify as a pool. This supplies that single
// unrepresentable fact so the pool path stays reachable in a unit test;
// everything else about the tree stays real.
type mountedFS struct {
	diskresolve.FS
	mounts map[string]bool
}

func (m *mountedFS) IsMountpoint(name string) (bool, error) { return m.mounts[name], nil }

// poolFS makes the named members under root read as mount roots.
func poolFS(root string, members ...string) diskresolve.FS {
	mounts := make(map[string]bool, len(members))
	for _, m := range members {
		mounts[filepath.Join(root, m)] = true
	}
	return &mountedFS{FS: diskresolve.OS, mounts: mounts}
}

func TestPoolResidentOptsWithMembers(t *testing.T) {
	root := mntTree(t, "user", "disk1", "disk2", "cache")
	log, buf := capturingLog()

	opts := poolResidentOpts(poolFS(root, "cache"), root, log)
	if len(opts) != 1 {
		t.Fatalf("len(opts) = %d, want 1 - placement resolution should be enabled", len(opts))
	}
	out := buf.String()
	if !strings.Contains(out, "placement resolution enabled") {
		t.Errorf("log = %q, want it to report placement resolution enabled", out)
	}
	if !strings.Contains(out, "pool_members") {
		t.Errorf("log = %q, want it to name the pool members", out)
	}
	if !strings.Contains(out, "cache") {
		t.Errorf("log = %q, want the pool member name %q logged", out, "cache")
	}
}

func TestPoolResidentOptsWhenRootIsUnreadable(t *testing.T) {
	// The non-Unraid and containerised shape: there is no /mnt to list.
	log, buf := capturingLog()
	opts := poolResidentOpts(diskresolve.OS, filepath.Join(t.TempDir(), "absent"), log)
	if len(opts) != 0 {
		t.Fatalf("len(opts) = %d, want 0 - an unreadable root must not enable sizing down", len(opts))
	}
	out := buf.String()
	if !strings.Contains(out, "placement resolution unavailable") {
		t.Errorf("log = %q, want it to report placement resolution unavailable", out)
	}
	// The no-members branch logs the SAME message, so asserting only that text
	// would pass on the wrong branch. err= appears only on this one, and telling
	// the two apart is the whole point of the deploy-time log check.
	if !strings.Contains(out, "err=") {
		t.Errorf("log = %q, want an err= field distinguishing an unreadable root "+
			"from a readable root with no members", out)
	}
}

func TestPoolResidentOptsWhenNoMembers(t *testing.T) {
	// /mnt exists but holds only the union views, so there is nothing to
	// resolve against and every answer would be false anyway.
	root := mntTree(t, "user", "user0")
	log, buf := capturingLog()

	opts := poolResidentOpts(diskresolve.OS, root, log)
	if len(opts) != 0 {
		t.Fatalf("len(opts) = %d, want 0 - no members means no resolution", len(opts))
	}
	out := buf.String()
	if !strings.Contains(out, "placement resolution unavailable") {
		t.Errorf("log = %q, want it to report placement resolution unavailable", out)
	}
	if !strings.Contains(out, "no array members found") {
		t.Errorf("log = %q, want the no-members reason logged", out)
	}
}

func TestPoolResidentOptsWhenMembersButNoPools(t *testing.T) {
	// An all-array deployment: members exist but none classify as a pool, so
	// IsPool could never return true. This must be logged as distinctly inert,
	// not as plain "enabled", or a zero-pool deploy would silently read as
	// success at verification time (finding 3).
	root := mntTree(t, "user", "disk1", "disk2")
	log, buf := capturingLog()

	opts := poolResidentOpts(diskresolve.OS, root, log)
	if len(opts) != 0 {
		t.Fatalf("len(opts) = %d, want 0 - no pool members means the predicate can never fire", len(opts))
	}
	out := buf.String()
	if !strings.Contains(out, "inert") {
		t.Errorf("log = %q, want it to call out that resolution is enabled but inert", out)
	}
	if strings.Contains(out, "msg=\"placement resolution enabled\"") {
		t.Errorf("log = %q, must not read as plain success on a zero-pool array", out)
	}
}

// TestPoolResidentOptsPredicateAnswersTrue is the end-to-end assertion the
// count-and-log tests above cannot make: that the predicate this function wires
// up actually answers TRUE for a pool-resident file.
//
// It is the test that would have caught the union-root defect both PR reviewers
// found. Every other test here asserts only that SOME option came back, which a
// permanently-false predicate satisfies just as well - the resolver looked for
// the union share at the hardcoded /mnt/user, found nothing under it, and
// answered false for everything while still reporting "enabled".
func TestPoolResidentOptsPredicateAnswersTrue(t *testing.T) {
	root := mntTree(t, "user", "disk1", "cache")
	// The union view mirrors the member path onto the same inode, which is what
	// shfs does and what the resolver matches on.
	onPool := filepath.Join(root, "cache", "Media", "b.mkv")
	if err := os.MkdirAll(filepath.Dir(onPool), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(onPool, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	view := filepath.Join(root, "user", "Media", "b.mkv")
	if err := os.MkdirAll(filepath.Dir(view), 0o755); err != nil {
		t.Fatalf("mkdir view: %v", err)
	}
	if err := os.Link(onPool, view); err != nil {
		t.Fatalf("link: %v", err)
	}

	got := poolResidentFunc(poolFS(root, "cache"), root, quietLog())
	if got == nil {
		t.Fatal("no pool-resident predicate was produced")
	}
	if !got(view) {
		t.Error("predicate = false for a pool-resident file under the injected root; " +
			"the resolver's union root does not match the discovered root")
	}
	if got(filepath.Join(root, "user", "Media", "absent.mkv")) {
		t.Error("predicate = true for a file that does not exist, want false")
	}
}
