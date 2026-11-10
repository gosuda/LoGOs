// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPrefixNames(t *testing.T) {
	src := "package main\n\nimport (\n\"comptime\"\n\"context\"\n\"encoding/json\"\n\"fmt\"\n\"iter\"\n\"logos/arena\"\n)\n\nfunc probeParse(probeInput string) (int, error) {\nreturn len(probeInput), nil\n}\n\nfunc probeGet(probeInput string) (int, error) {\nprobeVal := probeParse(probeInput)?\nreturn probeVal, nil\n}\n\nfunc probeFirstOr(probeOpt int?, probeDef int) int {\nreturn probeOpt ?? probeDef\n}\n\ntype ProbeA struct {\nProbeX int\n}\n\nfunc (probeRecv ProbeA) ProbeM() string {\nreturn fmt.Sprint(probeRecv.ProbeX)\n}\n\ntype ProbeB struct {\nProbeY string\n}\n\nfunc (probeRecv ProbeB) ProbeM() string {\nreturn probeRecv.ProbeY\n}\n\ntype ProbeU interface {\nProbeA | ProbeB\nProbeM() string\n}\n\nfunc probeWho(probeU ProbeU) string {\nreturn probeU.ProbeM()\n}\n\nfunc probePair() (int, error) {\nreturn 3, nil\n}\n\nconst probeEight = map[int]string{1: \"a\", 2: \"b\", 3: \"c\", 4: \"d\", 5: \"e\", 6: \"f\", 7: \"g\", 8: \"h\"}\n\nconst probeNine = map[int]string{1: \"a\", 2: \"b\", 3: \"c\", 4: \"d\", 5: \"e\", 6: \"f\", 7: \"g\", 8: \"h\", 9: \"i\"}\n\ntype ProbePair struct {\nProbeX int\nProbeY int\n}\n\nfunc probeNames[T any]() []string {\nvar probeOut []string\nconst for _, probeF := range comptime.Fields[T]() {\nprobeOut = append(probeOut, probeF.Name)\n}\nreturn probeOut\n}\n\ntype ProbeJS struct {\nProbeA int64 `json:\"probeA\"`\nProbeB string `json:\"probeB,omitempty\"`\n}\n\ntype ProbeBig struct {\nProbeF1 int64 `json:\"probeF1\"`\nProbeF2 string `json:\"probeF2\"`\nProbeF3 int64 `json:\"probeF3\"`\nProbeF4 string `json:\"probeF4\"`\nProbeF5 int64 `json:\"probeF5\"`\nProbeF6 string `json:\"probeF6\"`\nProbeF7 int64 `json:\"probeF7\"`\nProbeF8 string `json:\"probeF8\"`\nProbeF9 string `json:\"probeF9\"`\n}\n\ntype ProbeReq struct {\nProbeR *int64 `json:\"probeR\"`\n}\n\ntype probeNode struct {\nprobeV int\n}\n\nfunc probeLocals() []probeNode {\nprobeRegion := arena.Open()\nreturn arena.Append[probeNode](probeRegion, nil, probeNode{3})\n}\n\nvar probeCh = make(chan int, 8)\n\nfunc probeTask(probeN int) {\nprobeCh <- probeN + 41\n}\n\nfunc probeRun(probeCtx context.Context, probeIn int) error {\ngo group (probeCtx) {\ngo probeTask(probeIn)\n}\nreturn nil\n}\n\nfunc probeCount(probeN int) iter.Seq[int] {\nfor probeIdx := range probeN {\nyield probeIdx\n}\n}\n\ntype probeCloser struct {\nprobeErr error\n}\n\nfunc (probeC *probeCloser) Close() error {\nreturn probeC.probeErr\n}\n\nfunc probeReturnNil() (err error) {\nprobeC := &probeCloser{}\ndefer probeC.Close()?\nreturn nil\n}\n\ntype ProbeVec struct {\nProbeX int\nProbeY int\n}\n\ntype ProbeCarrier interface {\nProbePos ProbeVec\n}\n\ntype ProbeBot struct : ProbeCarrier {\nProbePos ProbeVec\n}\n\nfunc main() {\nfmt.Println(probeGet(\"hi\"))\nvar probeOpt int?\nfmt.Println(probeFirstOr(probeOpt, 7))\nfmt.Println(probeWho(ProbeA{1}))\nprobeT := probePair()\nprobeA, probeB := probeT\nfmt.Println(probeA, probeB)\nfmt.Println(probeEight[3] ?? \"none\")\nfmt.Println(probeNine[9] ?? \"none\")\nfmt.Println(probeNames[ProbePair]())\nprobeJ, _ := json.Marshal(ProbeJS{1, \"x\"})\nfmt.Println(string(probeJ))\nvar probeJU ProbeJS\n_ = json.Unmarshal(probeJ, &probeJU)\nprobeBin, _ := probeJU.MarshalBinary()\nvar probeBinBack ProbeJS\n_ = probeBinBack.UnmarshalBinary(probeBin)\nvar probeBig ProbeBig\nprobeBigJ, _ := json.Marshal(probeBig)\nfmt.Println(string(probeBigJ))\nvar probeBigBack ProbeBig\n_ = json.Unmarshal(probeBigJ, &probeBigBack)\nprobeBigBin, _ := probeBig.MarshalBinary()\nvar probeBigBinBack ProbeBig\n_ = probeBigBinBack.UnmarshalBinary(probeBigBin)\nprobeRVal := int64(7)\nvar probeReq ProbeReq = ProbeReq{ProbeR: &probeRVal}\nprobeReqJ, _ := json.Marshal(probeReq)\nfmt.Println(string(probeReqJ))\nvar probeReqBack ProbeReq = ProbeReq{ProbeR: &probeRVal}\n_ = json.Unmarshal(probeReqJ, &probeReqBack)\nfmt.Println(probeLocals())\n_ = probeRun(context.Background(), 1)\nfor probeV := range probeCount(3) {\nfmt.Println(probeV)\n}\nfmt.Println(probeReturnNil())\nvar probeBot ProbeBot\nprobeBot.ProbePos = ProbeVec{1, 2}\nfmt.Println(probeBot.ProbePos)\n}\n"
	out := lowerSnippetSrc(t, "prefixprobe.lgo", src)
	prefixCheckOutput(t, out)
}

