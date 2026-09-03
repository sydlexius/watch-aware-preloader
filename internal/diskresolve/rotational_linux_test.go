//go:build linux

package diskresolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sysTree builds a fake /proc/self/mountinfo and /sys, so the REAL parsing and
// the REAL sysfs walk are exercised. Mocking the prober itself would assert
// only that a stub returns what it was told to; the defects this code can
// actually have - a mis-parsed mountinfo line, a partition resolved to a
// non-existent parent, a multi-device pool judged by one device - live in the
// parsing and the walk, so those are what the fake has to leave real.
type sysTree struct {
	root string
	p    sysProber
}

func newSysTree(t *testing.T) *sysTree {
	t.Helper()
	root := t.TempDir()
	tr := &sysTree{
		root: root,
		p: sysProber{
			mountinfo: filepath.Join(root, "mountinfo"),
			sysRoot:   filepath.Join(root, "sys"),
		},
	}
	tr.writeMountinfo()
	return tr
}

// disk creates a whole block device with the given rotational value.
func (tr *sysTree) disk(t *testing.T, name string, rotational int) {
	t.Helper()
	dir := filepath.Join(tr.root, "sys", "block", name, "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rotational"), []byte{byte('0' + rotational), '\n'}, 0o644); err != nil {
		t.Fatal(err)
	}
	tr.link(t, name, filepath.Join(tr.root, "sys", "block", name))
}

// partition creates a partition of an existing disk. Like the real sysfs it
// carries a "partition" file and NO queue of its own, so rotational status can
// only be found by walking to the parent.
func (tr *sysTree) partition(t *testing.T, disk, part string) {
	t.Helper()
	dir := filepath.Join(tr.root, "sys", "block", disk, part)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "partition"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr.link(t, part, dir)
}

// link adds the /sys/class/block entry, which is the symlink the prober
// resolves. Real sysfs exposes every device there, partitions included.
func (tr *sysTree) link(t *testing.T, name, target string) {
	t.Helper()
	cls := filepath.Join(tr.root, "sys", "class", "block")
	if err := os.MkdirAll(cls, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(cls, name)); err != nil {
		t.Fatal(err)
	}
}

// btrfs declares a btrfs filesystem spanning the named devices, the way
// /sys/fs/btrfs/<fsid>/devices does.
func (tr *sysTree) btrfs(t *testing.T, fsid string, devices ...string) {
	t.Helper()
	dir := filepath.Join(tr.root, "sys", "fs", "btrfs", fsid, "devices")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real /sys/fs/btrfs/<fsid>/devices entries are SYMLINKS to the block
	// device, not directories. The fixture mirrors that so a future "filter to
	// directories" change cannot look safe here while rejecting every genuine
	// entry on a real host.
	for _, d := range devices {
		target := filepath.Join(tr.root, "sys", "block", d)
		if err := os.Symlink(target, filepath.Join(dir, d)); err != nil {
			t.Fatal(err)
		}
	}
}

