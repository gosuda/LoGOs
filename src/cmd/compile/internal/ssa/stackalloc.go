// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssa

import (
	"cmd/compile/internal/ir"
	"cmd/compile/internal/types"
	"cmd/internal/src"
	"fmt"
	"math/bits"
)

type stackLive struct {
	ids   []ID
	words [][]stackLiveWord
}

type stackLiveWord struct {
	index int
	bits  uint64
}

func (l stackLive) blockWords(bid ID) []stackLiveWord {
	row := l.words[bid]
	return row[:len(row):len(row)]
}

type stackAllocState struct {
	f *Func

	live stackLive

	// The following slices are reused across multiple users
	// of stackAllocState.
	values    []stackValState
	interfere [][]ID // interfere[v.id] = values that interfere with v.
	names     []LocalSlot

	nArgSlot, // Number of Values sourced to arg slot
	nNotNeed, // Number of Values not needing a stack slot
	nNamedSlot, // Number of Values using a named stack slot
	nReuse, // Number of values reusing a stack slot
	nAuto, // Number of autos allocated for stack slots.
	nSelfInterfere int32 // Number of self-interferences
}

func newStackAllocState(f *Func) *stackAllocState {
	s := f.Cache.stackAllocState
	if s == nil {
		return new(stackAllocState)
	}
	if s.f != nil {
		f.fe.Fatalf(src.NoXPos, "newStackAllocState called without previous free")
	}
	return s
}

func putStackAllocState(s *stackAllocState) {
	clear(s.values)
	clear(s.interfere)
	clear(s.names)
	s.f.Cache.stackAllocState = s
	s.f = nil
	s.live = stackLive{}
	s.nArgSlot, s.nNotNeed, s.nNamedSlot, s.nReuse, s.nAuto, s.nSelfInterfere = 0, 0, 0, 0, 0, 0
}

type stackValState struct {
	typ       *types.Type
	spill     *Value
	needSlot  bool
	isArg     bool
	defBlock  ID
	useBlocks []stackUseBlock
}

// addUseBlock adds a block to the set of blocks that uses this value.
// Note that we only loosely enforce the set property by checking the last
// block that was appended to the list and duplicates may occur.
// Because we add values block by block (barring phi-nodes), the number of duplicates is
// small and we deduplicate as part of the liveness algorithm later anyway.
func (sv *stackValState) addUseBlock(b *Block, liveout bool) {
	entry := stackUseBlock{
		b:       b,
		liveout: liveout,
	}
	if sv.useBlocks == nil || sv.useBlocks[len(sv.useBlocks)-1] != entry {
		sv.useBlocks = append(sv.useBlocks, stackUseBlock{
			b:       b,
			liveout: liveout,
		})
	}
}

type stackUseBlock struct {
	b       *Block
	liveout bool
}

// stackalloc allocates storage in the stack frame for
// all Values that did not get a register.
func stackalloc(f *Func, spillLive [][]ID) stackLive {
	if f.pass.debug > stackDebug {
		fmt.Println("before stackalloc")
		fmt.Println(f.String())
	}
	s := newStackAllocState(f)
	s.init(f, spillLive)
	defer putStackAllocState(s)

	s.stackalloc()
	if f.pass.stats > 0 {
		f.LogStat("stack_alloc_stats",
			s.nArgSlot, "arg_slots", s.nNotNeed, "slot_not_needed",
			s.nNamedSlot, "named_slots", s.nAuto, "auto_slots",
			s.nReuse, "reused_slots", s.nSelfInterfere, "self_interfering")
	}

	return s.live
}

func (s *stackAllocState) init(f *Func, spillLive [][]ID) {
	s.f = f

	// Initialize value information.
	if n := f.NumValues(); cap(s.values) >= n {
		s.values = s.values[:n]
	} else {
		s.values = make([]stackValState, n)
	}
	for _, b := range f.Blocks {
		for _, v := range b.Values {
			s.values[v.ID].typ = v.Type
			s.values[v.ID].needSlot = !v.Type.IsMemory() && !v.Type.IsVoid() && !v.Type.IsFlags() && f.getHome(v.ID) == nil && !v.rematerializeable() && !v.OnWasmStack
			s.values[v.ID].isArg = hasAnyArgOp(v)
			s.values[v.ID].defBlock = b.ID
			if f.pass.debug > stackDebug && s.values[v.ID].needSlot {
				fmt.Printf("%s needs a stack slot\n", v)
			}
			if v.Op == OpStoreReg {
				s.values[v.Args[0].ID].spill = v
			}
		}
	}

	// Compute liveness info for values needing a slot.
	s.computeLive(spillLive)

	// Build interference graph among values needing a slot.
	s.buildInterferenceGraph()
}

