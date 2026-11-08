// errorcheck

// REDTEAM #194: an unguarded field read after the deferred unlock was
// moved. The closure is deferred before the unlock, so LIFO runs the
// unlock first and the closure body reads the guarded field without the
// lock.

package p

import "sync"

type stats struct {
	mu sync.Mutex

	//logos:guardedby mu
	total int
}

func movedUnlock(s *stats) {
	s.mu.Lock()
	defer func() {
		_ = s.total // ERROR "\[concurrency::unguarded_access\]"
	}()
	defer s.mu.Unlock()
}
