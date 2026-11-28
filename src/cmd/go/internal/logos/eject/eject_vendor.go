// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/scanner"
	"go/token"
	"internal/logosfile"
	"internal/syslist"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/module"

	"cmd/go/internal/cfg"
	"cmd/go/internal/fsys"
	"cmd/go/internal/load"
	"cmd/go/internal/search"
)

const supportVendorDir = "internal/toolchainvendor"

func copyEjectedSupport(src, staging, dst string, roots []string) (decided map[string]map[string]bool, err error) {
	destinations, err := supportDestinations(roots)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          dst,
		pending:      roots,
		destinations: destinations,
		written:      make(map[string]string),
		notices:      make(map[string]bool),
		embedded:     make(map[string]bool),
		decided:      make(map[string]map[string]bool),
	}
	for len(c.pending) > 0 {
		importPath := c.pending[0]
		c.pending = c.pending[1:]
		p, err := ejectedSupportPackage(importPath)
		if err != nil {
			return nil, err
		}
		if err := c.copyPackage(p); err != nil {
			return nil, fmt.Errorf("support package %s: %w", importPath, err)
		}
	}
	for _, name := range c.format {
		if !c.embedded[name] {
			queueEjectGofmt(filepath.Join(dst, filepath.FromSlash(name)))
		}
	}
	return c.decided, nil
}

type supportCopy struct {
	src          *os.Root
	srcDir       string
	staging      string
	dst          string
	pending      []string
	destinations map[string]string
	written      map[string]string
	notices      map[string]bool
	embedded     map[string]bool
	format       []string
	decided      map[string]map[string]bool
}

// markDecided records one lowered stem the support copy owns: written or
// deliberately skipped. materializeGorootLogos must not emit these again:
// the second writer disagrees with the copy on ejected guards, so the
// first writer wins every stem it decided.
func (c *supportCopy) markDecided(p supportSourcePackage, stem string) {
	if p.vendored {
		return
	}
	if c.decided == nil {
		c.decided = make(map[string]map[string]bool)
	}
	m := c.decided[p.dst]
	if m == nil {
		m = make(map[string]bool)
		c.decided[p.dst] = m
	}
	m[stem] = true
}

// absSrcDir returns the host path of one support source package.
func (c *supportCopy) absSrcDir(p supportSourcePackage) string {
	return filepath.Join(c.srcDir, filepath.FromSlash(p.src))
}

// readDir lists one support source package. Toolchain packages read the
// effective directory, so an overlay-inserted LoGOs file is copied from
// its lowered output and an overlay-deleted file is never requested from
// staging. Vendored packages keep the rooted physical listing.
func (c *supportCopy) readDir(p supportSourcePackage) ([]fs.DirEntry, error) {
	if p.vendored {
		return fs.ReadDir(c.src.FS(), p.src)
	}
	return fsys.ReadDir(c.absSrcDir(p))
}

// readOriginal reads one toolchain support source through the effective
// filesystem, so overlay replacements govern ignore checks and inserted
// files read their overlaid content.
func (c *supportCopy) readOriginal(p supportSourcePackage, name string) ([]byte, error) {
	return fsys.ReadFile(filepath.Join(c.absSrcDir(p), name))
}

type supportSourcePackage struct {
	src      string
	dst      string
	vendored bool
}

func ejectedSupportPackage(importPath string) (supportSourcePackage, error) {
	if err := module.CheckImportPath(importPath); err != nil {
		return supportSourcePackage{}, err
	}
	if rel, ok := supportRelPath(importPath); ok {
		key := strings.ToLower(rel)
		if key == supportVendorDir || strings.HasPrefix(key, supportVendorDir+"/") || key == "internal/jsonscan" {
			return supportSourcePackage{}, fmt.Errorf("support package %s maps to reserved path %s", importPath, rel)
		}
		return supportSourcePackage{src: importPath, dst: rel}, nil
	}
	return supportSourcePackage{
		src:      "vendor/" + importPath,
		dst:      supportVendorDir + "/" + importPath,
		vendored: true,
	}, nil
}

func (c *supportCopy) copyPackage(p supportSourcePackage) error {
	entries, err := c.readDir(p)
	if err != nil {
		return err
	}
	// Reserve every output stem in this package up front so JSONv2
	// companions never collide with an emitted file, mirroring
	// ejectOutputStems for the staging path.
	taken := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name[0] == '.' || name[0] == '_' {
			continue
		}
		switch {
		case isSupportCopyFile(name):
			taken[strings.TrimSuffix(name, ".go")] = true
		case p.vendored && isVendoredGoFile(name):
			taken[strings.TrimSuffix(name, ".go")] = true
		case !p.vendored && isLoweredSupportFile(name):
			taken[logosStem(name)] = true
		}
	}
	// A package that selects constants by ejected mode and consumes them
	// under const-if cannot ship host lowering: the ejected tree builds
	// without the ejected tag, so refuse instead of publishing a host
	// specialization.
	if !p.vendored {
		var logosNames []string
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || name[0] == '.' || name[0] == '_' || !isLoweredSupportFile(name) {
				continue
			}
			logosNames = append(logosNames, name)
		}
		if len(logosNames) > 0 && modeDependentSupportLowering(func(name string) ([]byte, error) {
			return c.readOriginal(p, name)
		}, logosNames) {
			return fmt.Errorf("support package %s mixes ejected-mode sources with const-if lowering", p.src)
		}
	}
	goFiles := 0
	var embeds []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name[0] == '.' || name[0] == '_' {
			continue
		}
		if isSupportCopyFile(name) || p.vendored && isVendoredGoFile(name) || !p.vendored && isLoweredSupportFile(name) {
			if !entry.Type().IsRegular() {
				return fmt.Errorf("go source %s is not a regular file", name)
			}
			patterns, copied, err := c.copyPackageGo(p, name, taken)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if copied {
				goFiles++
				embeds = append(embeds, patterns...)
			}
			continue
		}
		if isPackageBuildAsset(name) {
			if err := c.copyAsset(p, name); err != nil {
				return err
			}
		}
	}
	if goFiles == 0 {
		return fmt.Errorf("holds no Go sources")
	}
	if err := c.copyPackageEmbeds(p, embeds); err != nil {
		return err
	}
	if p.vendored {
		return c.copyVendorNotices(p)
	}
	return nil
}

func isSupportLogosFile(name string) bool {
	return logosfile.Is(name) && !strings.HasSuffix(logosStem(name), "_test")
}

// isLoweredSupportFile reports whether a LoGOs source belongs in the
// support copy. The copy is the ejected form, so membership follows the
// source's effective build constraint specialized with logosejected=true,
// not the output file name. Handwritten _runtime.go exclusion applies only
// to Go sources via isSupportCopyFile; a LoGOs stem ending in _runtime is
// ordinary when its constraint selects the ejected build.
func isLoweredSupportFile(name string) bool {
	return isSupportLogosFile(name)
}

