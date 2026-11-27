// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package next

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"internal/diff"
	"internal/logosfile"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"cmd/go/internal/base"
	"cmd/go/internal/cache"
	"cmd/go/internal/cfg"
	"cmd/go/internal/load"
	"cmd/go/internal/modload"

	"golang.org/x/mod/modfile"
)

const planName = "next_tasks.json"

var moduleMetadata = [...]string{"go.mod", "go.sum", "logos.toml"}

// replacementDir holds the staged copies of the main module's filesystem
// replacements. The leading dot keeps the copies out of the staged ./....
const replacementDir = ".logos-next-replace"

// replacementFileCap and replacementByteCap bound the saved copy of one
// filesystem replacement, so a target such as /, $HOME, or a tree that holds
// node_modules is refused before its files are read into memory.
const (
	replacementFileCap = 20000
	replacementByteCap = 64 << 20
)

func checkFixContext(moddir string) error {
	var staged []string
	for _, flag := range base.GOFLAGS() {
		name, value, _ := strings.Cut(flag, "=")
		switch strings.TrimLeft(name, "-") {
		case "tags":
			if err := (*nextTags)(&staged).Set(value); err != nil {
				return fmt.Errorf("cannot verify the saved module with GOFLAGS flag %s: %v", name, err)
			}
		default:
			return fmt.Errorf("cannot verify the saved module with GOFLAGS flag %s", name)
		}
	}
	if !slices.Equal(staged, cfg.BuildContext.BuildTags) {
		return errors.New("scan tags do not match the staged build tags; pass -tags through GOFLAGS or convert by hand")
	}
	if modload.NewLoader().FindGoWork(base.Cwd()) != "" {
		return errors.New("workspace conversion requires manual review")
	}
	if _, err := os.Lstat(filepath.Join(moddir, "vendor")); err == nil {
		return errors.New("vendored module conversion requires manual review")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cfg.GOCACHEPROG != "" {
		return errors.New("cannot verify the saved module with GOCACHEPROG")
	}
	return checkFixDirectories(moddir)
}

func checkFixDirectories(moddir string) error {
	root, err := filepath.EvalSymlinks(moddir)
	if err != nil {
		return err
	}
	buildCache, _, err := cache.DefaultDir()
	if err != nil {
		return err
	}
	if buildCache == "off" {
		return errors.New("cannot verify the saved module with GOCACHE=off")
	}
	for _, dir := range []string{os.TempDir(), cfg.Getenv("GOTMPDIR"), buildCache, cfg.GOMODCACHE} {
		if dir == "" {
			continue
		}
		path, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		var resolved string
		for {
			resolved, err = filepath.EvalSymlinks(path)
			if err == nil {
				break
			}
			parent := filepath.Dir(path)
			if !errors.Is(err, os.ErrNotExist) || parent == path {
				return err
			}
			path = parent
		}
		if rel, err := filepath.Rel(root, resolved); err == nil && filepath.IsLocal(rel) {
			return fmt.Errorf("temporary or cache directory %q is inside the module", dir)
		}
	}
	return nil
}

type savedFile struct {
	info os.FileInfo
	data []byte
}

func readSavedFile(root *os.Root, name string) (saved savedFile, err error) {
	entry, err := root.Lstat(name)
	if err != nil {
		return savedFile{}, err
	}
	if !entry.Mode().IsRegular() {
		return savedFile{}, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := root.Open(name)
	if err != nil {
		return savedFile{}, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return savedFile{}, err
	}
	if !os.SameFile(entry, info) {
		return savedFile{}, fmt.Errorf("%s changed while opening", name)
	}
	data, err := io.ReadAll(f)
	return savedFile{info: info, data: data}, err
}

func readOptionalFile(root *os.Root, name string) (savedFile, error) {
	file, err := readSavedFile(root, name)
	if errors.Is(err, os.ErrNotExist) {
		return savedFile{}, nil
	}
	return file, err
}

func (f savedFile) same(other savedFile) bool {
	if f.info == nil || other.info == nil {
		return f.info == nil && other.info == nil
	}
	return os.SameFile(f.info, other.info) && f.info.Mode() == other.info.Mode() && bytes.Equal(f.data, other.data)
}

func (f savedFile) verify(root *os.Root, name string) error {
	current, err := readOptionalFile(root, name)
	if err != nil {
		return err
	}
	if !f.same(current) {
		return fmt.Errorf("%s changed during conversion", name)
	}
	return nil
}

type moduleSnapshot struct {
	sources      map[string]savedFile
	metadata     map[string]savedFile
	replacements []replacementSnapshot
	stagedGoMod  []byte
	plan         savedFile
}

// replacementSnapshot is the saved content of one filesystem replacement,
// staged at the module-relative slash path staged.
type replacementSnapshot struct {
	staged string
	files  map[string]savedFile
}

func (r replacementSnapshot) same(other replacementSnapshot) bool {
	return r.staged == other.staged && maps.EqualFunc(r.files, other.files, savedFile.same)
}

func checkStagePackages(roots []*load.Package) error {
	for _, p := range roots {
		if len(p.EmbedPatterns)+len(p.TestEmbedPatterns)+len(p.XTestEmbedPatterns) > 0 {
			return fmt.Errorf("%s: //go:embed inputs require manual review", p.ImportPath)
		}
		if len(p.CgoFiles)+len(p.CFiles)+len(p.CXXFiles)+len(p.MFiles)+len(p.HFiles)+len(p.FFiles)+len(p.SFiles)+len(p.SwigFiles)+len(p.SwigCXXFiles)+len(p.SysoFiles) > 0 {
			return fmt.Errorf("%s: native or cgo sources require manual review", p.ImportPath)
		}
	}
	return nil
}

func captureModule(moddir string) (snapshot *moduleSnapshot, err error) {
	root, err := os.OpenRoot(moddir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return snapshotModule(root, moddir)
}

func snapshotModule(root *os.Root, moddir string) (*moduleSnapshot, error) {
	s := &moduleSnapshot{
		sources: make(map[string]savedFile),
	}
	var err error
	s.plan, err = readOptionalFile(root, planName)
	if err != nil {
		return nil, err
	}
	s.metadata, err = readMetadata(root)
	if err != nil {
		return nil, err
	}
	if s.metadata["go.mod"].info == nil {
		return nil, errors.New("go.mod disappeared before conversion")
	}
	if err := s.freezeModuleReplacements(moddir); err != nil {
		return nil, err
	}
	err = walkModuleFiles(moddir, func(path string, d os.DirEntry) error {
		rel, err := filepath.Rel(moddir, path)
		if err != nil {
			return err
		}
		if logosfile.Is(path) {
			return fmt.Errorf("module already holds LoGOs file %s", rel)
		}
		if d.Name() == "default.pgo" {
			return fmt.Errorf("%s: PGO inputs require manual review", rel)
		}
		if !strings.HasSuffix(path, ".go") || strings.HasPrefix(d.Name(), "_") || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !filepath.IsLocal(rel) || !utf8.ValidString(rel) {
			return fmt.Errorf("unsupported conversion path %q", rel)
		}
		file, err := readSavedFile(root, rel)
		if err != nil {
			return err
		}
		if file.info.Mode() != file.info.Mode().Perm() {
			return fmt.Errorf("%s has a special file mode; convert it by hand", rel)
		}
		if _, err := root.Lstat(lgoName(rel)); err == nil {
			return fmt.Errorf("conversion destination %s already exists", lgoName(rel))
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		s.sources[rel] = file
		return nil
	})
	return s, err
}

func readMetadata(root *os.Root) (map[string]savedFile, error) {
	metadata := make(map[string]savedFile, len(moduleMetadata))
	for _, name := range moduleMetadata {
		file, err := readOptionalFile(root, name)
		if err != nil {
			return nil, err
		}
		metadata[name] = file
	}
	return metadata, nil
}

func (s *moduleSnapshot) verify(moddir string) error {
	current, err := captureModule(moddir)
	if err != nil {
		return err
	}
	if !maps.EqualFunc(s.sources, current.sources, savedFile.same) {
		return errors.New("source snapshot changed during conversion")
	}
	if !maps.EqualFunc(s.metadata, current.metadata, savedFile.same) {
		return errors.New("module metadata changed during conversion")
	}
	if !s.plan.same(current.plan) {
		return fmt.Errorf("%s changed during conversion", planName)
	}
	if !slices.EqualFunc(s.replacements, current.replacements, replacementSnapshot.same) {
		return errors.New("local replacement changed during conversion")
	}
	return checkFixContext(moddir)
}

type conversionFile struct {
	source string
	data   []byte
	before savedFile
}

func (s *scanState) writePatch(ctx context.Context, moddir string) error {
	files := make([]conversionFile, 0, len(s.snapshot.sources))
	rewrites := 0
	for _, rel := range slices.Sorted(maps.Keys(s.snapshot.sources)) {
		before := s.snapshot.sources[rel]
		work := s.files[rel]
		if work == nil {
			return fmt.Errorf("%s was not checked in the active Go build; use logos next --ai-patch and review the rename", rel)
		}
		if issue := work.renameIssue; issue != nil {
			return fmt.Errorf("%s:%d: %s", rel, issue.StartLine, issue.Instruction)
		}
		files = append(files, conversionFile{source: rel, before: before, data: applyEdits(before.data, work.edits)})
		rewrites += len(work.edits)
	}
	plan := s.snapshot.plan
	var planData []byte
	var err error
	if plan.info == nil {
		planData, err = encodePlan(s.tasks)
	} else {
		planData, err = mergePlan(plan.data, s.tasks)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", planName, err)
	}
	if err := s.snapshot.preflight(ctx, files); err != nil {
		return err
	}
	if err := s.snapshot.verify(moddir); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	out := bufio.NewWriter(os.Stdout)
	var patch bytes.Buffer
	for _, file := range files {
		source := filepath.ToSlash(file.source)
		target := lgoName(source)
		mode := 0o100644
		if file.before.info.Mode()&0o100 != 0 {
			mode = 0o100755
		}
		fmt.Fprintf(&patch, "diff --git %s %s\ndeleted file mode %06o\n", gitPatchPath("a/"+source), gitPatchPath("b/"+source), mode)
		patch.Write(patchHunks(gitPatchPath("a/"+source), file.before.data, "/dev/null", nil))
		fmt.Fprintf(&patch, "diff --git %s %s\nnew file mode %06o\n", gitPatchPath("a/"+target), gitPatchPath("b/"+target), mode)
		patch.Write(patchHunks("/dev/null", nil, gitPatchPath("b/"+target), file.data))
	}
	if plan.info == nil {
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\nnew file mode 100644\n", planName, planName)
		patch.Write(patchHunks("/dev/null", nil, "b/"+planName, planData))
	} else if hunks := patchHunks("a/"+planName, plan.data, "b/"+planName, planData); len(hunks) > 0 {
		fmt.Fprintf(&patch, "diff --git a/%s b/%s\n", planName, planName)
		patch.Write(hunks)
	}
	if _, err := out.Write(patch.Bytes()); err != nil {
		return fmt.Errorf("writing conversion patch: %w", err)
	}
	if err := out.Flush(); err != nil {
		return fmt.Errorf("writing conversion patch: %w", err)
	}
	s.reportTasks()
	_, _ = fmt.Fprintf(os.Stderr, "logos next: patch for %d files with %d rewrites; %d scan tasks for %s\n", len(files), rewrites, len(s.tasks), planName)
	return nil
}

func patchHunks(oldName string, old []byte, newName string, updated []byte) []byte {
	_, hunks, _ := bytes.Cut(diff.Diff(oldName, old, newName, updated), []byte("\n"))
	return hunks
}

func gitPatchPath(path string) string {
	if strings.IndexFunc(path, func(r rune) bool {
		return r <= ' ' || r >= 0x7f || r == '"' || r == '\\'
	}) == -1 {
		return path
	}
	var b strings.Builder
	b.Grow(len(path) + 2)
	b.WriteByte('"')
	for i := range len(path) {
		c := path[i]
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < ' ' || c >= 0x7f:
			b.WriteByte('\\')
			b.WriteByte('0' + (c >> 6))
			b.WriteByte('0' + ((c >> 3) & 7))
			b.WriteByte('0' + (c & 7))
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (s *moduleSnapshot) preflight(ctx context.Context, files []conversionFile) (err error) {
	dir, err := os.MkdirTemp("", "logos-next-check-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	if err := s.stage(dir, files); err != nil {
		return fmt.Errorf("preflight staging: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Test compilation accepts the null device across all packages, even when
	// distinct packages have the same default test binary name.
	for _, args := range [][]string{{"build"}, {"test", "-c", "-vet=off"}} {
		args = append(args, "-o", os.DevNull, "-buildvcs=false", "-mod=readonly", "./...")
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Args[0] = "logos"
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GOFLAGS="+cfg.Getenv("GOFLAGS"), "GOWORK=off", "GOTOOLCHAIN=local",
			"GOPROXY=off", "GONOPROXY=none", "GOSUMDB=off", "GOAUTH=off",
			"TEST_TELEMETRY_DIR="+filepath.Join(dir, ".telemetry"), "GO_TELEMETRY_CHILD=2",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("preflight %s failed: %w\n%s", args[0], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func (s *moduleSnapshot) stage(dir string, files []conversionFile) error {
	for _, name := range moduleMetadata {
		file := s.metadata[name]
		if file.info == nil {
			continue
		}
		data := file.data
		if name == "go.mod" {
			data = s.stagedGoMod
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	for _, file := range files {
		if err := writeStagedFile(filepath.Join(dir, lgoName(file.source)), file.data); err != nil {
			return err
		}
	}
	for _, r := range s.replacements {
		for name, file := range r.files {
			if err := writeStagedFile(filepath.Join(dir, filepath.FromSlash(r.staged), name), file.data); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeStagedFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// freezeModuleReplacements saves every filesystem replacement of the main
// module and points the staged go.mod at a staged copy of the saved bytes, so
// the preflight build reads exactly the source that verify compares again.
func (s *moduleSnapshot) freezeModuleReplacements(moddir string) error {
	data := s.metadata["go.mod"].data
	file, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return err
	}
	for _, replace := range file.Replace {
		if replace.New.Version != "" {
			continue
		}
		files, err := captureReplacement(moddir, replace.New.Path)
		if err != nil {
			return fmt.Errorf("local replacement %s: %w", replace.Old.Path, err)
		}
		arrow := slices.Index(replace.Syntax.Token, "=>")
		if arrow < 0 || arrow+1 >= len(replace.Syntax.Token) {
			return fmt.Errorf("local replacement %s: unexpected replace line", replace.Old.Path)
		}
		staged := replacementDir + "/" + strconv.Itoa(len(s.replacements))
		replace.Syntax.Token[arrow+1] = modfile.AutoQuote("./" + staged)
		s.replacements = append(s.replacements, replacementSnapshot{staged: staged, files: files})
	}
	s.stagedGoMod = data
	if len(s.replacements) > 0 {
		s.stagedGoMod = modfile.Format(file.Syntax)
	}
	return nil
}

// captureReplacement saves the regular files of the replacement module at
// target, a replace path of the go.mod in moddir. Nested modules and dot
// directories are not part of it; any other entry that is not a regular
// file refuses the conversion, and so does the file that would take the
// copy past replacementFileCap files or replacementByteCap bytes, before it
// is opened.
func captureReplacement(moddir, target string) (files map[string]savedFile, err error) {
	dir := target
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(moddir, dir)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	tooLarge := func(limit int64, unit string) error {
		return fmt.Errorf("%s: too large to freeze: more than %d %s", target, limit, unit)
	}
	files = make(map[string]savedFile)
	var size int64
	err = fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.FromSlash(name)
		if !d.IsDir() {
			if len(files) == replacementFileCap {
				return tooLarge(replacementFileCap, "files")
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if size+info.Size() > replacementByteCap {
				return tooLarge(replacementByteCap, "bytes")
			}
			file, err := readSavedFile(root, rel)
			if err != nil {
				return err
			}
			if size += int64(len(file.data)); size > replacementByteCap {
				return tooLarge(replacementByteCap, "bytes")
			}
			files[rel] = file
			return nil
		}
		if name == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if st, err := root.Lstat(filepath.Join(rel, "go.mod")); err == nil && st.Mode().IsRegular() {
			return fs.SkipDir
		}
		return nil
	})
	return files, err
}

func publishPlan(root *os.Root, pending string, plan savedFile) (string, error) {
	if err := plan.verify(root, planName); err != nil {
		return "", err
	}
	if plan.info == nil {
		return "", root.Link(pending, planName)
	}
	backup := filepath.Join(filepath.Dir(pending), "previous-plan")
	if err := root.Rename(planName, backup); err != nil {
		return "", err
	}
	if err := plan.verify(root, backup); err != nil {
		return backup, err
	}
	return backup, root.Link(pending, planName)
}

func finishPublication(root *os.Root, staging string, failure, rollback error) error {
	if rollback != nil {
		return errors.Join(failure, fmt.Errorf("rollback incomplete; originals retained in %s: %w", filepath.Join(root.Name(), staging), rollback))
	}
	if err := root.RemoveAll(staging); err != nil {
		if failure != nil {
			return errors.Join(failure, err)
		}
		_, _ = fmt.Fprintf(os.Stderr, "logos next: could not remove conversion staging directory: %v\n", err)
	}
	return failure
}

func writePlan(moddir string, data []byte) (err error) {
	root, err := os.OpenRoot(moddir)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "logos next: could not close module directory: %v\n", closeErr)
			}
		}
	}()
	plan, err := readOptionalFile(root, planName)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp(moddir, ".logos-next-")
	if err != nil {
		return err
	}
	staging := filepath.Base(dir)
	var backup string
	defer func() {
		var rollbackErr error
		if err != nil && backup != "" {
			rollbackErr = root.Link(backup, planName)
		}
		err = finishPublication(root, staging, err, rollbackErr)
	}()
	pending := filepath.Join(staging, planName)
	if err := preparePlan(root, pending, plan, data); err != nil {
		return err
	}
	backup, err = publishPlan(root, pending, plan)
	return err
}

func preparePlan(root *os.Root, pending string, plan savedFile, data []byte) error {
	mode := os.FileMode(0o644)
	if plan.info != nil {
		mode = plan.info.Mode().Perm()
	}
	return writeExclusive(root, pending, data, mode)
}

func writeExclusive(root *os.Root, name string, data []byte, mode os.FileMode) error {
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Chmod(mode), file.Close())
}
