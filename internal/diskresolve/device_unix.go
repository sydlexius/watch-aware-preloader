//go:build unix

package diskresolve

import (
	"fmt"
	"os"
	"syscall"
)

// deviceOf extracts the device number from a FileInfo.
//
// The type assertion is the only way to reach st_dev from os.FileInfo, and it
// can fail on a FileInfo that did not come from a real syscall (a fake used in
// a test, or a filesystem implementation that synthesizes its own). A failure
// is reported rather than defaulted, so the caller treats it as UNDETERMINED
// and resolves toward the array instead of silently reading a zero as a real
// device number - which would make every path look like it shared a device.
// The device number is returned as a string rather than a numeric type on
// purpose: syscall.Stat_t.Dev is int32 on darwin and uint64 on linux, so any
// single numeric type needs a platform-dependent conversion that gosec flags
// as a potential overflow (G115). Device numbers are only ever compared for
// EQUALITY here, never ordered or arithmetic, so formatting sidesteps the
// conversion without weakening anything.
func deviceOf(fi os.FileInfo) (string, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	return fmt.Sprint(st.Dev), true
}