// ejectedSelected reports whether the ejected support copy selects a file
// whose original build constraint is expr: some assignment with
// logosejected set and logosconc unset satisfies it. Untagged sources and
// platform or custom guards stay selectable; !logosejected twins do not.
// satisfyVariantTags rejects unsatisfiable guards without enumerating the
// free tags, so no cutoff can admit a native-only file.
func ejectedSelected(expr constraint.Expr) bool {
	if expr == nil {
		return true
	}
	values := map[string]bool{"logosejected": true, "logosconc": false}
	return satisfyVariantTags(expr, true, values, func() bool { return true })
}

// foldEjectedGuard substitutes the ejected-mode tags into expr and folds
// boolean identities: logosejected is set, logosconc is unset because the
// support copy has no monitored file. It returns nil for a
// constantly-true residual and reports a constantly-false one via
// excluded. version is the file's recorded language version: the
// constraint package reads it from //go:build lines alone, so a
// legacy-only guard contributes "". When folding would change that
// recorded version, it keeps the original structure with version-neutral
// mode tags instead, so the emitted file selects the same files under the
// same language version (a folded go1.N tag would pin the file to that
// version's semantics, such as shared loop variables before go1.22). A
// versionless file whose residual still names a release tag keeps the
// selection with a version-neutral, never-selected disjunct, which drops
// the implied version without selecting anything new: the disjunct is
// ignore && !ignore, unsatisfiable under every tag set, because a bare
// ignore tag is satisfiable with -tags ignore and would newly select
// the file.
func foldEjectedGuard(expr constraint.Expr, version string) (guard constraint.Expr, excluded bool) {
	guard, excluded = foldEjectedModeTags(expr)
	if excluded {
		return nil, true
	}
	if constraint.GoVersion(guard) != version {
		if version == "" {
			if constraint.GoVersion(guard) != "" {
				guard = &constraint.OrExpr{X: guard, Y: neverSelected()}
			}
		} else {
			guard = neutralizeEjectedGuard(expr)
		}
	}
	return guard, false
}

// neverSelected is a constraint no tag set satisfies. A bare ignore tag
// is satisfiable with -tags ignore, so it cannot serve as the
// version-neutral disjunct; conjoining it with its own negation keeps the
// versionless shape while selecting nothing.
func neverSelected() constraint.Expr {
	return &constraint.AndExpr{
		X: &constraint.TagExpr{Tag: "ignore"},
		Y: &constraint.NotExpr{X: &constraint.TagExpr{Tag: "ignore"}},
	}
}

// logosejected and !logosconc become !ignore (always true), !logosejected
// and logosconc become ignore (always false). Neither spelling names a
// Go version, so constraint.GoVersion reports the same version as for
// the original expression while plain-Go builds select the same files
// as the folded residual.
func neutralizeEjectedGuard(expr constraint.Expr) constraint.Expr {
	modeLiteral := func(tag string, negated bool) (constraint.Expr, bool) {
		switch tag {
		case "logosejected":
			if negated {
				return &constraint.TagExpr{Tag: "ignore"}, true
			}
			return &constraint.NotExpr{X: &constraint.TagExpr{Tag: "ignore"}}, true
		case "logosconc":
			if negated {
				return &constraint.NotExpr{X: &constraint.TagExpr{Tag: "ignore"}}, true
			}
			return &constraint.TagExpr{Tag: "ignore"}, true
		}
		return nil, false
	}
	switch e := expr.(type) {
	case *constraint.TagExpr:
		if neutral, ok := modeLiteral(e.Tag, false); ok {
			return neutral
		}
		return e
	case *constraint.NotExpr:
		if tag, ok := e.X.(*constraint.TagExpr); ok {
			if neutral, ok := modeLiteral(tag.Tag, true); ok {
				return neutral
			}
		}
		inner := neutralizeEjectedGuard(e.X)
		if neg, ok := inner.(*constraint.NotExpr); ok {
			return neg.X
		}
		return &constraint.NotExpr{X: inner}
	case *constraint.AndExpr:
		return &constraint.AndExpr{X: neutralizeEjectedGuard(e.X), Y: neutralizeEjectedGuard(e.Y)}
	case *constraint.OrExpr:
		return &constraint.OrExpr{X: neutralizeEjectedGuard(e.X), Y: neutralizeEjectedGuard(e.Y)}
	default:
		return expr
	}
}

func foldEjectedModeTags(expr constraint.Expr) (guard constraint.Expr, excluded bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		switch e.Tag {
		case "logosejected":
			return nil, false
		case "logosconc":
			return nil, true
		default:
			return e, false
		}
	case *constraint.NotExpr:
		g, ex := foldEjectedModeTags(e.X)
		if ex {
			return nil, false
		}
		if g == nil {
			return nil, true
		}
		if inner, ok := g.(*constraint.NotExpr); ok {
			return inner.X, false
		}
		return &constraint.NotExpr{X: g}, false
	case *constraint.AndExpr:
		gx, exx := foldEjectedModeTags(e.X)
		if exx {
			return nil, true
		}
		gy, exy := foldEjectedModeTags(e.Y)
		if exy {
			return nil, true
		}
		if gx == nil {
			return gy, false
		}
		if gy == nil {
			return gx, false
		}
		if gx.String() == gy.String() {
			return gx, false
		}
		return &constraint.AndExpr{X: gx, Y: gy}, false
	case *constraint.OrExpr:
		gx, exx := foldEjectedModeTags(e.X)
		if !exx && gx == nil {
			return nil, false
		}
		gy, exy := foldEjectedModeTags(e.Y)
		if !exy && gy == nil {
			return nil, false
		}
		if exx {
			return gy, exy
		}
		if exy {
			return gx, exx
		}
		if gx.String() == gy.String() {
			return gx, false
		}
		return &constraint.OrExpr{X: gx, Y: gy}, false
	default:
		return expr, false
	}
}

// ejectedSupportGuard reads the original LoGOs source and reports whether
// the ejected support copy skips it and, when kept, the residual guard for
// the emitted file: nil selects unconditionally, so the emitted file drops
// its build lines. rewrite reports whether the emitted file needs its
// header rewritten: files without an original guard pass through
// untouched.
func (c *supportCopy) ejectedSupportGuard(p supportSourcePackage, name string) (skip bool, guard constraint.Expr, rewrite bool) {
	data, err := c.readOriginal(p, name)
	if err != nil {
		return false, nil, false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, data, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false, nil, false
	}
	expr, err := ownedHeaderExpr(f)
	if err != nil || expr == nil {
		return false, nil, false
	}
	if !ejectedSelected(expr) {
		return true, nil, false
	}
	version := ""
	if modern, err := ownedModernHeaderExpr(f); err == nil && modern != nil {
		version = constraint.GoVersion(modern)
	}
	guard, excluded := foldEjectedGuard(expr, version)
	if excluded {
		return true, nil, false
	}
	return false, guard, true
}

