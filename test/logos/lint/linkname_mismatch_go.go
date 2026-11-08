// errorcheck

// correctness::linkname_mismatch (M9-S9): a //go:linkname local pkg.name
// reports when pkg is an import of this package, the named symbol is
// visible in its export data, and the signatures differ. A remote that the
// import graph cannot resolve is not checked.

package p

import (
	"sync"
	_ "unsafe"
)

//go:linkname goodUnlock sync.(*Mutex).Unlock
func goodUnlock(m *sync.Mutex)

//go:linkname badLock sync.(*Mutex).Lock
func badLock(n int) // ERROR "\[correctness::linkname_mismatch\]"

//go:linkname badWait sync.WaitGroup.Done
func badWait(x int) // ERROR "\[correctness::linkname_mismatch\]"

//go:linkname detached runtime.fastrand

//go:linkname notImported os.Getppid
func notImported()

func use(m *sync.Mutex, w *sync.WaitGroup) {
	goodUnlock(m)
	badLock(3)
	badWait(4)
	_ = w
}