// mounts replaces the fake mountinfo with the given entries.
func (tr *sysTree) mounts(t *testing.T, lines ...string) {
	t.Helper()
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(tr.p.mountinfo, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (tr *sysTree) writeMountinfo() {
	_ = os.WriteFile(tr.p.mountinfo, nil, 0o644)
}

// mountLine renders a mountinfo line in the kernel's real format, including the
// variable optional-fields section terminated by "-".
func mountLine(mountpoint, fstype, source string) string {
	return "36 35 0:45 / " + mountpoint + " rw,relatime shared:1 - " + fstype + " " + source + " rw"
}

// An all-SSD pool is a pool. This is the positive case the whole change has to
// keep working: without it a fix that answered "rotational" for everything
// would pass every safety test here and quietly disable pool sizing entirely.
func TestAllNonRotationalOnSSDPool(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	// A single-device btrfs pool, declared in sysfs as a real one is.
	tr.btrfs(t, "fsid-cache", "nvme0n1p1")
	tr.mounts(t, mountLine("/mnt/cache", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/cache")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if !got {
		t.Error("AllNonRotational(/mnt/cache) = false for an all-SSD pool, want true")
	}
}

// An HDD-backed pool is the original defect in #120: a genuine mount root,
// named unlike an array disk, that nevertheless spins down. It must NOT be a
// pool.
func TestHDDBackedPoolIsRotational(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "sda", 1)
	tr.partition(t, "sda", "sda1")
	// xfs, not btrfs: this test is about ROTATIONAL STATUS, and a btrfs source
	// with no /sys/fs/btrfs entry answers undetermined for a different reason
	// (see TestBtrfsWithoutSysfsIsUndetermined), which would pass this test
	// without the rotational check ever running.
	tr.mounts(t, mountLine("/mnt/archive", "xfs", "/dev/sda1"))

	got, err := tr.p.AllNonRotational("/mnt/archive")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if got {
		t.Error("AllNonRotational(/mnt/archive) = true for an HDD-backed pool, want false: " +
			"a pool of spinning disks spins down like an array disk and needs the full spin-up allowance")
	}
}

// A pool mixing an SSD and an HDD spins down, so it is NOT a pool - and it can
// only be caught by enumerating every device. The mount names the SSD, so any
// implementation that trusts the mountinfo source alone answers "pool" here and
// silently undersizes the spinning half.
//
// This is the case measured on the live array: a pool reporting one source in
// mountinfo was actually built from two devices.
func TestMixedBtrfsPoolIsRotational(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "sdb", 1)
	tr.partition(t, "sdb", "sdb1")
	tr.btrfs(t, "fsid-mixed", "nvme0n1p1", "sdb1")
	// mountinfo names ONLY the SSD, exactly as the kernel does.
	tr.mounts(t, mountLine("/mnt/mixed", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/mixed")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if got {
		t.Error("AllNonRotational(/mnt/mixed) = true for a pool spanning an SSD and an HDD, want false: " +
			"the mount source names only the SSD, so every backing device must be enumerated")
	}
}

// A multi-device pool where every device is an SSD is still a pool. Without
// this, a fix that answered "rotational" for anything multi-device would pass
// the mixed-pool test above for the wrong reason.
func TestMultiDeviceSSDPoolIsAPool(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "nvme2n1", 0)
	tr.partition(t, "nvme2n1", "nvme2n1p1")
	tr.btrfs(t, "fsid-ssd", "nvme0n1p1", "nvme2n1p1")
	tr.mounts(t, mountLine("/mnt/vms", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/vms")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if !got {
		t.Error("AllNonRotational(/mnt/vms) = false for an all-SSD multi-device pool, want true")
	}
}

// A btrfs volume whose device list cannot be enumerated is UNDETERMINED, never
// judged by the single device the mount happens to name.
//
// This is the one place uncertainty could have resolved toward the POOL. A
// container without /sys/fs/btrfs, or a kernel laying sysfs out differently,
// leaves a mixed SSD+HDD pool named by its SSD - and answering from that name
// would size the spinning half with no spin-up allowance. The fixture is the
// mixed pool from the test above with its sysfs declaration removed, which is
// exactly the shape a hostile review reproduced against the earlier fallback.
func TestBtrfsWithoutSysfsIsUndetermined(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "sdb", 1)
	tr.partition(t, "sdb", "sdb1")
	// No tr.btrfs(...): the filesystem spans both devices, but sysfs cannot say so.
	tr.mounts(t, mountLine("/mnt/mixed", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/mixed")
	if err == nil {
		t.Errorf("AllNonRotational(/mnt/mixed) = (%v, nil) with btrfs sysfs absent, "+
			"want undetermined: the mount source names only one device and a btrfs "+
			"volume may span more, so it cannot answer for every backing device", got)
	}
	if got {
		t.Error("AllNonRotational(/mnt/mixed) = true with btrfs sysfs absent: this is " +
			"the mixed SSD+HDD pool judged by its SSD alone, the one direction this " +
			"package must never fail in")
	}
}

// A non-btrfs filesystem is still answered from its single mount source. Without
// this, returning undetermined for EVERYTHING would satisfy the test above and
// disable pool sizing entirely.
func TestNonBtrfsStillUsesTheMountSource(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.mounts(t, mountLine("/mnt/single", "xfs", "/dev/nvme0n1"))

	got, err := tr.p.AllNonRotational("/mnt/single")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if !got {
		t.Error("AllNonRotational(/mnt/single) = false for an SSD-backed xfs mount, want true")
	}
}

// An Unraid array member is an md device that carries its own queue and has NO
// "partition" file. Stripping a trailing "pN" by name would look for md1, which
// does not exist in sysfs, so this asserts the partition-file test is what
// decides - not the device's name.
func TestMdDeviceResolvesWithoutAParent(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "md1p1", 1) // named like a partition, but IS the device
	tr.mounts(t, mountLine("/mnt/disk1", "xfs", "/dev/md1p1"))

	got, err := tr.p.AllNonRotational("/mnt/disk1")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if got {
		t.Error("AllNonRotational(/mnt/disk1) = true for a spinning md device, want false")
	}
}

// Every shape that cannot be answered must resolve toward the ARRAY. Each of
// these is a real deployment: a container with no /sys, a FUSE or network mount
// with no block device, a member that is not mounted at all.
func TestUndeterminedShapesReportAnError(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, tr *sysTree)
		probe string
	}{
		{
			name:  "member not in mountinfo",
			setup: func(_ *testing.T, _ *sysTree) {},
			probe: "/mnt/absent",
		},
		{
			name: "source is not a block device",
			setup: func(t *testing.T, tr *sysTree) {
				tr.mounts(t, mountLine("/mnt/user", "fuse.shfs", "shfs"))
			},
			probe: "/mnt/user",
		},
		{
			name: "block device missing from sysfs",
			setup: func(t *testing.T, tr *sysTree) {
				tr.mounts(t, mountLine("/mnt/gone", "xfs", "/dev/sdz1"))
			},
			probe: "/mnt/gone",
		},
		{
			name: "rotational file holds something unparseable",
			setup: func(t *testing.T, tr *sysTree) {
				dir := filepath.Join(tr.root, "sys", "block", "sdq", "queue")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "rotational"), []byte("maybe\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				tr.link(t, "sdq", filepath.Join(tr.root, "sys", "block", "sdq"))
				tr.mounts(t, mountLine("/mnt/odd", "xfs", "/dev/sdq"))
			},
			probe: "/mnt/odd",
		},
		{
			name: "mountinfo itself is unreadable",
			setup: func(t *testing.T, tr *sysTree) {
				if err := os.Remove(tr.p.mountinfo); err != nil {
					t.Fatal(err)
				}
			},
			probe: "/mnt/cache",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newSysTree(t)
			tc.setup(t, tr)
			got, err := tr.p.AllNonRotational(tc.probe)
			if err == nil {
				t.Fatalf("AllNonRotational(%s) returned no error, want undetermined", tc.probe)
			}
			if got {
				t.Errorf("AllNonRotational(%s) = true alongside an error: an undetermined "+
					"answer must never read as non-rotational", tc.probe)
			}
		})
	}
}