func (s *stackAllocState) stackalloc() {
	f := s.f

	// Build map from values to their names, if any.
	// A value may be associated with more than one name (e.g. after
	// the assignment i=j). This step picks one name per value arbitrarily.
	if n := f.NumValues(); cap(s.names) >= n {
		s.names = s.names[:n]
	} else {
		s.names = make([]LocalSlot, n)
	}
	names := s.names
	empty := LocalSlot{}
	for _, name := range f.Names {
		// Note: not "range f.NamedValues" above, because
		// that would be nondeterministic.
		for _, v := range f.NamedValues[*name] {
			if v.Op == OpArgIntReg || v.Op == OpArgFloatReg {
				aux := v.Aux.(*AuxNameOffset)
				// Never let an arg be bound to a differently named thing.
				if name.N != aux.Name || name.Off != aux.Offset {
					if f.pass.debug > stackDebug {
						fmt.Printf("stackalloc register arg %s skipping name %s\n", v, name)
					}
					continue
				}
			} else if name.N.Class == ir.PPARAM && v.Op != OpArg {
				// PPARAM's only bind to OpArg
				if f.pass.debug > stackDebug {
					fmt.Printf("stackalloc PPARAM name %s skipping non-Arg %s\n", name, v)
				}
				continue
			}

			if names[v.ID] == empty {
				if f.pass.debug > stackDebug {
					fmt.Printf("stackalloc value %s to name %s\n", v, *name)
				}
				names[v.ID] = *name
			}
		}
	}

	// Allocate args to their assigned locations.
	for _, v := range f.Entry.Values {
		if !hasAnyArgOp(v) {
			continue
		}
		if v.Aux == nil {
			f.Fatalf("%s has nil Aux\n", v.LongString())
		}
		if v.Op == OpArg {
			loc := LocalSlot{N: v.Aux.(*ir.Name), Type: v.Type, Off: v.AuxInt}
			if f.pass.debug > stackDebug {
				fmt.Printf("stackalloc OpArg %s to %s\n", v, loc)
			}
			f.setHome(v, loc)
			continue
		}
		// You might think this below would be the right idea, but you would be wrong.
		// It almost works; as of 105a6e9518 - 2021-04-23,
		// GOSSAHASH=11011011001011111 == cmd/compile/internal/noder.(*noder).embedded
		// is compiled incorrectly.  I believe the cause is one of those SSA-to-registers
		// puzzles that the register allocator untangles; in the event that a register
		// parameter does not end up bound to a name, "fixing" it is a bad idea.
		//
		//if f.DebugTest {
		//	if v.Op == OpArgIntReg || v.Op == OpArgFloatReg {
		//		aux := v.Aux.(*AuxNameOffset)
		//		loc := LocalSlot{N: aux.Name, Type: v.Type, Off: aux.Offset}
		//		if f.pass.debug > stackDebug {
		//			fmt.Printf("stackalloc Op%s %s to %s\n", v.Op, v, loc)
		//		}
		//		names[v.ID] = loc
		//		continue
		//	}
		//}

	}

	// For each type, we keep track of all the stack slots we
	// have allocated for that type. This map is keyed by
	// strings returned by types.LinkString. This guarantees
	// type equality, but also lets us match the same type represented
	// by two different types.Type structures. See issue 65783.
	locations := map[string][]LocalSlot{}

	// Each time we assign a stack slot to a value v, we remember
	// the slot we used via an index into locations[v.Type].
	slots := f.Cache.allocIntSlice(f.NumValues())
	defer f.Cache.freeIntSlice(slots)
	for i := range slots {
		slots[i] = -1
	}

	// Pick a stack slot for each value needing one.
	used := f.Cache.allocBoolSlice(f.NumValues())
	defer f.Cache.freeBoolSlice(used)
	for _, b := range f.Blocks {
		for _, v := range b.Values {
			if !s.values[v.ID].needSlot {
				s.nNotNeed++
				continue
			}
			if hasAnyArgOp(v) {
				s.nArgSlot++
				continue // already picked
			}

			// If this is a named value, try to use the name as
			// the spill location.
			var name LocalSlot
			if v.Op == OpStoreReg {
				name = names[v.Args[0].ID]
			} else {
				name = names[v.ID]
			}
			if name.N != nil && v.Type.Compare(name.Type) == types.CMPeq {
				for _, id := range s.interfere[v.ID] {
					h := f.getHome(id)
					if h != nil && h.(LocalSlot).N == name.N && h.(LocalSlot).Off == name.Off {
						// A variable can interfere with itself.
						// It is rare, but it can happen.
						s.nSelfInterfere++
						goto noname
					}
				}
				if f.pass.debug > stackDebug {
					fmt.Printf("stackalloc %s to %s\n", v, name)
				}
				s.nNamedSlot++
				f.setHome(v, name)
				continue
			}

		noname:
			// Set of stack slots we could reuse.
			typeKey := v.Type.LinkString()
			locs := locations[typeKey]
			// Mark all positions in locs used by interfering values.
			for i := 0; i < len(locs); i++ {
				used[i] = false
			}
			for _, xid := range s.interfere[v.ID] {
				slot := slots[xid]
				if slot >= 0 {
					used[slot] = true
				}
			}
			// Find an unused stack slot.
			var i int
			for i = 0; i < len(locs); i++ {
				if !used[i] {
					s.nReuse++
					break
				}
			}
			// If there is no unused stack slot, allocate a new one.
			if i == len(locs) {
				s.nAuto++
				locs = append(locs, LocalSlot{N: f.NewLocal(v.Pos, v.Type), Type: v.Type, Off: 0})
				locations[typeKey] = locs
			}
			// Use the stack variable at that index for v.
			loc := locs[i]
			if f.pass.debug > stackDebug {
				fmt.Printf("stackalloc %s to %s\n", v, loc)
			}
			f.setHome(v, loc)
			slots[v.ID] = i
		}
	}
}