// rewriteEjectedGuard drops every build-constraint line from the header of
// the lowered data and, unless guard selects unconditionally, prepends the
// specialized residual as a single modern build line. Legacy lines never
// survive: a modern line governs the file, so dropping them preserves the
// constraint. A leading UTF-8 BOM is not part of a build line for the
// constraint package, so the first line is matched without it; the BOM
// leaves with the dropped line.
func rewriteEjectedGuard(data []byte, guard constraint.Expr) []byte {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", data, parser.PackageClauseOnly)
	if err != nil {
		return data
	}
	pkgOff := fset.Position(f.Package).Offset
	var kept []string
	hasBuild := false
	for i, ln := range strings.Split(string(data[:pkgOff]), "\n") {
		t := ln
		if i == 0 {
			t = strings.TrimPrefix(t, "\ufeff")
		}
		t = strings.TrimSpace(t)
		if constraint.IsGoBuild(t) || constraint.IsPlusBuild(t) {
			hasBuild = true
			continue
		}
		kept = append(kept, ln)
	}
	if !hasBuild && guard == nil {
		return data
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if guard != nil {
		if len(kept) > 0 {
			kept = append(kept, "")
		}
		kept = append(kept, "//go:build "+guard.String())
	}
	kept = append(kept, "")
	return []byte(strings.Join(kept, "\n") + string(data[pkgOff:]))
}

func isVendoredGoFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
}

func isPackageBuildAsset(name string) bool {
	switch path.Ext(name) {
	case ".s", ".S", ".sx", ".h", ".hh", ".hpp", ".hxx", ".c", ".cc", ".cpp", ".cxx", ".m", ".mm", ".f", ".F", ".for", ".f90", ".syso", ".swig", ".swigcxx":
		return true
	}
	return false
}

func (c *supportCopy) copyPackageGo(p supportSourcePackage, name string, taken map[string]bool) ([]string, bool, error) {
	src := path.Join(p.src, name)
	outputName := name
	var data []byte
	var err error
	lowered := !p.vendored && isSupportLogosFile(name)
	if lowered {
		outputName = logosStem(name) + ".go"
		data, err = os.ReadFile(filepath.Join(c.staging, filepath.FromSlash(p.src), outputName))
		if os.IsNotExist(err) {
			original, readErr := c.readOriginal(p, name)
			if readErr != nil {
				return nil, false, readErr
			}
			if isBuildIgnored(original) {
				c.markDecided(p, logosStem(name))
				return nil, false, nil
			}
		}
		if err != nil {
			return nil, false, fmt.Errorf("read lowered output: %w", err)
		}
	} else if p.vendored {
		data, err = c.src.ReadFile(src)
		if err != nil {
			return nil, false, err
		}
	} else {
		// Non-vendored sources read through the effective filesystem,
		// so an overlay replacement governs the copy exactly as it
		// governs the embed pass below. Untouched files read their
		// physical bytes; deleted files are never listed.
		data, err = c.readOriginal(p, name)
		if err != nil {
			return nil, false, err
		}
	}
	if !p.vendored && !lowered {
		// Handwritten Go sources only: lowered output already had its
		// header rewritten to the ejected residual, so the whole-file
		// legacy strip would also eat matching lines inside raw strings.
		data = stripEjectedBuildTag(data)
	}
	// The support copy is the ejected form: a lowered LoGOs source belongs
	// only with logosejected set, and its emitted guard is specialized to
	// the residual so plain-Go builds select the same files. The rewrite
	// runs before parsing so every later offset refers to the emitted bytes.
	if lowered {
		skip, guard, rewrite := c.ejectedSupportGuard(p, name)
		if skip {
			c.markDecided(p, logosStem(name))
			return nil, false, nil
		}
		if rewrite {
			data = rewriteEjectedGuard(data, guard)
		}
	}

	dst := path.Join(p.dst, outputName)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		if isBuildIgnored(data) {
			return nil, false, nil
		}
		return nil, false, err
	}
	constraint, err := ownedHeaderExpr(f)
	if err != nil {
		return nil, false, err
	}
	if hasIgnoreTag(constraint) {
		return nil, false, nil
	}

	edits, _ := ejectedImportEdits(data, fset, f)
	if lowered {
		edits = append(edits, siteIdentEdits(fset, f, siteNamespace(logosStem(name)))...)
	}
	// The generated support import must avoid package-level names from
	// sibling files, exactly as the main-module copy does: an alias that
	// collides with a sibling declaration makes the exported package fail
	// to build.
	var h2reserved map[string]bool
	if bytes.Contains(data, []byte(h2extendedConnectMethod)) {
		h2reserved = c.supportReservedNames(p, filepath.Join(c.staging, filepath.FromSlash(p.src)))
	}
	h2edits, h2uses := h2ExtendedConnectEdits(fset, data, f, h2reserved)
	if len(h2edits) > 0 && !lowered {
		kept, keepUses, err := c.filterH2EditsToGenuine(p, name, fset, data, f, h2edits, h2uses)
		if err != nil {
			return nil, false, err
		}
		h2edits, h2uses = kept, keepUses
	}
	if h2Overlaps(edits, h2edits) {
		return nil, false, fmt.Errorf("overlapping extended-CONNECT rewrite")
	}
	edits = append(edits, h2edits...)
	if len(h2edits) > 0 && !lowered && c.packageDeclaresExtendedConnect(p, name) {
		return nil, false, fmt.Errorf("ambiguous extended-CONNECT opt-in in %s: package declares %s and %s calls it, so eject cannot tell the fork method from the user method", p.src, h2extendedConnectMethod, name)
	}
	embeds := false
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, false, err
		}
		embeds = embeds || importPath == "embed"
		target, err := c.supportImport(importPath)
		if err != nil {
			return nil, false, err
		}
		if target == "" {
			continue
		}
		start := fset.Position(imp.Path.Pos()).Offset
		edits = append(edits, textEdit{start, start + len(imp.Path.Value), strconv.Quote(target)})
	}
	if h2uses {
		if _, err := c.supportImport("logos/net/http"); err != nil {
			return nil, false, err
		}
	}
	fixed, ok := applyTextEdits(data, edits)
	if !ok {
		return nil, false, fmt.Errorf("overlapping import edits")
	}
	mainBytes, extraBytes, extraStem := splitJSONv2File(fixed, strings.TrimSuffix(outputName, ".go"), taken)
	var patterns []string
	if embeds {
		patterns, err = packageEmbedPatterns(fset, f.Comments)
		if err != nil {
			return nil, false, err
		}
	}
	if err := c.writeFile(src, dst, mainBytes); err != nil {
		return nil, false, err
	}
	if lowered {
		c.markDecided(p, strings.TrimSuffix(outputName, ".go"))
	}
	if !p.vendored {
		c.format = append(c.format, dst)
	}
	if extraBytes != nil {
		extraDst := path.Join(p.dst, extraStem+".go")
		if err := c.writeFile(src, extraDst, extraBytes); err != nil {
			return nil, false, err
		}
		if !p.vendored {
			c.format = append(c.format, extraDst)
		}
	}
	return patterns, true, nil
}

// supportReservedNames collects the reserved names for the h2 rewrite of
// one support package: every Go source the copy reads, plus the lowered
// staging files. The sources inventory through the effective filesystem,
// so an overlay-inserted sibling counts exactly as the copy sees it.
func (c *supportCopy) supportReservedNames(p supportSourcePackage, stagingDir string) map[string]bool {
	used := map[string]bool{}
	if entries, err := c.readDir(p); err == nil {
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") {
				continue
			}
			data, err := c.readOriginal(p, name)
			if err != nil {
				continue
			}
			h2ReserveGoNames(used, name, data)
		}
	}
	for name := range h2PackageReserved(stagingDir) {
		used[name] = true
	}
	return used
}