// A mount source naming a path outside /sys must not be followed. These values
// come from mountinfo, so the probe treats them as untrusted input.
func TestTraversalInTheSourceIsRejected(t *testing.T) {
	for _, src := range []string{"/dev/../../etc/passwd", "/dev/../sys/block/sda", "/dev/"} {
		tr := newSysTree(t)
		tr.disk(t, "sda", 0)
		tr.mounts(t, mountLine("/mnt/evil", "xfs", src))

		got, err := tr.p.AllNonRotational("/mnt/evil")
		if err == nil || got {
			t.Errorf("AllNonRotational with source %q = (%v, %v), want undetermined", src, got, err)
		}
	}
}

// The LAST mountinfo entry for a path wins: a later mount over the same
// directory shadows an earlier one, so it is what a read of that path reaches.
func TestLastMountEntryWins(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.disk(t, "sda", 1)
	tr.mounts(t,
		mountLine("/mnt/shadowed", "xfs", "/dev/nvme0n1"),
		mountLine("/mnt/shadowed", "xfs", "/dev/sda"), // mounted over the top
	)

	got, err := tr.p.AllNonRotational("/mnt/shadowed")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if got {
		t.Error("AllNonRotational(/mnt/shadowed) = true, want false: the LAST entry " +
			"shadows the earlier one, and it names a spinning disk")
	}
}

