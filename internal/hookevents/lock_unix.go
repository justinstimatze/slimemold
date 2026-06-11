//go:build !windows

package hookevents

import (
	"os"
	"syscall"
)

// tryLockForRotation takes an advisory exclusive, non-blocking lock on f.
// Returns an unlock func and true on success; (nil, false) if the lock is
// already held by a sibling racer mid-rotation. The unlock func is safe to
// call exactly once and only when ok is true.
//
// Unix: flock(2). The kernel releases the lock when the holder dies, so a
// crash between acquisition and rename can't orphan it.
func tryLockForRotation(f *os.File) (unlock func(), ok bool) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, true
}