// h2SupportReceiver records what the support copy can prove about one
// handwritten extended-CONNECT receiver from visible declarations alone.
// Lowered files carry the compiler's own verification, so only
// handwritten sources need this check: the text rewrite cannot resolve
// selector identity.
type h2SupportReceiver int

const (
	h2ReceiverUnknown h2SupportReceiver = iota
	h2ReceiverHTTP
	h2ReceiverUser
)

// h2CallSite is one handwritten extended-CONNECT candidate: a zero-arg
// call or a marked method-value capture of the fork method name, with
// the source span the rewrite would replace.
type h2CallSite struct {
	recv       ast.Expr
	start, end int
	text       string
}

// h2Binding is the net/http import binding of one file: the local name
// bound to net/http, or a dot import. Type expressions resolve against
// the binding of the file that declares them, not the file holding the
// call site, so a server declared under one alias still proves when the
// call site imports net/http under another.
type h2Binding struct {
	local string
	dot   bool
}

// h2Scope maps visible value names to their declared type expressions. A
// nil expression means the name is declared but its type cannot be read
// from the declaration.
type h2Scope struct {
	parent *h2Scope
	types  map[string]ast.Expr
	bind   h2Binding
}

func (s *h2Scope) lookup(name string) (ast.Expr, h2Binding, bool) {
	for sc := s; sc != nil; sc = sc.parent {
		if typ, ok := sc.types[name]; ok {
			return typ, sc.bind, true
		}
	}
	return nil, h2Binding{}, false
}

// h2ScopeAddValue records one value declaration: an explicit type wins,
// otherwise a &T{} or T{} shape lends its type, and anything else marks
// the name declared but unprovable.
func h2ScopeAddValue(scope *h2Scope, names []*ast.Ident, typ ast.Expr, values []ast.Expr) {
	for _, name := range names {
		if name.Name == "_" {
			continue
		}
		if typ != nil {
			scope.types[name.Name] = typ
			continue
		}
		if len(names) == 1 && len(values) == 1 {
			if inferred, ok := h2InferredType(values[0]); ok {
				scope.types[name.Name] = inferred
				continue
			}
		}
		scope.types[name.Name] = nil
	}
}

// h2InferredType reads the type of &T{} and T{} composite shapes. Any
// other right-hand side, including new(T) under a shadowed new, proves
// nothing.
func h2InferredType(rhs ast.Expr) (ast.Expr, bool) {
	switch r := rhs.(type) {
	case *ast.CompositeLit:
		return r.Type, true
	case *ast.UnaryExpr:
		if r.Op == token.AND {
			if lit, ok := r.X.(*ast.CompositeLit); ok {
				return lit.Type, true
			}
		}
	}
	return nil, false
}

// h2IsHTTPServerType reports whether typ names (*net/http.Server) through
// the file's own binding of the net/http import.
func h2IsHTTPServerType(typ ast.Expr, httpLocal string, dotHTTP bool) bool {
	switch t := typ.(type) {
	case *ast.StarExpr:
		return h2IsHTTPServerType(t.X, httpLocal, dotHTTP)
	case *ast.ParenExpr:
		return h2IsHTTPServerType(t.X, httpLocal, dotHTTP)
	case *ast.SelectorExpr:
		id, ok := t.X.(*ast.Ident)
		return ok && id.Name == httpLocal && t.Sel.Name == "Server"
	case *ast.Ident:
		return dotHTTP && t.Name == "Server"
	}
	return false
}

// h2AddGenDecl records the value declarations of one declaration.
func h2AddGenDecl(scope *h2Scope, decl ast.Decl) {
	gd, ok := decl.(*ast.GenDecl)
	if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
		return
	}
	for _, spec := range gd.Specs {
		vs, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		h2ScopeAddValue(scope, vs.Names, vs.Type, vs.Values)
	}
}

// h2HTTPBinding returns the local name bound to net/http in one file,
// reporting a dot import separately.
func h2HTTPBinding(f *ast.File) (local string, dot bool) {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "net/http" {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "." {
				return "", true
			}
			if imp.Name.Name != "_" && imp.Name.Name != "" {
				return imp.Name.Name, false
			}
			continue
		}
		return "http", false
	}
	return "", false
}

// h2FileDecls is one file's package-level picture: the value declarations
// sibling files contribute, with the import binding and type aliases of
// the file that declares them. A type expression resolves where it was
// written, so a server declared under one net/http alias still proves
// when the call site imports net/http under another.
type h2FileDecls struct {
	name    string
	file    *ast.File
	values  *h2Scope
	aliases map[string]ast.Expr
	bind    h2Binding
}

// h2ExportTagSelected mirrors go/build's matchTag under the export's
// build context, so a sibling condition evaluates exactly as the go tool
// selects files for the exported package. A tag the host build does not
// set stays false: the export builds with the host tag set, and a file
// gated on another tag compiles in no export build.
func h2ExportTagSelected(tag string) bool {
	ctxt := cfg.BuildContext
	if ctxt.CgoEnabled && tag == "cgo" {
		return true
	}
	if tag == ctxt.GOOS || tag == ctxt.GOARCH || tag == ctxt.Compiler {
		return true
	}
	if ctxt.GOOS == "android" && tag == "linux" {
		return true
	}
	if ctxt.GOOS == "illumos" && tag == "solaris" {
		return true
	}
	if ctxt.GOOS == "ios" && tag == "darwin" {
		return true
	}
	if tag == "unix" && syslist.UnixOS[ctxt.GOOS] {
		return true
	}
	if tag == "boringcrypto" {
		tag = "goexperiment.boringcrypto"
	}
	return slices.Contains(ctxt.BuildTags, tag) || slices.Contains(ctxt.ToolTags, tag) ||
		slices.Contains(ctxt.ReleaseTags, tag)
}

// h2FileSelectedByExport reports whether the export build selects a
// sibling source: the same file filter the export applies to the
// package, evaluated on the bytes the copy emits. The name check drops
// test and runtime sources the copy never emits; the build condition
// combines the header constraint, filename GOOS/GOARCH suffixes, and cgo
// through ownedFileCondition, exactly as owned declarations do. An
// unreadable or unparsable file counts as selected, so a declaration the
// copy reads is never dropped on a read error.
func h2FileSelectedByExport(name string, data []byte) bool {
	if !isSupportCopyFile(name) {
		return false
	}
	f, err := parser.ParseFile(token.NewFileSet(), name, data, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return true
	}
	cond, err := ownedFileCondition(name, f)
	if err != nil {
		return true
	}
	if cond == nil {
		return true
	}
	return cond.Eval(h2ExportTagSelected)
}