// A mount point containing a space is octal-escaped in mountinfo. Without
// decoding it the entry never matches its own member and every such pool
// silently answers undetermined.
func TestEscapedMountPointMatches(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.mounts(t, mountLine(`/mnt/my\040pool`, "xfs", "/dev/nvme0n1"))

	got, err := tr.p.AllNonRotational("/mnt/my pool")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if !got {
		t.Error(`AllNonRotational("/mnt/my pool") = false, want true: the mountinfo ` +
			`entry escapes the space as \040 and must be decoded before matching`)
	}
}

// The osFS wiring must actually consult the real prober, and must NEVER be
// skipped.
//
// Every other test here drives sysProber directly, which proves the prober works
// and says NOTHING about whether the shipped binary uses it -
// osFS.AllNonRotational is the only path cmd/preloadd takes. Three constant
// stubs must fail: "true, nil" makes every member a pool, and both
// "false, errUndetermined" and "false, nil" make pool sizing permanently dead
// while every other test stays green, which is exactly the #119 defect.
//
// Two earlier versions of this test failed differently and both are worth
// recording, because each looked correct:
//
//   - Comparing error STRINGS to tell a real answer from a stub. Every failure
//     in the prober returns the same errUndetermined sentinel, so that branch
//     was dead and only the "true, nil" stub died.
//   - Discovering a block-backed mount from the host. That SKIPS wherever none
//     exists - a rootless runner, gVisor, a read-only /etc - and a silent skip
//     on the only test covering the shipped path is how #119 recurs unnoticed.
//     Reproduced: unmounting the container's block-backed binds made it skip
//     while the suite still reported ok.
//
// So the host is not consulted at all. hostProber is pointed at a fabricated
// tree, which makes the wiring answer THREE different values - a pool, a
// spinning disk, and undetermined - from one fixture. No constant satisfies all
// three, and nothing here can be skipped.
func TestOSWiringUsesTheRealProber(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.disk(t, "sda", 1)
	tr.mounts(t,
		mountLine("/mnt/ssd", "xfs", "/dev/nvme0n1"),
		mountLine("/mnt/hdd", "xfs", "/dev/sda"),
		mountLine("/mnt/nodev", "tmpfs", "tmpfs"),
	)

	restore := hostProber
	hostProber = func() sysProber { return tr.p }
	t.Cleanup(func() { hostProber = restore })

	cases := []struct {
		member  string
		want    bool
		wantErr bool
		why     string
	}{
		{"/mnt/ssd", true, false, "an SSD-backed mount is non-rotational"},
		{"/mnt/hdd", false, false, "an HDD-backed mount spins"},
		{"/mnt/nodev", false, true, "a source naming no block device is undetermined"},
	}
	for _, tc := range cases {
		got, err := (osFS{}).AllNonRotational(tc.member)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("osFS.AllNonRotational(%s) = (%v, %v), want (%v, err!=nil %v): %s - "+
				"the wiring is not reaching the real prober",
				tc.member, got, err, tc.want, tc.wantErr, tc.why)
		}
	}
}

// The default wiring must point at the HOST's /proc and /sys, not at whatever a
// test last set. Without this, hostProber could be left pointing anywhere and
// every assertion above would still pass while the shipped binary read a path
// that does not exist.
func TestHostProberDefaultsToTheRealInterfaces(t *testing.T) {
	p := hostProber()
	if p.mountinfo != defaultMountinfo || p.sysRoot != defaultSysRoot {
		t.Errorf("hostProber() = {mountinfo:%q sysRoot:%q}, want {%q %q}: the shipped "+
			"binary must read the host's real kernel interfaces",
			p.mountinfo, p.sysRoot, defaultMountinfo, defaultSysRoot)
	}
}

