// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package staticinit

import (
	"strconv"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/logos"
	"cmd/compile/internal/staticdata"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
)

func logosBackingName(l *ir.Name, loff int64, t *types.Type) *ir.Name {
	if !l.Readonly() {
		return StaticName(t)
	}
	parent := l.Sym()
	sym := parent.Pkg.Lookup(parent.Name + "·logos_backing" + strconv.FormatInt(loff, 10))
	if sym.Def != nil {
		base.FatalfAt(base.Pos, "logos: backing %v already defined", sym)
	}
	n := ir.NewNameAt(base.Pos, sym, t)
	sym.Def = n
	n.Class = ir.PEXTERN
	typecheck.Target.Externs = append(typecheck.Target.Externs, n)
	n.Linksym().Align = int16(t.Alignment())
	if l.LogosCtGlobal() {
		n.SetLogosCtGlobal(true)
	}
	n.MarkReadonly()
	return n
}

func logosStaticInit(s *Schedule, n ir.Node) bool {
	target := logosRodataTarget(n)
	if target == nil || target.LogosCtGlobal() {
		return s.tryStaticInit(n)
	}
	if as, ok := n.(*ir.AssignStmt); ok {
		target.MarkReadonly()
		if sl, ok := as.Y.(*ir.SliceExpr); ok && sl.Op() == ir.OSLICE3 && s.logosStaticSlice(target, 0, sl) {
			return true
		}
	}
	nOut := len(s.Out)
	ok := s.tryStaticInit(n)
	if !ok || len(s.Out) != nOut {
		sym := target.Sym()
		if !logos.IsWrittenRodata(sym.Pkg.Path, sym.Name) {
			base.FatalfAt(n.Pos(), "logos: const composite %s needs dynamic initialization", sym.Name)
		}
		base.ErrorfAt(n.Pos(), 0, "[correctness::rodata_dynamic_init] variable %s marked //logos:rodata needs dynamic initialization", sym.Name)
	}
	return ok
}

func logosRodataTarget(n ir.Node) *ir.Name {
	var nm *ir.Name
	switch n.Op() {
	case ir.OAS:
		nm, _ = n.(*ir.AssignStmt).X.(*ir.Name)
	case ir.OCALLFUNC:
		fn, ok := n.(*ir.CallExpr).Fun.(*ir.Name)
		if ok && fn.Func != nil {
			nm = MapInitToVar[fn.Func]
		}
	}
	if nm == nil || nm.Class != ir.PEXTERN {
		return nil
	}
	if nm.LogosCtGlobal() {
		if nm.Readonly() {
			return nm
		}
		return nil
	}
	sym := nm.Sym()
	if logos.IsRodata(sym.Pkg.Path, sym.Name) {
		return nm
	}
	return nil
}

func logosStaticTemp(l *ir.Name, loff int64, t *types.Type) *ir.Name {
	if l.Readonly() && l.LogosCtGlobal() {
		return logosBackingName(l, loff, t)
	}
	a := StaticName(t)
	if l.Readonly() {
		a.MarkReadonly()
	}
	return a
}

func (s *Schedule) logosStaticSlice(l *ir.Name, loff int64, sl *ir.SliceExpr) bool {
	if base.Ctxt.IsFIPS() {
		return false
	}
	lo, ok := staticSliceBound(sl.Low, 0)
	if !ok {
		return false
	}
	hi, ok := staticSliceBound(sl.High, -1)
	if !ok || hi < 0 {
		return false
	}
	mx, ok := staticSliceBound(sl.Max, -1)
	if !ok || mx < 0 {
		return false
	}
	back, ok := sl.X.(*ir.CompLitExpr)
	if !ok || back.Op() != ir.OSLICELIT {
		return false
	}
	if lo < 0 || hi < lo || mx < hi || mx > int64(back.Len) {
		return false
	}
	ln, cp := hi-lo, mx-lo
	s.initplan(back)
	p := s.Plans[back]
	if !l.LogosCtGlobal() {
		for i := range p.E {
			if e := &p.E[i]; e.Expr.Op() != ir.OLITERAL && e.Expr.Op() != ir.ONIL {
				return false
			}
		}
	}
	ta := types.NewArray(back.Type().Elem(), back.Len)
	ta.SetNoalg(true)
	a := logosBackingName(l, loff, ta)
	for i := range p.E {
		e := &p.E[i]
		if e.Expr.Op() == ir.OLITERAL || e.Expr.Op() == ir.ONIL {
			staticdata.InitConst(a, e.Xoffset, e.Expr, int(e.Expr.Type().Size()))
			continue
		}
		if !s.StaticAssign(a, e.Xoffset, e.Expr, e.Expr.Type()) {
			return false
		}
	}
	s.Temps[back] = a
	sym := l.Linksym()
	sym.WriteAddr(base.Ctxt, loff, types.PtrSize, a.Linksym(), lo*int64(back.Type().Elem().Size()))
	sym.WriteInt(base.Ctxt, loff+types.SliceLenOffset, types.PtrSize, ln)
	sym.WriteInt(base.Ctxt, loff+types.SliceCapOffset, types.PtrSize, cp)
	return true
}

func staticSliceBound(e ir.Node, def int64) (int64, bool) {
	if e == nil {
		return def, true
	}
	if e.Op() != ir.OLITERAL {
		return 0, false
	}
	return ir.Int64Val(e), true
}