func (s *stackAllocState) computeLive(spillLive [][]ID) {
	s.collectLiveUses(spillLive)
	s.initializeLiveSlots()
	s.computeLiveWords()

	if s.f.pass.debug > stackDebug {
		for _, b := range s.f.Blocks {
			var ids []ID
			for _, chunk := range s.live.blockWords(b.ID) {
				for word := chunk.bits; word != 0; {
					ids = append(ids, s.live.ids[chunk.index*64+bits.TrailingZeros64(word)])
					word &= word - 1
				}
			}
			fmt.Printf("stacklive %s %v\n", b, ids)
		}
	}
}

func (s *stackAllocState) collectLiveUses(spillLive [][]ID) {
	f := s.f
	for _, b := range f.Blocks {
		for _, spillvid := range spillLive[b.ID] {
			val := &s.values[spillvid]
			val.addUseBlock(b, true)
		}
		for _, v := range b.Values {
			for i, a := range v.Args {
				val := &s.values[a.ID]
				useBlock := b
				forceLiveout := false
				if v.Op == OpPhi {
					useBlock = b.Preds[i].b
					forceLiveout = true
					if spill := val.spill; spill != nil {
						s.values[spill.ID].addUseBlock(useBlock, true)
					}
				}
				if !val.needSlot {
					continue
				}
				val.addUseBlock(useBlock, forceLiveout)
			}
		}
	}
}

func (s *stackAllocState) initializeLiveSlots() {
	f := s.f
	nslots := 0
	for _, v := range s.values {
		if v.needSlot {
			nslots++
		}
	}
	s.live = stackLive{
		ids:   make([]ID, nslots),
		words: make([][]stackLiveWord, f.NumBlocks()),
	}
	ordinal := 0
	for vid, v := range s.values {
		if v.needSlot {
			s.live.ids[ordinal] = ID(vid)
			ordinal++
		}
	}
}

type stackLiveBlock struct {
	defs    uint64
	in      uint64
	out     uint64
	pending uint64
}

type stackLiveFlow struct {
	blocks  []stackLiveBlock
	touched []ID
	queue   []ID
	offsets []int
	preds   []ID
}

func newStackLiveFlow(f *Func) stackLiveFlow {
	nblocks := f.NumBlocks()
	offsets := make([]int, nblocks+1)
	for _, b := range f.Blocks {
		offsets[b.ID+1] = len(b.Preds)
	}
	for i := 1; i < len(offsets); i++ {
		offsets[i] += offsets[i-1]
	}
	ids := make([]ID, 2*nblocks+offsets[nblocks])
	flow := stackLiveFlow{
		blocks:  make([]stackLiveBlock, nblocks),
		touched: ids[:0:nblocks],
		queue:   ids[nblocks : nblocks : 2*nblocks],
		offsets: offsets,
		preds:   ids[2*nblocks:],
	}
	for _, b := range f.Blocks {
		for i, edge := range b.Preds {
			flow.preds[offsets[b.ID]+i] = edge.b.ID
		}
	}
	return flow
}

func (flow *stackLiveFlow) touch(bid ID) {
	block := &flow.blocks[bid]
	if block.defs|block.in|block.out == 0 {
		flow.touched = append(flow.touched, bid)
	}
}

func (flow *stackLiveFlow) addInput(bid ID, mask uint64) {
	block := &flow.blocks[bid]
	fresh := mask &^ (block.defs | block.in)
	if fresh == 0 {
		return
	}
	flow.touch(bid)
	block.in |= fresh
	if block.pending == 0 {
		flow.queue = append(flow.queue, bid)
	}
	block.pending |= fresh
}