// siblingFileDecls returns the package-level declarations of the sibling
// Go files of one support package in filename order, read through the
// effective filesystem so overlay replacements govern exactly as they
// govern the copy. Test files are out: the copy never emits them. Files
// the export build does not select are out too: a declaration in a
// platform-muted sibling must not shadow a call the export keeps.
func (c *supportCopy) siblingFileDecls(p supportSourcePackage, name string) []*h2FileDecls {
	var out []*h2FileDecls
	entries, err := c.readDir(p)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		other := entry.Name()
		if entry.IsDir() || other == name || !strings.HasSuffix(other, ".go") || strings.HasSuffix(other, "_test.go") {
			continue
		}
		data, err := c.readOriginal(p, other)
		if err != nil {
			continue
		}
		// Judge the bytes the copy emits: ejected build lines strip out
		// exactly as copyPackageGo strips them, so an ejected-only
		// sibling still counts while a file no export build compiles
		// contributes no shadowing declaration.
		emitted := data
		if !p.vendored {
			emitted = stripEjectedBuildTag(data)
		}
		if !h2FileSelectedByExport(other, emitted) {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), other, data, 0)
		if err != nil {
			continue
		}
		httpLocal, dotHTTP := h2HTTPBinding(f)
		decls := &h2FileDecls{
			name:    other,
			file:    f,
			values:  &h2Scope{types: map[string]ast.Expr{}, bind: h2Binding{local: httpLocal, dot: dotHTTP}},
			aliases: map[string]ast.Expr{},
			bind:    h2Binding{local: httpLocal, dot: dotHTTP},
		}
		for _, decl := range f.Decls {
			h2AddGenDecl(decls.values, decl)
			h2AddAlias(decls.aliases, decl)
		}
		out = append(out, decls)
	}
	slices.SortFunc(out, func(a, b *h2FileDecls) int { return strings.Compare(a.name, b.name) })
	return out
}

// h2AddAlias records one type alias declaration.
func h2AddAlias(aliases map[string]ast.Expr, decl ast.Decl) {
	gd, ok := decl.(*ast.GenDecl)
	if !ok || gd.Tok != token.TYPE {
		return
	}
	for _, spec := range gd.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok || ts.Assign == token.NoPos {
			continue
		}
		aliases[ts.Name.Name] = ts.Type
	}
}

// h2TypeOrigin is one file's contribution to type resolution: its type
// aliases with the import binding of the file that declares them.
type h2TypeOrigin struct {
	file    *ast.File
	aliases map[string]ast.Expr
	bind    h2Binding
}

// h2EmbedsHTTPServer reports whether the named type declares an embedded
// *net/http.Server field, so a value of that type carries the fork
// method exactly as the type-checked exemption allows. The check runs
// in the file that declares the type, so an embedding written under one
// net/http alias still proves when the call site uses another. Alias
// hops cross into the file that declares each target.
func h2EmbedsHTTPServer(origins []*h2TypeOrigin, name string, seen map[string]bool) bool {
	if seen[name] {
		return false
	}
	seen[name] = true
	for _, o := range origins {
		ts := h2TypeDecl(o.file, name)
		if ts == nil {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return false
		}
		for _, field := range st.Fields.List {
			if len(field.Names) != 0 {
				continue
			}
			typ := field.Type
			if star, ok := typ.(*ast.StarExpr); ok {
				typ = star.X
			}
			if h2IsHTTPServerType(typ, o.bind.local, o.bind.dot) {
				return true
			}
		}
		return false
	}
	for _, o := range origins {
		if target, ok := o.aliases[name]; ok {
			if id, ok := target.(*ast.Ident); ok {
				return h2EmbedsHTTPServer(origins, id.Name, seen)
			}
			return false
		}
	}
	return false
}

// h2DeclaresMethod reports whether any file of the package declares a
// method with the given name on the named type. The origins hold only
// files the export build selects, so an explicit method shadows a
// promoted one exactly when the export compiles it: a call on such a
// type is a user method even when the type embeds a server. A generic
// receiver spells the type with arguments, so the base name decides.
func h2DeclaresMethod(origins []*h2TypeOrigin, typeName, method string) bool {
	for _, o := range origins {
		for _, decl := range o.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != method || fn.Recv == nil {
				continue
			}
			for _, field := range fn.Recv.List {
				recv := field.Type
				if star, ok := recv.(*ast.StarExpr); ok {
					recv = star.X
				}
				if name, ok := h2NamedTypeBase(recv); ok && name == typeName {
					return true
				}
			}
		}
	}
	return false
}

// h2NamedTypeBase reports the declared type name of a receiver or value
// type: a bare identifier or a generic instantiation of one. Pointer,
// parenthesized, and all other shapes stay unproven exactly as before:
// only instantiations newly resolve, so Holder[string] follows the
// declared Holder without changing any existing verdict.
func h2NamedTypeBase(typ ast.Expr) (string, bool) {
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name, true
	}
	var base ast.Expr
	switch t := typ.(type) {
	case *ast.IndexExpr:
		base = t.X
	case *ast.IndexListExpr:
		base = t.X
	default:
		return "", false
	}
	if id, ok := base.(*ast.Ident); ok {
		return id.Name, true
	}
	return "", false
}

// h2TypeDecl returns the type declaration of name in one file, or nil
// when the file does not declare it.
func h2TypeDecl(f *ast.File, name string) *ast.TypeSpec {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				continue
			}
			return ts
		}
	}
	return nil
}

// h2ScopeAt pairs one lexical scope with the byte span it governs, so a
// call site resolves against the declarations visible at its position. A
// short declaration in a nested block never retypes the same name for
// the whole function.
type h2ScopeAt struct {
	start, end int
	scope      *h2Scope
}

// h2ScopeBuilder walks one function body maintaining the lexical scope
// stack. Statements with header declarations govern them; blocks and
// case and communication clauses govern their bodies. Function literals
// are never entered: declarations inside one never reach the enclosing
// scopes, and the shadows pass judges call sites inside one separately.
type h2ScopeBuilder struct {
	stack []*h2Scope
	segs  []h2ScopeAt
	span  func(ast.Node) (int, int, bool)
	bind  h2Binding
	fset  *token.FileSet
}

// h2AliasOrigin returns the first origin aliasing name, skipping except.
// Alias targets resolve where they were written, so a name the declaring
// file does not alias may still name an alias in another file of the
// same package.
func h2AliasOrigin(origins []*h2TypeOrigin, except *h2TypeOrigin, name string) *h2TypeOrigin {
	for _, o := range origins {
		if o == except {
			continue
		}
		if _, ok := o.aliases[name]; ok {
			return o
		}
	}
	return nil
}

func (b *h2ScopeBuilder) Visit(n ast.Node) ast.Visitor {
	if n == nil {
		b.stack = b.stack[:len(b.stack)-1]
		return nil
	}
	if _, ok := n.(*ast.FuncLit); ok {
		return nil
	}
	top := b.stack[len(b.stack)-1]
	if h2OpensScope(n) {
		child := &h2Scope{parent: top, types: map[string]ast.Expr{}, bind: b.bind}
		if start, end, ok := b.h2ScopeSpan(n); ok {
			b.segs = append(b.segs, h2ScopeAt{start: start, end: end, scope: child})
		}
		b.stack = append(b.stack, child)
	} else {
		b.stack = append(b.stack, top)
	}
	b.record(n)
	return b
}

