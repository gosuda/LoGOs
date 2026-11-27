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
	"go/token"
	"internal/logosfile"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/module"

	"cmd/go/internal/fsys"
	"cmd/go/internal/load"
	"cmd/go/internal/search"
)

const supportVendorDir = "internal/toolchainvendor"

func copyEjectedSupport(src, staging, dst string, roots []string) (err error) {
	destinations, err := supportDestinations(roots)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
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
	}
	for len(c.pending) > 0 {
		importPath := c.pending[0]
		c.pending = c.pending[1:]
		p, err := ejectedSupportPackage(importPath)
		if err != nil {
			return err
		}
		if err := c.copyPackage(p); err != nil {
			return fmt.Errorf("support package %s: %w", importPath, err)
		}
	}
	for _, name := range c.format {
		if !c.embedded[name] {
			queueEjectGofmt(filepath.Join(dst, filepath.FromSlash(name)))
		}
	}
	return nil
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
// excluded. When folding would change the Go version the build line
// implies, it keeps the original structure with version-neutral mode
// tags instead, so the emitted file selects the same files under the
// same language version (a folded go1.N tag would pin the file to that
// version's semantics, such as shared loop variables before go1.22).
func foldEjectedGuard(expr constraint.Expr) (guard constraint.Expr, excluded bool) {
	guard, excluded = foldEjectedModeTags(expr)
	if excluded {
		return nil, true
	}
	if constraint.GoVersion(guard) != constraint.GoVersion(expr) {
		guard = neutralizeEjectedGuard(expr)
	}
	return guard, false
}

// neutralizeEjectedGuard keeps the original expression structure but
// replaces ejected-mode tags with version-neutral equivalents holding
// the same truth value with logosejected set and logosconc unset:
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
		return &constraint.NotExpr{X: neutralizeEjectedGuard(e.X)}
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
	guard, excluded := foldEjectedGuard(expr)
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
	if !p.vendored {
		data = stripEjectedBuildTag(data)
	}
	// The support copy is the ejected form: a lowered LoGOs source belongs
	// only with logosejected set, and its emitted guard is specialized to
	// the residual so plain-Go builds select the same files. The rewrite
	// runs before parsing so every later offset refers to the emitted bytes.
	if lowered {
		skip, guard, rewrite := c.ejectedSupportGuard(p, name)
		if skip {
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
