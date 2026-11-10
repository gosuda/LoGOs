// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"cmd/compile/internal/base"
	"cmd/compile/internal/types2"
)

const jsonKeySwitchMax = constMapSwitchMax

func foldJSONKey(k string) string {
	var b strings.Builder
	for i := 0; i < len(k); {
		c := k[i]
		if c < utf8.RuneSelf {
			if 'a' <= c && c <= 'z' {
				c -= 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(k[i:])
		b.WriteRune(foldJSONRune(r))
		i += n
	}
	return b.String()
}

func foldJSONRune(r rune) rune {
	for {
		r2 := unicode.SimpleFold(r)
		if r2 <= r {
			return r2
		}
		r = r2
	}
}

func seenWords(n int) int {
	return (n + 63) / 64
}

func (js *jsonStruct) unmarshalText(b *strings.Builder) {
	var body []string
	body = append(body, fmt.Sprintf("_logos_s := %s.NewScanner(_logos_data, (*%s)(nil))", jsonAlias, js.name))
	body = append(body, ifLines("!_logos_s.Object()", []string{"return _logos_s.End()"})...)
	if len(js.required) > 0 {
		body = append(body, fmt.Sprintf("var _logos_seen [%d]uint64", seenWords(len(js.required))))
	}
	body = append(body, js.defaults...)
	body = append(body, js.loopLines()...)
	body = append(body, ifLines("_logos_e := _logos_s.Err(); _logos_e != nil", []string{"return _logos_e"})...)
	body = append(body, js.requiredCheck()...)
	body = append(body, "return _logos_s.Saved()")
	fmt.Fprintf(b, "\nfunc (%s *%s) UnmarshalJSON(_logos_data []byte) error {\n", jsonRecv, js.name)
	for _, l := range indentLines(body) {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
}

func (js *jsonStruct) unmarshalKeys(b *strings.Builder) {
	js.keyFuncs(b)
	js.missingFunc(b)
}

func (js *jsonStruct) loopLines() []string {
	body := []string{
		fmt.Sprintf("_logos_i := _logos_jsonkey_%s(_logos_s.Key())", js.name),
		"if _logos_i < 0 {",
		fmt.Sprintf("\t_logos_i = _logos_jsonfold_%s(_logos_s.FoldKey())", js.name),
		"}",
		"switch _logos_i {",
	}
	for _, f := range js.fields {
		body = append(body, fmt.Sprintf("case %d:", f.pos))
		body = append(body, indentLines(js.decodeLines(f))...)
	}
	body = append(body, "default:", "\t_logos_s.Skip()", "}")
	return blockLines("for _logos_s.Next()", body)
}

func (js *jsonStruct) decodeLines(f *jsonField) []string {
	var lines []string
	if f.required >= 0 {
		lines = append(lines, ifLines("_logos_s.Null()", []string{
			fmt.Sprintf("return %s.MissingFieldError{Type: %q, Field: %q}", rtAlias, js.name, f.goName),
		})...)
		lines = append(lines, fmt.Sprintf("_logos_seen[%d] |= 1 << %d", f.required/64, f.required%64))
	}
	return append(lines, js.valueLines(f)...)
}

func (js *jsonStruct) valueLines(f *jsonField) []string {
	field := jsonRecv + "." + f.goName
	switch f.shape.kind {
	case shapeScalar:
		return []string{fmt.Sprintf("%s(&_logos_s, &%s)", f.codec().decodeFn(), field)}
	case shapePointer:
		return f.pointerLines(field)
	case shapeSlice:
		return []string{fmt.Sprintf("%s.Slice(&_logos_s, &%s, %s)", jsonAlias, field, f.elemCodec().decodeFn())}
	case shapeUnion:
		return js.unionLines(f, field)
	case shapeFallback:
		if f.quoted {
			if isExactNumber(f.typ) {
				return []string{fmt.Sprintf("_logos_s.FallbackQ(&%s)", field)}
			}
			if hasUnmarshalMethods(f.typ) {
				return []string{fmt.Sprintf("_logos_s.Fallback(&%s)", field)}
			}
			if kind, ok := fallbackScalarKind(f.typ); ok {
				return js.fallbackQuotedValueLines(f, field, kind)
			}
		}
		return []string{fmt.Sprintf("_logos_s.Fallback(&%s)", field)}
	default:
		base.Fatalf("logos: unhandled shape kind %v for %s", f.shape.kind, f.goName)
		return nil
	}
}

func (js *jsonStruct) fallbackQuotedValueLines(f *jsonField, field string, kind scalarKind) []string {
	u := types2.Unalias(f.typ)
	if cv, ok := u.(*types2.ConstView); ok {
		u = types2.Unalias(cv.Elem)
	}
	_, isPtr := u.(*types2.Pointer)
	tmp := &jsonField{
		goName:    f.goName,
		key:       f.key,
		omitEmpty: f.omitEmpty,
		omitZero:  f.omitZero,
		quoted:    true,
		pos:       f.pos,
		required:  f.required,
		typ:       f.typ,
	}
	if isPtr {
		tmp.shape = jsonShape{kind: shapePointer, scalar: kind, nonNil: f.shape.nonNil}
		return tmp.pointerLines(field)
	}
	tmp.shape = jsonShape{kind: shapeScalar, scalar: kind}
	return []string{fmt.Sprintf("%s(&_logos_s, &%s)", tmp.codec().decodeFn(), field)}
}

func (js *jsonStruct) unionLines(f *jsonField, field string) []string {
	members, unionName, ok := classifyUnionMembers(js.holderPkg, js.staticJSON, f.typ)
	if !ok || len(members) == 0 {
		base.Fatalf("logos: union field %s has no union terms", f.goName)
	}
	memberQuotes := make([]string, len(members))
	for i, c := range members {
		memberQuotes[i] = strconv.Quote(c.spell)
	}
	membersList := strings.Join(memberQuotes, ", ")
	type memberKeys struct {
		keyed   bool
		custom  bool
		spell   string
		sample  string
		ptr     bool
		keyFunc string
	}
	memberList := make([]memberKeys, len(members))
	for i, c := range members {
		sample, isPtr := strings.CutPrefix(c.spell, "*")
		memberList[i] = memberKeys{
			keyed:   c.keyed,
			custom:  !c.keyed,
			spell:   c.spell,
			sample:  sample,
			ptr:     isPtr,
			keyFunc: c.keyFunc,
		}
	}

	var trials []string
	trials = append(trials, "_logos_raw := _logos_s.Value()")
	trials = append(trials, "if _logos_value_err := _logos_s.Err(); _logos_value_err != nil {")
	trials = append(trials, "\treturn _logos_value_err")
	trials = append(trials, "}")
	trials = append(trials, "var _logos_match int")
	trials = append(trials, fmt.Sprintf("var _logos_target %s", unionName))
	hasCustom := false
	for _, s := range memberList {
		if s.custom {
			hasCustom = true
			break
		}
	}
	if hasCustom {
		trials = append(trials, "var _logos_custom int")
		trials = append(trials, fmt.Sprintf("var _logos_custom_target %s", unionName))
	}
	hasKeyed := false
	for _, s := range memberList {
		if s.keyed {
			hasKeyed = true
			break
		}
	}
	needsClaim := hasCustom && hasKeyed
	if hasKeyed {
		firstKeyed := ""
		for _, s := range memberList {
			if s.keyed {
				firstKeyed = s.sample
				break
			}
		}
		for i, s := range memberList {
			if !s.keyed {
				continue
			}
			trials = append(trials, fmt.Sprintf("var _logos_keys_%d int", i))
			trials = append(trials, fmt.Sprintf("_logos_matched_%d := false", i))
		}
		trials = append(trials, "{")
		if needsClaim {
			trials = append(trials, "var _logos_claimed bool")
		}
		trials = append(trials, fmt.Sprintf("\t_logos_scan := %s.NewScanner(_logos_raw, (*%s)(nil))", jsonAlias, firstKeyed))
		trials = append(trials, "\tif _logos_scan.Object() {")
		for i, s := range memberList {
			if s.keyed {
				trials = append(trials, fmt.Sprintf("\t\t_logos_matched_%d = true", i))
			}
		}
		trials = append(trials, "\t\tfor _logos_scan.Next() {")
		if needsClaim {
			var conds []string
			for _, s := range memberList {
				if !s.keyed {
					continue
				}
				conds = append(conds, fmt.Sprintf("%s(_logos_scan.Key()) >= 0 || %s(_logos_scan.FoldKey()) >= 0", s.keyFunc, unionFoldFunc(s.keyFunc)))
			}
			trials = append(trials, fmt.Sprintf("\t\t\tif %s {", strings.Join(conds, " || ")))
			trials = append(trials, "\t\t\t\t_logos_claimed = true")
			trials = append(trials, "\t\t\t}")
		}
		for i, s := range memberList {
			if !s.keyed {
				continue
			}
			trials = append(trials, fmt.Sprintf("\t\t\tif %s(_logos_scan.Key()) >= 0 || %s(_logos_scan.FoldKey()) >= 0 {", s.keyFunc, unionFoldFunc(s.keyFunc)))
			trials = append(trials, fmt.Sprintf("\t\t\t\t_logos_keys_%d++", i))
			trials = append(trials, "\t\t\t} else {")
			for j, sib := range memberList {
				if j == i || !sib.keyed {
					continue
				}
				trials = append(trials, fmt.Sprintf("\t\t\t\tif %s(_logos_scan.Key()) >= 0 || %s(_logos_scan.FoldKey()) >= 0 {", sib.keyFunc, unionFoldFunc(sib.keyFunc)))
				trials = append(trials, fmt.Sprintf("\t\t\t\t\t_logos_matched_%d = false", i))
				trials = append(trials, "\t\t\t\t}")
			}
			trials = append(trials, "\t\t\t}")
		}
		trials = append(trials, "\t\t\t_logos_scan.Skip()")
		trials = append(trials, "\t\t}")
		trials = append(trials, "\t}")
		trials = append(trials, "}")
	}
	for i := range members {
		scratch := fmt.Sprintf("_logos_scratch%d", i)
		s := memberList[i]
		hasKeyHelper := s.keyed

		trials = append(trials, "{")
		if s.ptr {
			trials = append(trials, fmt.Sprintf("\t%s := new(%s)", scratch, s.sample))
		} else {
			trials = append(trials, fmt.Sprintf("\tvar %s %s", scratch, s.spell))
		}
		if hasKeyHelper {
			trials = append(trials, fmt.Sprintf("\tif _logos_keys_%d == 0 {", i))
			trials = append(trials, fmt.Sprintf("\t\t_logos_matched_%d = false", i))
			trials = append(trials, "\t}")
			trials = append(trials, fmt.Sprintf("\tif _logos_matched_%d {", i))
			trials = append(trials, fmt.Sprintf("\t\tif %s.UnmarshalJSON(_logos_raw) == nil {", scratch))
			trials = append(trials, "\t\t\t_logos_match++")
			trials = append(trials, fmt.Sprintf("\t\t\t_logos_target = %s", scratch))
			trials = append(trials, "\t\t}")
			trials = append(trials, "\t}")
		} else if hasCustom && s.custom {
			if needsClaim {
				trials = append(trials, "\tif !_logos_claimed {")
				trials = append(trials, fmt.Sprintf("\t\tif %s.UnmarshalJSON(_logos_raw) == nil {", scratch))
				trials = append(trials, "\t\t\t_logos_custom++")
				trials = append(trials, fmt.Sprintf("\t\t\t_logos_custom_target = %s", scratch))
				trials = append(trials, "\t\t}")
				trials = append(trials, "\t}")
			} else {
				trials = append(trials, fmt.Sprintf("\tif %s.UnmarshalJSON(_logos_raw) == nil {", scratch))
				trials = append(trials, "\t\t_logos_custom++")
				trials = append(trials, fmt.Sprintf("\t\t_logos_custom_target = %s", scratch))
				trials = append(trials, "\t}")
			}
		}
		trials = append(trials, "}")
	}

	if hasCustom {
		trials = append(trials, "switch {")
		trials = append(trials, "case _logos_match == 1:")
		trials = append(trials, fmt.Sprintf("\t%s = _logos_target", field))
		trials = append(trials, "case _logos_match == 0 && _logos_custom == 1:")
		trials = append(trials, fmt.Sprintf("\t%s = _logos_custom_target", field))
		trials = append(trials, "case _logos_match == 0 && _logos_custom == 0:")
		trials = append(trials, fmt.Sprintf("\treturn %s.UnionTrialError{Union: %q, Members: []string{%s}, Matched: 0, Total: %d}", rtAlias, unionName, membersList, len(members)))
		trials = append(trials, "case _logos_match == 0:")
		trials = append(trials, fmt.Sprintf("\treturn %s.UnionTrialError{Union: %q, Members: []string{%s}, Matched: _logos_custom, Total: %d}", rtAlias, unionName, membersList, len(members)))
		trials = append(trials, "default:")
		trials = append(trials, fmt.Sprintf("\treturn %s.UnionTrialError{Union: %q, Members: []string{%s}, Matched: _logos_match, Total: %d}", rtAlias, unionName, membersList, len(members)))
		trials = append(trials, "}")
	} else {
		trials = append(trials, "switch _logos_match {")
		trials = append(trials, "case 1:")
		trials = append(trials, fmt.Sprintf("\t%s = _logos_target", field))
		trials = append(trials, "case 0:")
		trials = append(trials, fmt.Sprintf("\treturn %s.UnionTrialError{Union: %q, Members: []string{%s}, Matched: 0, Total: %d}", rtAlias, unionName, membersList, len(members)))
		trials = append(trials, "default:")
		trials = append(trials, fmt.Sprintf("\treturn %s.UnionTrialError{Union: %q, Members: []string{%s}, Matched: _logos_match, Total: %d}", rtAlias, unionName, membersList, len(members)))
		trials = append(trials, "}")
	}

	if f.required < 0 {
		return ifElseLines("!_logos_s.Null()", trials, []string{field + " = nil"})
	}
	out := []string{"{"}
	out = append(out, indentLines(trials)...)
	return append(out, "}")
}

func (f *jsonField) pointerLines(field string) []string {
	fill := fmt.Sprintf("%s(&_logos_s, %s.Alloc(&%s))", f.codec().decodeFn(), jsonAlias, field)
	if f.required >= 0 {
		return []string{fill}
	}
	return ifElseLines("_logos_s.Null()", []string{field + " = nil"}, []string{fill})
}

func (js *jsonStruct) requiredCheck() []string {
	n := len(js.required)
	var lines, conds []string
	for w := range seenWords(n) {
		lines = append(lines, fmt.Sprintf("const _logos_required%d = 1<<%d - 1", w, min(64, n-64*w)))
		conds = append(conds, fmt.Sprintf("_logos_seen[%[1]d]&_logos_required%[1]d != _logos_required%[1]d", w))
	}
	if n == 0 {
		return nil
	}
	ret := fmt.Sprintf("return %s.MissingFieldError{Type: %q, Field: _logos_jsonmissing_%s(_logos_seen)}", rtAlias, js.name, js.name)
	return append(lines, ifLines(strings.Join(conds, " || "), []string{ret})...)
}

func (js *jsonStruct) missingFunc(b *strings.Builder) {
	if len(js.required) == 0 {
		return
	}
	quoted := make([]string, len(js.required))
	for i, name := range js.required {
		quoted[i] = strconv.Quote(name)
	}
	fmt.Fprintf(b, "\nfunc _logos_jsonmissing_%s(_logos_seen [%d]uint64) string {\n", js.name, seenWords(len(js.required)))
	fmt.Fprintf(b, "\tfor _logos_i, _logos_name := range [...]string{%s} {\n", strings.Join(quoted, ", "))
	b.WriteString("\t\tif _logos_seen[_logos_i/64]>>(_logos_i%64)&1 == 0 {\n\t\t\treturn _logos_name\n\t\t}\n\t}\n\treturn \"\"\n}\n")
}

type keyEntry struct {
	key string
	idx int
}

func (js *jsonStruct) keyFuncs(b *strings.Builder) {
	var exact, folded []keyEntry
	seen := make(map[string]bool)
	for _, f := range js.fields {
		exact = append(exact, keyEntry{f.key, f.pos})
		fk := foldJSONKey(f.key)
		if !seen[fk] {
			seen[fk] = true
			folded = append(folded, keyEntry{fk, f.pos})
		}
	}
	writeKeyFunc(b, js.name, "jsonkey", exact)
	writeKeyFunc(b, js.name, "jsonfold", folded)
}

func unionFoldFunc(keyFunc string) string {
	return "_logos_jsonfold_" + strings.TrimPrefix(keyFunc, "_logos_jsonkey_")
}

func (js *jsonStruct) unionMemberKeys(b *strings.Builder, seen map[string]bool) {
	for _, f := range js.fields {
		if f.shape.kind != shapeUnion {
			continue
		}
		members, _, ok := classifyUnionMembers(js.holderPkg, js.staticJSON, f.typ)
		if !ok {
			continue
		}
		for _, c := range members {
			if !c.keyed || !c.foreign || seen[c.keyFunc] {
				continue
			}
			seen[c.keyFunc] = true
			var exact, folded []keyEntry
			foldSeen := make(map[string]bool)
			for i, k := range c.keys {
				exact = append(exact, keyEntry{k, i})
				fk := foldJSONKey(k)
				if !foldSeen[fk] {
					foldSeen[fk] = true
					folded = append(folded, keyEntry{fk, i})
				}
			}
			name := strings.TrimPrefix(c.keyFunc, "_logos_jsonkey_")
			writeKeyFunc(b, name, "jsonkey", exact)
			writeKeyFunc(b, name, "jsonfold", folded)
		}
	}
}

func writeKeyFunc(b *strings.Builder, name, kind string, keys []keyEntry) {
	if len(keys) <= jsonKeySwitchMax {
		fmt.Fprintf(b, "\nfunc _logos_%s_%s(_logos_k string) int {\n\tswitch _logos_k {\n", kind, name)
		for _, e := range keys {
			fmt.Fprintf(b, "\tcase %s:\n\t\treturn %d\n", strconv.Quote(e.key), e.idx)
		}
		b.WriteString("\t}\n\treturn -1\n}\n")
		return
	}
	writeKeyTable(b, name, kind, keys)
}

func writeKeyTable(b *strings.Builder, name, kind string, keys []keyEntry) {
	entries := make([]constMapKey, len(keys))
	for i, e := range keys {
		entries[i] = constMapKey{hash: constHashString(e.key), canon: e.key}
	}
	layout, err := buildConstMap(entries, defaultConstMapBudget)
	if err != nil {
		base.Fatalf("logos: no key table for %s: %v", name, err)
	}
	table := fmt.Sprintf("_logos_%ss_%s", kind, name)
	fmt.Fprintf(b, "\n%s\nvar %s = %s.ConstTable[string, int]{Salt: %d, Buckets: []%s.ConstBucket{", rodataMarker, table, rtAlias, layout.salt, rtAlias)
	for i, bk := range layout.buckets {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "{Off: %d, Size: %d, Seed: %d}", bk.off, bk.size, bk.seed)
	}
	fmt.Fprintf(b, "}, Slots: []%s.ConstSlot[string, int]{", rtAlias)
	for s, ki := range layout.slots {
		if s > 0 {
			b.WriteString(", ")
		}
		if ki < 0 {
			b.WriteString("{}")
			continue
		}
		fmt.Fprintf(b, "{Key: %s, Val: %d, Occ: true}", strconv.Quote(keys[ki].key), keys[ki].idx)
	}
	b.WriteString("}}\n")
	fmt.Fprintf(b, "func _logos_%s_%s(_logos_k string) int {\n\tif _logos_i, _logos_ok := %s.Get(%s.HashString(_logos_k), _logos_k); _logos_ok {\n\t\treturn _logos_i\n\t}\n\treturn -1\n}\n", kind, name, table, rtAlias)
}