// h2OpensScope reports whether n governs declarations in a scope of its
// own: any block, a statement with header declarations, or a case or
// communication clause body.
func h2OpensScope(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.BlockStmt, *ast.RangeStmt, *ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause:
		return true
	case *ast.IfStmt:
		return n.Init != nil
	case *ast.ForStmt:
		return n.Init != nil
	}
	return false
}

// h2ScopeSpan reports the source range a scope governs. A case or
// communication clause governs only its body: the clause expressions
// and the communication itself evaluate in the enclosing scope, so a
// call there must not see the body's declarations.
func (b *h2ScopeBuilder) h2ScopeSpan(n ast.Node) (int, int, bool) {
	switch c := n.(type) {
	case *ast.CaseClause:
		return h2ClauseBodySpan(b.fset, c.Body, c.End())
	case *ast.CommClause:
		return h2ClauseBodySpan(b.fset, c.Body, c.End())
	}
	return b.span(n)
}

func h2ClauseBodySpan(fset *token.FileSet, body []ast.Stmt, end token.Pos) (int, int, bool) {
	if len(body) == 0 {
		return 0, 0, false
	}
	return fset.Position(body[0].Pos()).Offset, fset.Position(end).Offset, true
}

// record adds the declarations of one node to the innermost scope.
// Declarations in one scope cannot change type after their position in
// code that compiles, so recording in visit order is exact, and every
// call site in that scope resolves to the same map.
func (b *h2ScopeBuilder) record(n ast.Node) {
	scope := b.stack[len(b.stack)-1]
	switch s := n.(type) {
	case *ast.DeclStmt:
		h2AddGenDecl(scope, s.Decl)
	case *ast.AssignStmt:
		if s.Tok == token.DEFINE {
			if len(s.Lhs) == 1 && len(s.Rhs) == 1 {
				if id, ok := s.Lhs[0].(*ast.Ident); ok {
					h2ScopeAddValue(scope, []*ast.Ident{id}, nil, s.Rhs)
				}
			} else {
				for _, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
						scope.types[id.Name] = nil
					}
				}
			}
		}
	case *ast.RangeStmt:
		for _, e := range []ast.Expr{s.Key, s.Value} {
			if id, ok := e.(*ast.Ident); ok && id.Name != "_" {
				scope.types[id.Name] = nil
			}
		}
	}
}

