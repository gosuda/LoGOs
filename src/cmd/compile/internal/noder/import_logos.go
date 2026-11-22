// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"fmt"
	"internal/pkgbits"
	"os"
	"slices"

	"cmd/compile/internal/base"
	"cmd/compile/internal/importer"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/compile/internal/types2"
)

func logosDepVerifiedFacts(dep string) (*facts.Facts, error) {
	if !logosDepResolvable(dep) {
		return nil, nil
	}
	f, err := openPackage(dep)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := readExportData(f)
	if err != nil {
		return nil, err
	}
	member, err := facts.FindMember(f)
	if err != nil || member == nil {
		return nil, err
	}
	rec, err := facts.Import(dep, data, member)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

func logosDepResolvable(dep string) bool {
	if base.Flag.Cfg.PackageFile == nil {
		return true
	}
	return base.Flag.Cfg.PackageFile[dep] != ""
}

func logosHasStructuralFacts(f *facts.Facts) bool {
	return f != nil && (len(f.Opts) > 0 || len(f.Consts) > 0 || len(f.ConstMaps) > 0)
}

func logosFnsNeedPreload(fns []facts.FnFact) bool {
	for _, fn := range fns {
		if fn.Readonly || fn.HasAssert {
			return true
		}
	}
	return false
}

func logosDepNeedsPreload(dep string) (bool, error) {
	rec, err := logosDepVerifiedFacts(dep)
	if err != nil || rec == nil {
		return false, err
	}
	return len(rec.Opts) != 0 || len(rec.Consts) != 0 || len(rec.Readonlys) != 0 || logosFnsNeedPreload(rec.Fns), nil
}

func readLogosFacts(f *os.File, path, data string) (*facts.Facts, error) {
	member, err := facts.FindMember(f)
	if err != nil {
		return nil, err
	}
	if member == nil {
		return nil, nil
	}
	rec, err := facts.Import(path, data, member)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

type logosImporterState struct {
	isLogos        bool
	restoreOverlay bool
	ct             logosCtImportState
}

func logosMarkImporter(m *gcimports, isLogos, restoreOverlay bool) {
	m.logos = logosImporterState{
		isLogos:        isLogos,
		restoreOverlay: restoreOverlay,
	}
}

func logosImportFrom(m *gcimports, path string) (*types2.Package, error) {
	_, pkg, err := readImportFileImpl(m, path)
	return pkg, err
}

func preloadLogosOwners(m *gcimports, pr *pkgbits.PkgDecoder, selfPath string) error {
	if m.packages == nil || pr == nil {
		return nil
	}
	n := pr.NumElems(pkgbits.SectionPkg)
	seen := make(map[string]struct{}, n)
	for i := range n {
		dep := pr.PeekPkgPath(pkgbits.RelElemIdx(i))
		if dep == "" || dep == "builtin" || dep == "unsafe" {
			continue
		}
		if dep == selfPath {
			continue
		}
		if _, ok := seen[dep]; ok {
			continue
		}
		seen[dep] = struct{}{}
		if pkg, ok := m.packages[dep]; ok && pkg.Complete() {
			continue
		}
		if !logosDepResolvable(dep) {
			continue
		}
		if !m.logos.restoreOverlay {
			need, err := logosDepNeedsPreload(dep)
			if err != nil {
				return err
			}
			if !need {
				continue
			}
		}
		if _, _, err := readImportFileImpl(m, dep); err != nil {
			return err
		}
	}
	return nil
}

func readImportFileImpl(m *gcimports, path string) (pkg1 *types.Pkg, pkg2 *types2.Package, err error) {
	target, packages := typecheck.Target, m.packages
	path, err = resolveImportPath(path)
	if err != nil {
		return
	}

	if path == "unsafe" {
		pkg1, pkg2 = types.UnsafePkg, types2.Unsafe

		if !pkg1.Direct {
			pkg1.Direct = true
			target.Imports = append(target.Imports, pkg1)
		}

		return
	}

	pkg1 = types.NewPkg(path, "")
	if packages != nil {
		pkg2 = packages[path]
		if !(pkg1.Direct == (pkg2 != nil && pkg2.Complete())) {

			base.Fatalf("pkg1.Direct == (pkg2 != nil && pkg2.Complete()), path=%s\npackages=%v", path, packages)
		}
	}

	if pkg1.Direct {
		return
	}
	pkg1.Direct = true
	target.Imports = append(target.Imports, pkg1)
	// A failed import leaves pkg1 cached by path: when the types2 package
	// never completed, undo Direct and the target.Imports entry so a later
	// import of the same path passes the pkg1.Direct invariant instead of
	// aborting.
	undo := func() {
		if pkg2 := packages[path]; pkg2 == nil || !pkg2.Complete() {
			pkg1.Direct = false
			if i := slices.Index(target.Imports, pkg1); i >= 0 {
				target.Imports = slices.Delete(target.Imports, i, i+1)
			}
		}
	}

	f, err := openPackage(path)
	if err != nil {
		undo()
		return
	}
	defer f.Close()

	data, err := readExportData(f)
	if err != nil {
		undo()
		return
	}
	var logosFacts *facts.Facts
	if logosFacts, err = readLogosFacts(f, path, data); err != nil {
		undo()
		return
	}

	if base.Debug.Export != 0 {
		fmt.Printf("importing %s (%s)\n", path, f.Name())
	}

	pr := pkgbits.NewPkgDecoder(pkg1.Path, data)

	restoreUnions := m.logos.restoreOverlay && logosFacts != nil && !logosFacts.GoPackage
	if restoreUnions && !logosFacts.CheckedUnionTypes {
		err = fmt.Errorf("logos: %s was built without checked union types; rebuild it with this toolchain", path)
		undo()
		return
	}

	if err = preloadLogosOwners(m, &pr, path); err != nil {
		undo()
		return
	}

	checkLogosUnion := !m.logos.isLogos && logosFacts != nil && logosExportHasLogosFile(&pr)

	var ct *logosCtImportState
	if m.logos.isLogos && logosCtNative {
		ct = &m.logos.ct
	}
	readPackage(newPkgReader(pr), pkg1, false, ct)
	if restoreUnions {
		pkg2 = importer.ReadLogosPackage(m.ctxt, packages, pr)
	} else {
		pkg2 = importer.ReadPackage(m.ctxt, packages, pr)
	}
	if pkg2 != nil && m.logos.restoreOverlay {
		if pkg2, err = applyLogosOverlayForLgoImporter(path, pkg2, packages, logosFacts); err != nil {
			undo()
			return
		}
	} else if pkg2 != nil && logosFacts != nil {
		if pkg2, err = applyLogosReadonlyForGoImporter(path, pkg2, packages, logosFacts); err != nil {
			undo()
			return
		}
	}
	if pkg2 != nil && checkLogosUnion {
		checkLogosGoImport(&pr, pkg2, logosFacts.DualUse)
	}

	err = addFingerprint(path, data)
	return
}
