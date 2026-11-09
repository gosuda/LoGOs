// errorcheck

package p

import "sync"

type gate struct {
	mu sync.Mutex

	//logos:guardedby mu
	open bool
}

//logos:requires g.mu
func (g *gate) toggle() {
	g.open = !g.open
}

//logos:acquires g.mu
func (g *gate) seal() {}

//logos:releases g.mu
func (g *gate) release() {}

func goodCall(g *gate) {
	g.mu.Lock()
	g.toggle()
	g.mu.Unlock()
}

func badCall(g *gate) {
	g.toggle() // ERROR "\[concurrency::lock_requirement_unmet\]"
}

func wrongLock(g, other *gate) {
	other.mu.Lock()
	g.toggle() // ERROR "\[concurrency::lock_requirement_unmet\]"
	other.mu.Unlock()
}

func acquireThenGood(g *gate) {
	g.seal()
	g.toggle()
}

func releaseThenAccess(g *gate) {
	g.mu.Lock()
	g.release()
	g.open = true // ERROR "\[concurrency::unguarded_access\]"
}

func relockAfterRelease(g *gate) {
	g.mu.Lock()
	g.release()
	g.mu.Lock()
	g.toggle()
	g.mu.Unlock()
}

func deferReleaseKeepsHeld(g *gate) {
	g.mu.Lock()
	defer g.release()
	g.toggle()
}