// filterH2EditsToGenuine keeps only h2 edits whose selector resolves to
// the net/http method: a receiver the declarations visible at the call
// site prove is (*net/http.Server), directly or through an embedded field
// declared in the file that declares the type. Scopes honor block
// boundaries, so a short declaration in a nested block never retypes the
// same name outside it; sibling declarations resolve against the import
// binding of the file that declares them. A call on a proven user type
// stays untouched, and a receiver no declaration proves is refused
// instead of rewritten, so eject cannot steal a user method by spelling.
func (c *supportCopy) filterH2EditsToGenuine(p supportSourcePackage, name string, fset *token.FileSet, data []byte, f *ast.File, h2edits []textEdit, h2uses bool) ([]textEdit, bool, error) {
	httpLocal, dotHTTP := h2HTTPBinding(f)
	fileBind := h2Binding{local: httpLocal, dot: dotHTTP}
	sibs := c.siblingFileDecls(p, name)
	pkgScope := &h2Scope{types: map[string]ast.Expr{}, bind: fileBind}
	aliases := map[string]ast.Expr{}
	for _, imp := range f.Imports {
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." && imp.Name.Name != "" {
			pkgScope.types[imp.Name.Name] = nil
		}
	}
	for _, decl := range f.Decls {
		h2AddGenDecl(pkgScope, decl)
		h2AddAlias(aliases, decl)
	}
	origins := []*h2TypeOrigin{{file: f, aliases: aliases, bind: fileBind}}
	for _, sib := range sibs {
		origins = append(origins, &h2TypeOrigin{file: sib.file, aliases: sib.aliases, bind: sib.bind})
	}
	span := func(n ast.Node) (int, int, bool) {
		start := fset.Position(n.Pos()).Offset
		end := fset.Position(n.End()).Offset
		if start < 0 || end < start || end > len(data) {
			return 0, 0, false
		}
		return start, end, true
	}
	var segs []h2ScopeAt
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		scope := &h2Scope{parent: pkgScope, types: map[string]ast.Expr{}, bind: fileBind}
		// The receiver is a declaration of its function: a call on the
		// receiver name resolves to the receiver type, so a method on a
		// user type never refuses as ambiguous and never rewrites
		// through a same-named package-level server.
		if fn.Recv != nil {
			for _, field := range fn.Recv.List {
				h2ScopeAddValue(scope, field.Names, field.Type, nil)
			}
		}
		if fn.Type != nil {
			if fn.Type.Params != nil {
				for _, field := range fn.Type.Params.List {
					h2ScopeAddValue(scope, field.Names, field.Type, nil)
				}
			}
			if fn.Type.Results != nil {
				for _, field := range fn.Type.Results.List {
					h2ScopeAddValue(scope, field.Names, field.Type, nil)
				}
			}
		}
		b := &h2ScopeBuilder{stack: []*h2Scope{scope}, span: span, bind: fileBind, fset: fset}
		if start, end, ok := span(fn.Body); ok {
			b.segs = append(b.segs, h2ScopeAt{start: start, end: end, scope: scope})
		}
		for _, stmt := range fn.Body.List {
			ast.Walk(b, stmt)
		}
		segs = append(segs, b.segs...)
	}
	type litShadow struct {
		start, end int
		bound      map[string]bool
	}
	var shadows []litShadow
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		bound := map[string]bool{}
		if lit.Type != nil {
			if lit.Type.Params != nil {
				for _, field := range lit.Type.Params.List {
					for _, id := range field.Names {
						bound[id.Name] = true
					}
				}
			}
			if lit.Type.Results != nil {
				for _, field := range lit.Type.Results.List {
					for _, id := range field.Names {
						bound[id.Name] = true
					}
				}
			}
		}
		ast.Inspect(lit.Body, func(m ast.Node) bool {
			switch s := m.(type) {
			case *ast.AssignStmt:
				if s.Tok == token.DEFINE {
					for _, lhs := range s.Lhs {
						if id, ok := lhs.(*ast.Ident); ok {
							bound[id.Name] = true
						}
					}
				}
			case *ast.RangeStmt:
				for _, e := range []ast.Expr{s.Key, s.Value} {
					if id, ok := e.(*ast.Ident); ok {
						bound[id.Name] = true
					}
				}
			case *ast.ValueSpec:
				for _, id := range s.Names {
					bound[id.Name] = true
				}
			}
			return true
		})
		shadows = append(shadows, litShadow{
			start: fset.Position(lit.Body.Pos()).Offset,
			end:   fset.Position(lit.Body.End()).Offset,
			bound: bound,
		})
		return true
	})
	var sites []h2CallSite
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != h2extendedConnectMethod || len(x.Args) != 0 || h2HasSiteIdent(sel.X) {
				return true
			}
			start, end, ok := span(x)
			if !ok {
				return true
			}
			sites = append(sites, h2CallSite{recv: sel.X, start: start, end: end, text: string(data[start:end])})
		case *ast.AssignStmt:
			if len(x.Lhs) != 1 || len(x.Rhs) != 1 {
				return true
			}
			sel, ok := x.Rhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != h2extendedConnectMethod || h2HasSiteIdent(sel.X) {
				return true
			}
			lhs, ok := x.Lhs[0].(*ast.Ident)
			if !ok || !h2IsLoweredCalleeTemp(lhs.Name) {
				return true
			}
			start, end, ok := span(x)
			if !ok {
				return true
			}
			sites = append(sites, h2CallSite{recv: sel.X, start: start, end: end, text: string(data[start:end])})
		}
		return true
	})
	// Segments are recorded parents before children, so on identical
	// spans the later segment is the deeper scope: a case clause that
	// holds only a block shares the block's span, and the call inside
	// resolves against the block's declarations, not the clause's.
	enclosing := func(offset int) *h2Scope {
		best := pkgScope
		bestSize := -1
		for _, sg := range segs {
			if sg.start <= offset && offset <= sg.end {
				if size := sg.end - sg.start; bestSize < 0 || size <= bestSize {
					best, bestSize = sg.scope, size
				}
			}
		}
		return best
	}
	// resolveName finds the declaration of name visible at offset: the
	// innermost block scope first, then the current file's package scope,
	// then each sibling file's package scope in filename order. It returns
	// the declared type, the binding of the file that declares it, and the
	// origin that alias resolution starts from.
	resolveName := func(offset int, name string) (ast.Expr, h2Binding, *h2TypeOrigin, bool) {
		if typ, bind, ok := enclosing(offset).lookup(name); ok {
			return typ, bind, origins[0], true
		}
		for i, sib := range sibs {
			if typ, ok := sib.values.types[name]; ok {
				return typ, sib.bind, origins[i+1], true
			}
		}
		return nil, h2Binding{}, nil, false
	}
	resolve := func(typ ast.Expr, origin *h2TypeOrigin) (ast.Expr, h2Binding) {
		// Cycles are tracked on aliases actually followed, keyed by
		// the declaring file: switching to a sibling origin to find
		// the alias must not count as a visit, or the target is never
		// read and a cross-file alias hides a genuine server call.
		type followedAlias struct {
			file *ast.File
			name string
		}
		seen := map[followedAlias]bool{}
		for {
			id, ok := typ.(*ast.Ident)
			if !ok {
				return typ, origin.bind
			}
			target, ok := origin.aliases[id.Name]
			if !ok {
				next := h2AliasOrigin(origins, origin, id.Name)
				if next == nil {
					return typ, origin.bind
				}
				origin = next
				continue
			}
			ref := followedAlias{file: origin.file, name: id.Name}
			if seen[ref] {
				return typ, origin.bind
			}
			seen[ref] = true
			typ = target
		}
	}
	classify := func(site h2CallSite) h2SupportReceiver {
		recv := site.recv
		for {
			par, ok := recv.(*ast.ParenExpr)
			if !ok {
				break
			}
			recv = par.X
		}
		id, ok := recv.(*ast.Ident)
		if !ok {
			return h2ReceiverUnknown
		}
		for _, sh := range shadows {
			if sh.start <= site.start && site.end <= sh.end && sh.bound[id.Name] {
				return h2ReceiverUnknown
			}
		}
		typ, bind, origin, declared := resolveName(site.start, id.Name)
		if !declared || typ == nil {
			return h2ReceiverUnknown
		}
		typ, bind = resolve(typ, origin)
		if h2IsHTTPServerType(typ, bind.local, bind.dot) {
			return h2ReceiverHTTP
		}
		if name, ok := h2NamedTypeBase(typ); ok && h2EmbedsHTTPServer(origins, name, map[string]bool{}) {
			if h2DeclaresMethod(origins, name, h2extendedConnectMethod) {
				return h2ReceiverUser
			}
			return h2ReceiverHTTP
		}
		return h2ReceiverUser
	}
	var user []h2CallSite
	for _, site := range sites {
		switch classify(site) {
		case h2ReceiverUnknown:
			return nil, false, fmt.Errorf("ambiguous extended-CONNECT opt-in in %s: cannot prove the receiver of %s is (*net/http.Server), so eject cannot tell the fork method from the user method", p.src, site.text)
		case h2ReceiverUser:
			user = append(user, site)
		}
	}
	if len(user) == 0 {
		return h2edits, h2uses, nil
	}
	var kept []textEdit
	nonEmpty := false
	for _, edit := range h2edits {
		drop := false
		for _, u := range user {
			if edit.start <= u.start && edit.end >= u.end {
				drop = true
				break
			}
		}
		if drop {
			continue
		}
		kept = append(kept, edit)
		if edit.start != edit.end {
			nonEmpty = true
		}
	}
	if !nonEmpty {
		return nil, false, nil
	}
	return kept, h2uses, nil
}

// modeDependentSupportLowering reports whether a toolchain support package
// mixes ejected-mode source selection with const-if lowering: one of its
// LoGOs sources carries a logosejected build constraint while another
// holds a const-if or const-switch. The support copy ships host lowering
// without the ejected tag, so such a package cannot lower the same way in
// both modes.
// hasConstBranch reports whether data holds a const-if or const-switch
// branch. It reads tokens, never bytes: comments and strings never
// count, while any whitespace between the keywords still counts, exactly
// matching the compiler's keyword lookahead. Only the header parses as
// Go, so a byte scan would mistake incidental text for a branch and miss
// non-space separations. Adjacent const and if or switch tokens can only
// be the branch construct: const must open a declaration, and only the
// branch continues it with if or switch.
func hasConstBranch(data []byte) bool {
	var sawConst bool
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(data))
	var scan scanner.Scanner
	scan.Init(file, data, func(pos token.Position, msg string) {}, 0)
	for {
		_, tok, _ := scan.Scan()
		if tok == token.EOF {
			return false
		}
		if sawConst {
			if tok == token.IF || tok == token.SWITCH {
				return true
			}
			sawConst = false
		}
		if tok == token.CONST {
			sawConst = true
		}
	}
}

// modeDependentSupportLowering reports whether a toolchain support package
// mixes ejected-mode source selection with const-if lowering: one of its
// LoGOs sources carries a logosejected build constraint while another
// holds a const-if or const-switch. The support copy ships host lowering
// without the ejected tag, so such a package cannot lower the same way in
// both modes.
func modeDependentSupportLowering(read func(name string) ([]byte, error), names []string) bool {
	var twin, branch bool
	for _, name := range names {
		if !isSupportLogosFile(name) {
			continue
		}
		data, err := read(name)
		if err != nil {
			continue
		}
		if selectsEjectedMode(data) {
			twin = true
		}
		if hasConstBranch(data) {
			branch = true
		}
		if twin && branch {
			return true
		}
	}
	return false
}

