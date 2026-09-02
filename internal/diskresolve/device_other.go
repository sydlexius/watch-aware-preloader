//go:build !unix

package diskresolve

import "os"

// deviceOf has no portable implementation off unix, so it always reports
// "undetermined" rather than guessing. Every caller resolves that toward the
// array, which is the safe direction: the plugin ships for Linux, and a host
// without device numbers has no Unraid pools to detect in the first place.
func deviceOf(os.FileInfo) (string, bool) { return "", false }
