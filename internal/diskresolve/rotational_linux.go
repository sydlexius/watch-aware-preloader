//go:build linux

package diskresolve

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Default kernel interfaces used to answer "does this member spin?". They are
// fields on sysProber rather than constants so a test can point the whole walk
// at a fabricated tree and exercise the real parsing, instead of mocking it.
const (
	defaultMountinfo = "/proc/self/mountinfo"
	defaultSysRoot   = "/sys"
)

// sysProber answers rotational questions from /proc and /sys.
type sysProber struct {
	mountinfo string
	sysRoot   string
}

func newSysProber() sysProber {
	return sysProber{mountinfo: defaultMountinfo, sysRoot: defaultSysRoot}
}

// hostProber is the prober osFS.AllNonRotational uses. It is a var solely so a
// test can point the REAL wiring at a fabricated /proc and /sys and assert what
// it answers.
//
// The seam exists because the alternative did not work: a test that discovers a
// block-backed mount on the host SKIPS wherever none exists (a rootless or
// read-only-/etc runner), and a silent skip on the only test covering the path
// the shipped binary takes is how the #119 defect - a permanently-inert
// predicate with a green suite - goes unnoticed a second time.
var hostProber = newSysProber

// AllNonRotational reports whether EVERY device backing member is
// non-rotational. Any device that spins, and any device whose status cannot be
// read, makes the whole member rotational - a pool mixing an SSD and an HDD
// spins down, so a single spinning member decides the answer.
func (p sysProber) AllNonRotational(member string) (bool, error) {
	devs, err := p.backingDevices(member)
	if err != nil {
		return false, err
	}
	// Defence in depth, not a reachable branch today: every path in
	// backingDevices either returns a non-empty list or an error. It is kept
	// because the loop below would otherwise answer "non-rotational" for an
	// empty list - vacuously true, and the dangerous direction - if a future
	// backing-device source ever returned one.
	if len(devs) == 0 {
		return false, errUndetermined
	}
	for _, d := range devs {
		rot, err := p.rotational(d)
		if err != nil {
			return false, err
		}
		if rot {
			return false, nil
		}
	}
	return true, nil
}

// backingDevices names the block devices behind member, as they appear in
// /sys/class/block.
//
// The mount source alone is not enough for every filesystem. A btrfs volume may
// span several devices while /proc/self/mountinfo names only the one it was
// mounted by - measured on a live array, where a pool reporting a single
// nvme0n1p1 source was actually built from nvme0n1p1 and nvme2n1p1. Trusting
// the source there would judge a mixed SSD+HDD pool by whichever device the
// mount happened to name, so btrfs is expanded through sysfs instead.
func (p sysProber) backingDevices(member string) ([]string, error) {
	fstype, source, err := p.mountSource(member)
	if err != nil {
		return nil, err
	}
	// A source that is not a block device (shfs, tmpfs, rootfs, an NFS export)
	// has no rotational status to read. That is undetermined, not "does not
	// spin": the caller must not treat an unrecognized backing store as a pool.
	name, ok := strings.CutPrefix(source, "/dev/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return nil, errUndetermined
	}
	if fstype == "btrfs" {
		// A btrfs volume may span devices the mount source does not name, so
		// the source alone cannot answer "is EVERY backing device
		// non-rotational". Enumeration failing is therefore UNDETERMINED, not
		// a licence to judge the one device that happens to be named.
		//
		// Falling back to the named source here would be the one place in this
		// package where uncertainty resolves toward the POOL: on a container
		// without /sys/fs/btrfs, or a kernel laying sysfs out differently, a
		// pool mixing an SSD and an HDD would be judged solely by its SSD and
		// sized with no spin-up allowance. Answering undetermined instead costs
		// a little cache budget on an all-SSD pool whose sysfs cannot be read,
		// which is the direction this path is built to fail in.
		return p.btrfsDevices(name)
	}
	return []string{name}, nil
}

// mountSource returns the filesystem type and mount source for a mount point,
// read from /proc/self/mountinfo.
//
// The LAST matching line wins: mountinfo lists mounts in order, and a later
// mount over the same directory shadows an earlier one, so the last entry is
// what a read of that path actually reaches.
func (p sysProber) mountSource(member string) (fstype, source string, err error) {
	f, err := os.Open(p.mountinfo)
	if err != nil {
		return "", "", errUndetermined
	}
	defer func() { _ = f.Close() }()

	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// Format: ID PARENT MAJ:MIN ROOT MOUNTPOINT OPTS [OPTIONAL...] - FSTYPE SOURCE OPTS
		// The optional fields are variable in number and terminated by a lone
		// "-", so the line has to be split on that separator rather than by a
		// fixed field index.
		line := sc.Text()
		left, right, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		lf := strings.Fields(left)
		rf := strings.Fields(right)
		if len(lf) < 5 || len(rf) < 2 {
			continue
		}
		// Mount points are octal-escaped in mountinfo (a space is \040).
		if unescapeMountinfo(lf[4]) != member {
			continue
		}
		fstype, source, found = rf[0], unescapeMountinfo(rf[1]), true
	}
	if err := sc.Err(); err != nil || !found {
		return "", "", errUndetermined
	}
	return fstype, source, nil
}