// selectsEjectedMode reports whether a LoGOs source mentions the ejected
// build tag in its header constraint. Only the header parses: the body
// may hold LoGOs syntax the Go parser rejects.
func selectsEjectedMode(data []byte) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", data, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false
	}
	expr, err := ownedHeaderExpr(f)
	if err != nil || expr == nil {
		return false
	}
	var tags []string
	collectConstraintTags(expr, &tags)
	for _, tag := range tags {
		if tag == "logosejected" {
			return true
		}
	}
	return false
}

// h2DeclaresExtendedConnectFunc reports whether f declares the extended
// CONNECT method name as a package-level function or method. Parameters
// and results are scoped to their function and cannot be selected from
// another file, so only a declaration counts for the cross-file check.
func h2DeclaresExtendedConnectFunc(f *ast.File) bool {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == h2extendedConnectMethod {
			return true
		}
	}
	return false
}

// packageDeclaresExtendedConnect reports whether any handwritten Go file
// the export build selects, other than name, declares the extended
// CONNECT method name. Lowered files carry the compiler's own ambiguity
// verification, so only handwritten sources need this package-wide check:
// the text rewrite cannot tell a genuine server opt-in from a same-named
// user method in a sibling file. A file no export build compiles cannot
// make the export ambiguous.
func (c *supportCopy) packageDeclaresExtendedConnect(p supportSourcePackage, name string) bool {
	entries, err := c.readDir(p)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		other := entry.Name()
		if entry.IsDir() || other == name || !strings.HasSuffix(other, ".go") || strings.HasSuffix(other, "_test.go") {
			continue
		}
		data, err := c.readOriginal(p, other)
		if err != nil {
			continue
		}
		emitted := data
		if !p.vendored {
			emitted = stripEjectedBuildTag(data)
		}
		if !h2FileSelectedByExport(other, emitted) {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, other, data, 0)
		if err != nil {
			continue
		}
		if h2DeclaresExtendedConnectFunc(f) {
			return true
		}
	}
	return false
}

// copyAsset copies one non-Go build asset. Vendored assets keep the rooted
// physical read. Toolchain assets read through the effective filesystem,
// so an overlay-inserted header is copied from its overlaid content
// instead of aborting on the missing physical file.
func (c *supportCopy) copyAsset(p supportSourcePackage, name string) error {
	src, dst := path.Join(p.src, name), path.Join(p.dst, name)
	if p.vendored {
		return c.copyRaw(src, dst)
	}
	data, err := c.readOriginal(p, name)
	if err != nil {
		return err
	}
	return c.writeFile(src, dst, data)
}

func (c *supportCopy) supportImport(importPath string) (string, error) {
	_, logos := supportRelPath(importPath)
	if !logos && search.IsStandardImportPath(importPath) {
		return "", nil
	}
	p, err := ejectedSupportPackage(importPath)
	if err != nil {
		return "", err
	}
	key := p.dst
	if p.vendored {
		key = strings.ToLower(key)
	}
	switch prev := c.destinations[key]; prev {
	case "":
		c.destinations[key] = importPath
		c.pending = append(c.pending, importPath)
	case importPath:
	default:
		return "", fmt.Errorf("support packages %s and %s map to the same path %s", prev, importPath, p.dst)
	}
	if p.vendored {
		return rtModulePath + "/" + p.dst, nil
	}
	return "", nil
}

func (c *supportCopy) copyPackageEmbeds(p supportSourcePackage, patterns []string) error {
	if len(patterns) == 0 {
		return nil
	}
	files, err := load.ResolveEmbed(filepath.Join(c.src.Name(), filepath.FromSlash(p.src)), patterns)
	if err != nil {
		return err
	}
	for _, name := range files {
		// Assets ride the same effective read as the source copy so an
		// overlay replacement lands identically in both writes instead
		// of tripping the rewrite-versus-verbatim collision check.
		if err := c.copyAsset(p, name); err != nil {
			return err
		}
		c.embedded[path.Join(p.dst, name)] = true
	}
	return nil
}

func packageEmbedPatterns(fset *token.FileSet, groups []*ast.CommentGroup) ([]string, error) {
	var patterns []string
	for _, group := range groups {
		for _, comment := range group.List {
			args, ok := strings.CutPrefix(comment.Text, "//go:embed")
			if !ok || len(args) == 0 || args[0] != ' ' && args[0] != '\t' {
				continue
			}
			var err error
			patterns, err = appendEmbedPatterns(patterns, args)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fset.Position(comment.Pos()), err)
			}
		}
	}
	return patterns, nil
}

func appendEmbedPatterns(patterns []string, args string) ([]string, error) {
	if strings.TrimSpace(args) == "" {
		return nil, fmt.Errorf("missing //go:embed pattern")
	}
	for args = strings.TrimSpace(args); args != ""; args = strings.TrimSpace(args) {
		if args[0] != '"' && args[0] != '`' {
			end := strings.IndexFunc(args, unicode.IsSpace)
			if end < 0 {
				return append(patterns, args), nil
			}
			patterns = append(patterns, args[:end])
			args = args[end:]
			continue
		}
		quoted, err := strconv.QuotedPrefix(args)
		if err != nil {
			return nil, fmt.Errorf("invalid //go:embed pattern: %w", err)
		}
		pattern, err := strconv.Unquote(quoted)
		if err != nil {
			return nil, fmt.Errorf("invalid //go:embed pattern: %w", err)
		}
		args = args[len(quoted):]
		if r, _ := utf8.DecodeRuneInString(args); args != "" && !unicode.IsSpace(r) {
			return nil, fmt.Errorf("missing space after //go:embed pattern %s", quoted)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

func (c *supportCopy) copyVendorNotices(p supportSourcePackage) error {
	for src, dst := p.src, p.dst; src != "vendor" && !c.notices[src]; src, dst = path.Dir(src), path.Dir(dst) {
		entries, err := fs.ReadDir(c.src.FS(), src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			stem, _, _ := strings.Cut(name, ".")
			switch strings.ToUpper(stem) {
			case "AUTHORS", "CONTRIBUTORS", "COPYING", "COPYRIGHT", "LICENSE", "NOTICE", "PATENTS":
				if err := c.copyRaw(path.Join(src, name), path.Join(dst, name)); err != nil {
					return err
				}
			}
		}
		c.notices[src] = true
	}
	return nil
}

func (c *supportCopy) copyRaw(src, dst string) error {
	info, err := c.src.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("support asset %s is not a regular file", src)
	}
	data, err := c.src.ReadFile(src)
	if err != nil {
		return err
	}
	return c.writeFile(src, dst, data)
}

func (c *supportCopy) writeFile(src, dst string, data []byte) error {
	file := filepath.Join(c.dst, filepath.FromSlash(dst))
	if prev, ok := c.written[dst]; ok {
		if prev != src {
			return fmt.Errorf("support files %s and %s map to the same path %s", prev, src, dst)
		}
		prior, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if !bytes.Equal(prior, data) {
			return fmt.Errorf("%s cannot be emitted both as rewritten Go and as a verbatim asset", src)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0777); err != nil {
		return err
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	c.written[dst] = src
	return nil
}
