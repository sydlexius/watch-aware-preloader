//go:build !linux

package diskresolve

// AllNonRotational has no portable implementation off Linux: rotational status
// comes from /sys/block/<dev>/queue/rotational, which is a Linux interface.
// It always answers UNDETERMINED rather than guessing, and isPool resolves that
// toward the array.
//
// This is not a gap in coverage for the shipped product. The plugin runs on
// Unraid, which is Linux; the stub exists so the package builds and its
// non-Linux behavior is stated rather than accidental (relevant to #60, where
// a portable deployment may have no usable /sys either).
func (osFS) AllNonRotational(string) (bool, error) { return false, errUndetermined }