// The device-name guard must stop a read from ESCAPING the sysfs root.
//
// The obvious form of this test - pass "../x" and assert undetermined - passes
// with the guard DELETED, because such a name resolves to nothing and the read
// fails anyway. That is a test that cannot fail for the reason it claims. What
// the guard actually buys is refusing to follow a name to a real directory
// OUTSIDE the sysfs root: here a planted tree with a valid-looking
// queue/rotational, which without the guard is read and BELIEVED, yielding a
// confident "non-rotational" - a pool - from a file that is not sysfs at all.
func TestRotationalWillNotReadOutsideSysRoot(t *testing.T) {
	tr := newSysTree(t)

	// A directory outside sysRoot that looks like a block device to this code.
	planted := filepath.Join(tr.root, "planted", "queue")
	if err := os.MkdirAll(planted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planted, "rotational"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sysRoot is <root>/sys, so class/block/<name> is three levels deep in it:
	// this walks back out to <root> and into the planted tree.
	escape := filepath.Join("..", "..", "..", "planted")

	got, err := tr.p.rotational(escape)
	if err == nil {
		t.Errorf("rotational(%q) = (%v, nil), want undetermined: the name walks "+
			"outside the sysfs root and must not be followed", escape, got)
	}
	if got {
		t.Errorf("rotational(%q) = true: a planted file outside sysfs was read and "+
			"believed, classifying unknown storage as a pool", escape)
	}
}

// Plain non-device names are refused too. Kept separate from the escape case
// above because these also fail for a second reason (they resolve to nothing),
// so this asserts the refusal rather than the guard.
func TestRotationalRefusesNonDeviceNames(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "sda", 0)

	for _, name := range []string{"", ".", "..", "sub/sda", `back\slash`} {
		got, err := tr.p.rotational(name)
		if err == nil {
			t.Errorf("rotational(%q) = (%v, nil), want undetermined: only a bare "+
				"device name may be resolved under /sys", name, got)
		}
		if got {
			t.Errorf("rotational(%q) = true: a refused name must never read as "+
				"non-rotational", name)
		}
	}
}

// A device name claimed by TWO btrfs filesystems is UNDETERMINED, never resolved
// by picking one.
//
// Returning the first match in directory order is a coin flip: here a stale
// volume listing only the SSD sorts BEFORE the live mixed pool that also holds a
// spinning disk, so the stale list wins and the member reads as non-rotational -
// the stall-reintroducing direction. A btrfs replace remnant or a second pool
// sharing a device name reaches this, and nothing in sysfs says which filesystem
// the mount belongs to.
//
// The ordering is deliberate: "aaa-stale" sorts first, so a first-match
// implementation fails this test rather than passing by luck.
func TestAmbiguousBtrfsDeviceIsUndetermined(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "sdb", 1)
	tr.partition(t, "sdb", "sdb1")
	tr.btrfs(t, "aaa-stale", "nvme0n1p1")        // SSD only, sorts FIRST
	tr.btrfs(t, "zzz-real", "nvme0n1p1", "sdb1") // the live mixed pool
	tr.mounts(t, mountLine("/mnt/mixed", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/mixed")
	if err == nil {
		t.Errorf("AllNonRotational(/mnt/mixed) = (%v, nil) with the device claimed by "+
			"two filesystems, want undetermined: which one the mount belongs to is "+
			"not knowable here", got)
	}
	if got {
		t.Error("AllNonRotational(/mnt/mixed) = true: the stale SSD-only device list " +
			"won on sort order, hiding the spinning disk in the real pool")
	}
}

// A single unambiguous filesystem among several still resolves. Without this,
// answering undetermined for every host that has more than one btrfs pool - the
// normal Unraid case - would satisfy the test above and disable pool sizing.
func TestSeveralBtrfsFilesystemsStillResolve(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "nvme1n1", 0)
	tr.partition(t, "nvme1n1", "nvme1n1p1")
	// Two pools, disjoint device lists - exactly the measured host layout.
	tr.btrfs(t, "fsid-cache", "nvme1n1p1")
	tr.btrfs(t, "fsid-vms", "nvme0n1p1")
	tr.mounts(t, mountLine("/mnt/vms", "btrfs", "/dev/nvme0n1p1"))

	got, err := tr.p.AllNonRotational("/mnt/vms")
	if err != nil {
		t.Fatalf("AllNonRotational: unexpected error %v", err)
	}
	if !got {
		t.Error("AllNonRotational(/mnt/vms) = false with two disjoint btrfs pools " +
			"present, want true: only a SHARED device name is ambiguous")
	}
}

