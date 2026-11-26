// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
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
			patterns, copied, err := c.copyPackageGo(p, name)
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

func (c *supportCopy) copyPackageGo(p supportSourcePackage, name string) ([]string, bool, error) {
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
	mainBytes, extraBytes := splitJSONv2File(fixed)
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
		extraDst := path.Join(p.dst, jsonv2ExtraStem(logosStem(name))+".go")
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