func prefixCheckOutput(t *testing.T, out string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "prefix.go", out, 0)
	if err != nil {
		t.Fatal(err)
	}
	imports := prefixImports(f)
	var bad []string
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		bad = append(bad, prefixCheckFunc(fd, imports)...)
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range gd.Specs {
			ts, ok := sp.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if prefixIsGen(ts.Name.Name) {
				bad = append(bad, prefixCheckGenFields(ts)...)
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("%d compiler-made names lack prefix: %s", len(bad), strings.Join(bad, ", "))
	}
}

func prefixImports(f *ast.File) map[string]bool {
	m := map[string]bool{}
	for _, im := range f.Imports {
		if im.Name != nil {
			if im.Name.Name == "_" || im.Name.Name == "." {
				continue
			}
			m[im.Name.Name] = true
			continue
		}
		s := strings.Trim(im.Path.Value, "\"")
		_, after, found := strings.Cut(s, "/")
		if found {
			s = after
			if i := strings.LastIndex(s, "/"); i >= 0 {
				s = s[i+1:]
			}
		}
		if s != "" {
			m[s] = true
		}
	}
	return m
}

func prefixIsGen(name string) bool {
	return strings.HasPrefix(name, "_logos_") || strings.Contains(name, "_logosct")
}

func prefixIsProp(name string) bool {
	return strings.HasPrefix(name, "Logos_Get") || strings.HasPrefix(name, "Logos_Set")
}

func prefixAllowed(name string, imports map[string]bool) bool {
	if name == "_" {
		return true
	}
	if imports[name] {
		return true
	}
	switch name {
	case "true", "false", "iota", "nil", "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any", "comparable", "append", "cap", "clear", "close", "complex", "copy", "delete", "imag", "len", "make", "max", "min", "new", "panic", "print", "println", "real", "recover", "MarshalJSON", "UnmarshalJSON", "MarshalBinary", "AppendBinary", "UnmarshalBinary", "MarshalText", "UnmarshalText":
		return true
	}
	if prefixIsGen(name) {
		return true
	}
	if prefixIsProp(name) {
		return true
	}
	return false
}

func prefixCheckFunc(fd *ast.FuncDecl, imports map[string]bool) []string {
	if strings.Contains(fd.Name.Name, "_logosct") {
		return nil
	}
	if prefixIsProp(fd.Name.Name) {
		return nil
	}
	gen := prefixIsGen(fd.Name.Name)
	allowed := false
	switch fd.Name.Name {
	case "MarshalJSON", "UnmarshalJSON", "MarshalBinary", "AppendBinary", "UnmarshalBinary", "MarshalText", "UnmarshalText":
		allowed = true
	}
	if !gen && !allowed {
		return nil
	}
	var bad []string
	if fd.Recv != nil {
		for _, fl := range fd.Recv.List {
			for _, nm := range fl.Names {
				if !prefixAllowed(nm.Name, imports) {
					bad = append(bad, nm.Name)
				}
			}
		}
	}
	if fd.Type.Params != nil {
		for _, fl := range fd.Type.Params.List {
			for _, nm := range fl.Names {
				if !prefixAllowed(nm.Name, imports) {
					bad = append(bad, nm.Name)
				}
			}
		}
	}
	if fd.Type.Results != nil {
		for _, fl := range fd.Type.Results.List {
			for _, nm := range fl.Names {
				if !prefixAllowed(nm.Name, imports) {
					bad = append(bad, nm.Name)
				}
			}
		}
	}
	if fd.Body == nil {
		return bad
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.AssignStmt:
			if v.Tok.String() == ":=" {
				for _, e := range v.Lhs {
					id, ok := e.(*ast.Ident)
					if ok && !prefixAllowed(id.Name, imports) {
						bad = append(bad, id.Name)
					}
				}
			}
		case *ast.ValueSpec:
			for _, nm := range v.Names {
				if !prefixAllowed(nm.Name, imports) {
					bad = append(bad, nm.Name)
				}
			}
		case *ast.RangeStmt:
			if id, ok := v.Key.(*ast.Ident); ok && id.Name != "_" && !prefixAllowed(id.Name, imports) {
				bad = append(bad, id.Name)
			}
			if id, ok := v.Value.(*ast.Ident); ok && id.Name != "_" && !prefixAllowed(id.Name, imports) {
				bad = append(bad, id.Name)
			}
		case *ast.LabeledStmt:
			if !prefixAllowed(v.Label.Name, imports) {
				bad = append(bad, v.Label.Name)
			}
		}
		return true
	})
	return bad
}

func prefixCheckGenFields(ts *ast.TypeSpec) []string {
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil
	}
	var bad []string
	for _, fl := range st.Fields.List {
		for _, nm := range fl.Names {
			if !prefixIsGen(nm.Name) && !prefixIsProp(nm.Name) {
				bad = append(bad, nm.Name)
			}
		}
	}
	return bad
}