// A mountinfo line too long for the scanner aborts the whole read as
// UNDETERMINED, rather than answering from the lines read so far.
//
// bufio.Scanner stops at 64KB per line and reports the failure only through
// Err(). Ignoring it would answer from a TRUNCATED view of mountinfo, which
// silently defeats the "last entry wins" rule: a shadowing mount that appears
// after the oversized line would be invisible, and the earlier entry - here a
// non-rotational one - would be believed. That is the direction this package
// must never fail in, and it is reachable through a pathological mount source.
func TestOversizedMountinfoLineIsUndetermined(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.disk(t, "sda", 1)
	tr.mounts(t,
		mountLine("/mnt/shadowed", "xfs", "/dev/nvme0n1"), // the SSD, read first
		mountLine("/mnt/pad", "xfs", "/dev/"+strings.Repeat("x", 70000)),
		mountLine("/mnt/shadowed", "xfs", "/dev/sda"), // the HDD that shadows it
	)

	got, err := tr.p.AllNonRotational("/mnt/shadowed")
	if err == nil {
		t.Errorf("AllNonRotational(/mnt/shadowed) = (%v, nil) with an unreadable "+
			"mountinfo, want undetermined: the scan aborted before the shadowing "+
			"entry, so no answer here is trustworthy", got)
	}
	if got {
		t.Error("AllNonRotational(/mnt/shadowed) = true: answered from a truncated " +
			"mountinfo, believing the SSD entry the later HDD mount shadows")
	}
}

// A filesystem on a device-mapper target mounts by its MAPPER name, which this
// code does not resolve - so it answers undetermined and is sized for the array.
//
// This pins a known, deliberate gap rather than a desired behavior: an
// encrypted Unraid array or a LUKS pool loses pool sizing entirely and silently.
// The test exists so that loss is visible in the suite instead of being
// rediscovered from an unexplained absence, and so that anyone implementing
// mapper resolution finds the case already described. It must NOT be read as an
// assertion that undetermined is the right long-term answer here.
func TestMapperSourceIsUndetermined(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "dm-0", 0) // the dm target itself IS in /sys/class/block
	tr.mounts(t, mountLine("/mnt/encrypted", "ext4", "/dev/mapper/wap-crypt"))

	got, err := tr.p.AllNonRotational("/mnt/encrypted")
	if err == nil {
		t.Errorf("AllNonRotational(/mnt/encrypted) = (%v, nil) for a /dev/mapper "+
			"source, want undetermined: the mapper name is not resolved to its "+
			"dm device, so nothing is known about the backing storage", got)
	}
	if got {
		t.Error("AllNonRotational(/mnt/encrypted) = true for an unresolved mapper " +
			"source: an undetermined answer must never read as non-rotational")
	}
}

// A btrfs filesystem whose device list cannot be READ makes the answer
// undetermined, rather than being passed over in favor of a readable one.
//
// Skipping it reaches the exact wrong answer the ambiguity check exists to
// prevent, through a different door: the unreadable filesystem may be the one
// holding this device and may list a spinning member, so answering from a
// readable SSD-only list classifies a pool that has an HDD in it. Here the
// readable list is SSD-only and the unreadable one holds the mixed pool.
func TestUnreadableBtrfsDeviceListIsUndetermined(t *testing.T) {
	tr := newSysTree(t)
	tr.disk(t, "nvme0n1", 0)
	tr.partition(t, "nvme0n1", "nvme0n1p1")
	tr.disk(t, "sdb", 1)
	tr.partition(t, "sdb", "sdb1")
	tr.btrfs(t, "aaa-readable", "nvme0n1p1")
	tr.btrfs(t, "zzz-unreadable", "nvme0n1p1", "sdb1")
	tr.mounts(t, mountLine("/mnt/mixed", "btrfs", "/dev/nvme0n1p1"))

	// Make the second filesystem's device list unreadable. Running as root
	// ignores the permission bits, so the directory is replaced by a FILE:
	// ReadDir fails on it for every user, which is the condition under test.
	dir := filepath.Join(tr.root, "sys", "fs", "btrfs", "zzz-unreadable", "devices")
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := tr.p.AllNonRotational("/mnt/mixed")
	if err == nil {
		t.Errorf("AllNonRotational(/mnt/mixed) = (%v, nil) with one device list "+
			"unreadable, want undetermined: that list may name a spinning member", got)
	}
	if got {
		t.Error("AllNonRotational(/mnt/mixed) = true: answered from the readable " +
			"SSD-only list while an unreadable one holds the pool's spinning disk")
	}
}