// unescapeMountinfo decodes the octal escapes the kernel writes for space,
// tab, newline and backslash in mountinfo paths. Without it a share directory
// with a space in its name never matches its own mount entry.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			var v int
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok && v < 256 {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// btrfsDevices lists every device in the btrfs filesystem that contains the
// named device, via /sys/fs/btrfs/<fsid>/devices.
//
// A device name that appears under MORE THAN ONE fsid is AMBIGUOUS and answers
// undetermined rather than picking one. Returning the first match in directory
// order would be a coin flip resolved toward whichever fsid sorts first: given a
// stale volume listing only an SSD and the live mixed pool listing that SSD plus
// an HDD, the stale one can win and the pool classifies as non-rotational -
// the stall-reintroducing direction. A btrfs replace remnant or a second pool
// sharing a device name is enough to reach it, and nothing here can tell which
// fsid the mount belongs to.
//
// Entries under devices/ are SYMLINKS to the block devices on a real host, not
// directories, so they are read by NAME and never filtered by type. An IsDir
// check here would reject every genuine entry and silently disable pool sizing
// on every btrfs member.
func (p sysProber) btrfsDevices(name string) ([]string, error) {
	fsids, err := os.ReadDir(filepath.Join(p.sysRoot, "fs", "btrfs"))
	if err != nil {
		return nil, errUndetermined
	}
	var found []string
	for _, fsid := range fsids {
		if !fsid.IsDir() {
			continue
		}
		dir := filepath.Join(p.sysRoot, "fs", "btrfs", fsid.Name(), "devices")
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A filesystem whose device list cannot be read might be the one
			// holding this device, and it might list a spinning member the
			// readable lists do not. Skipping it would let a readable
			// SSD-only list answer for a pool that has an HDD in it - the
			// same wrong answer the ambiguity check below exists to prevent,
			// reached through a different door.
			return nil, errUndetermined
		}
		var devs []string
		member := false
		for _, e := range entries {
			devs = append(devs, e.Name())
			if e.Name() == name {
				member = true
			}
		}
		if !member {
			continue
		}
		if found != nil {
			// A second filesystem claims the same device name. Which one the
			// mount actually belongs to is not knowable from here.
			return nil, errUndetermined
		}
		found = devs
	}
	if found == nil {
		return nil, errUndetermined
	}
	return found, nil
}

// rotational reads queue/rotational for a device, resolving a partition to the
// whole disk that carries the queue.
//
// Three device shapes appear on a real array and all three resolve the same
// way: a partition of an NVMe drive (nvme0n1p1 -> nvme0n1), a partition of a
// SCSI disk (sda1 -> sda), and an Unraid md device (md1p1), which carries no
// "partition" file and IS the device. Stripping a trailing "pN" by name would
// break the md case, because /sys/block/md1 does not exist while md1p1 does -
// so the presence of the "partition" file decides it instead.
func (p sysProber) rotational(name string) (bool, error) {
	// Reject anything that is not a bare device name before touching the
	// filesystem: these values come from mountinfo, and a "../" in one must not
	// be able to walk the read outside /sys. EvalSymlinks below would fail on
	// such a name anyway on any host where the traversed path does not exist,
	// so this is a second line rather than the only one - it makes the refusal
	// explicit and independent of what happens to exist on disk.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
		return false, errUndetermined
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(p.sysRoot, "class", "block", name))
	if err != nil {
		return false, errUndetermined
	}
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		dir = filepath.Dir(dir)
	}
	b, err := os.ReadFile(filepath.Join(dir, "queue", "rotational"))
	if err != nil {
		return false, errUndetermined
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return false, errUndetermined
	}
}

// AllNonRotational reports whether every device backing name is non-rotational,
// read from the host's real /proc and /sys.
//
// This is the wiring the shipped binary actually takes, and it is deliberately
// the whole of it: everything it could get wrong lives in sysProber, which is
// tested directly, so the only defect this method can carry is failing to
// delegate. TestOSWiringUsesTheRealProber pins exactly that.
func (osFS) AllNonRotational(name string) (bool, error) {
	return hostProber().AllNonRotational(name)
}