func (s *stackAllocState) seedLiveWord(flow *stackLiveFlow, ids []ID) {
	for bit, vid := range ids {
		bid := s.values[vid].defBlock
		flow.touch(bid)
		flow.blocks[bid].defs |= uint64(1) << uint(bit)
	}
	for bit, vid := range ids {
		mask := uint64(1) << uint(bit)
		for _, use := range s.values[vid].useBlocks {
			if use.liveout {
				flow.touch(use.b.ID)
				flow.blocks[use.b.ID].out |= mask
			}
			flow.addInput(use.b.ID, mask)
		}
	}
}

func (flow *stackLiveFlow) propagate() {
	for len(flow.queue) > 0 {
		work := flow.queue[len(flow.queue)-1]
		flow.queue = flow.queue[:len(flow.queue)-1]
		delta := flow.blocks[work].pending
		flow.blocks[work].pending = 0
		for _, bid := range flow.preds[flow.offsets[work]:flow.offsets[work+1]] {
			block := &flow.blocks[bid]
			fresh := delta &^ block.out
			if fresh == 0 {
				continue
			}
			flow.touch(bid)
			block.out |= fresh
			flow.addInput(bid, fresh)
		}
	}
}

func (s *stackAllocState) computeLiveWords() {
	if len(s.live.ids) == 0 {
		return
	}
	flow := newStackLiveFlow(s.f)
	for start := 0; start < len(s.live.ids); start += 64 {
		end := start + 64
		if end > len(s.live.ids) {
			end = len(s.live.ids)
		}
		s.seedLiveWord(&flow, s.live.ids[start:end])
		flow.propagate()
		for _, bid := range flow.touched {
			if out := flow.blocks[bid].out; out != 0 {
				s.live.words[bid] = append(s.live.words[bid], stackLiveWord{index: start / 64, bits: out})
			}
			flow.blocks[bid] = stackLiveBlock{}
		}
		flow.touched = flow.touched[:0]
	}
}

func (f *Func) getHome(vid ID) Location {
	if int(vid) >= len(f.RegAlloc) {
		return nil
	}
	return f.RegAlloc[vid]
}

func (f *Func) setHome(v *Value, loc Location) {
	for v.ID >= ID(len(f.RegAlloc)) {
		f.RegAlloc = append(f.RegAlloc, nil)
	}
	f.RegAlloc[v.ID] = loc
}

func (s *stackAllocState) buildInterferenceGraph() {
	f := s.f
	if n := f.NumValues(); cap(s.interfere) >= n {
		s.interfere = s.interfere[:n]
	} else {
		s.interfere = make([][]ID, n)
	}
	live := f.newSparseSet(f.NumValues())
	defer f.retSparseSet(live)
	for _, b := range f.Blocks {
		// Propagate liveness backwards to the start of the block.
		// Two values interfere if one is defined while the other is live.
		live.clear()
		for _, chunk := range s.live.blockWords(b.ID) {
			for word := chunk.bits; word != 0; {
				live.add(s.live.ids[chunk.index*64+bits.TrailingZeros64(word)])
				word &= word - 1
			}
		}
		for i := len(b.Values) - 1; i >= 0; i-- {
			v := b.Values[i]
			if s.values[v.ID].needSlot {
				live.remove(v.ID)
				for _, id := range live.contents() {
					// Note: args can have different types and still interfere
					// (with each other or with other values). See issue 23522.
					if s.values[v.ID].typ.Compare(s.values[id].typ) == types.CMPeq || hasAnyArgOp(v) || s.values[id].isArg {
						s.interfere[v.ID] = append(s.interfere[v.ID], id)
						s.interfere[id] = append(s.interfere[id], v.ID)
					}
				}
			}
			for _, a := range v.Args {
				if s.values[a.ID].needSlot {
					live.add(a.ID)
				}
			}
			if hasAnyArgOp(v) && s.values[v.ID].needSlot {
				// OpArg is an input argument which is pre-spilled.
				// We add back v.ID here because we want this value
				// to appear live even before this point. Being live
				// all the way to the start of the entry block prevents other
				// values from being allocated to the same slot and clobbering
				// the input value before we have a chance to load it.

				// TODO(register args) this is apparently not wrong for register args -- is it necessary?
				live.add(v.ID)
			}
		}
	}
	if f.pass.debug > stackDebug {
		for vid, i := range s.interfere {
			if len(i) > 0 {
				fmt.Printf("v%d interferes with", vid)
				for _, x := range i {
					fmt.Printf(" v%d", x)
				}
				fmt.Println()
			}
		}
	}
}

func hasAnyArgOp(v *Value) bool {
	return v.Op == OpArg || v.Op == OpArgIntReg || v.Op == OpArgFloatReg
}
