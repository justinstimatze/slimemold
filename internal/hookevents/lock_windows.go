//go:build windows

package hookevents

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLockForRotation takes an advisory exclusive, non-blocking lock on f.
// Returns an unlock func and true on success; (nil, false) if the lock is
// already held by a sibling racer mid-rotation. The unlock func is safe to
// call exactly once and only when ok is true.
//
// Windows: LockFileEx with LOCKFILE_FAIL_IMMEDIATELY (non-blocking) +
// LOCKFILE_EXCLUSIVE_LOCK over a one-byte range at offset 0. Like flock(2)
// on Unix, the lock is released when the holding process dies, so a crash
// between acquisition and rename can't orphan it. The file is opened
// O_RDONLY (GENERIC_READ), which is sufficient for LockFileEx.
func tryLockForRotation(f *os.File) (unlock func(), ok bool) {
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		h,
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
	if err != nil {
		return nil, false
	}
	return func() {
		_ = windows.UnlockFileEx(h, 0, 1, 0, new(windows.Overlapped))
	}, true
}
