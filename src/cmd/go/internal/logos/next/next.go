// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package next

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"internal/logosfile"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/load"
	"cmd/go/internal/modload"
	"cmd/go/internal/work"
)

var CmdNext = &base.Command{
	Run:       runNext,
	UsageLine: "logos next [--fix | --ai-patch]",
	Short:     "convert a Go module to a loGo/s module",
	Long: `
Next previews verified Go-to-loGo/s rewrites in a unified diff without
changing files. Next --fix checks a saved module snapshot with a staged
loGo/s build and test compilation, then writes a unified patch to standard
output. The patch deletes the original .go files, adds the .lgo files, and
adds new tasks to next_tasks.json. It does not change module files, run
tests, or fetch modules. Review the patch and apply it from the module root
with git apply to retain the executable bit.
Next --ai-patch writes and prints next_tasks.json without renaming sources.

Automatic rewrites are limited to unadorned fmt.Sprintf verbs for primitive
values. Review tasks leave explicit error returns and append loops intact:
? records a trace on failure, and chains can change slice capacity or nilness.
Source files not checked in the active build require manual conversion.
	`,
}

var nextFix bool

var nextAIPatch bool

type nextTags []string

func (v *nextTags) Set(s string) error {
	if strings.Contains(s, " ") || strings.Contains(s, "'") {
		return (*base.StringsFlag)(v).Set(s)
	}
	*v = nil
	for tag := range strings.SplitSeq(s, ",") {
		if tag != "" {
			*v = append(*v, tag)
		}
	}
	return nil
}

func (v *nextTags) String() string { return "<TagsFlag>" }

func init() {
	CmdNext.Flag.BoolVar(&nextFix, "fix", false, "")
	CmdNext.Flag.BoolVar(&nextAIPatch, "ai-patch", false, "")
	CmdNext.Flag.Var((*nextTags)(&cfg.BuildContext.BuildTags), "tags", "")
	base.AddChdirFlag(&CmdNext.Flag)
}

func Register(root *base.Command) {
	root.Commands = append(root.Commands, CmdNext)
}

type wireEdit struct {
	Rule        string `json:"rule"`
	FuncSig     string `json:"function_signature"`
	StartLine   int    `json:"start_line"`
	StartCol    int    `json:"start_col"`
	EndLine     int    `json:"end_line"`
	EndCol      int    `json:"end_col"`
	Replacement string `json:"replacement"`
}

type wireTask struct {
	Category    string `json:"category"`
	Rule        string `json:"rule"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	FuncSig     string `json:"function_signature"`
	Instruction string `json:"ai_instruction"`
}

type wireFile struct {
	File        string     `json:"file"`
	Sum         string     `json:"sha256"`
	Edits       []wireEdit `json:"edits"`
	Tasks       []wireTask `json:"tasks"`
	RenameIssue *wireTask  `json:"rename_issue"`
}

type wireReport struct {
	Version string     `json:"version"`
	Package string     `json:"package"`
	Files   []wireFile `json:"files"`
}

type taskSpan struct {
	Start int `json:"start_line"`
	End   int `json:"end_line"`
}

type planTask struct {
	ID        string   `json:"id"`
	Category  string   `json:"category"`
	File      string   `json:"file"`
	Span      taskSpan `json:"span"`
	Rule      string   `json:"rule"`
	FuncSig   string   `json:"function_signature"`
	AiInstr   string   `json:"ai_instruction"`
	VerifyCmd string   `json:"verification_cmd"`
}

const planVersion = "logos-next-1"

type planFile[T planTask | json.RawMessage] struct {
	Version string `json:"version"`
	Tasks   []T    `json:"tasks"`
}

type fileWork struct {
	data        []byte
	sum         string
	edits       []wireEdit
	tasks       []wireTask
	renameIssue *wireTask
	reports     int
	votes       map[wireEdit]int
}

func runNext(ctx context.Context, cmd *base.Command, args []string) {
	if len(args) > 0 {
		base.Fatalf("logos next: unexpected package arguments")
	}
	if nextFix && nextAIPatch {
		base.Fatalf("logos next: --fix and --ai-patch are exclusive")
	}
	gomod := modload.FindGoMod(base.Cwd())
	if gomod == "" {
		base.Fatalf("logos next: no main module (run inside a module)")
	}
	moddir := filepath.Dir(gomod)
	modload.ExplicitWriteGoMod = true
	if nextFix {
		if err := checkFixContext(moddir); err != nil {
			base.Fatalf("logos next: %v", err)
		}
		cfg.GOPROXY, cfg.GONOPROXY, cfg.GOSUMDB, cfg.GOAUTH = "off", "none", "off", "off"
	}
	work := scanModule(ctx, moddir)
	switch {
	case nextFix:
		if err := work.writePatch(ctx, moddir); err != nil {
			base.Fatalf("logos next: %v", err)
		}
	case nextAIPatch:
		data := work.planData()
		if err := writePlan(moddir, data); err != nil {
			base.Fatalf("logos next: %v", err)
		}
		if _, err := os.Stdout.Write(data); err != nil {
			base.Fatalf("logos next: writing plan to standard output: %v", err)
		}
	default:
		work.printDiff()
	}
}

type scanState struct {
	files    map[string]*fileWork
	order    []string
	tasks    []planTask
	snapshot *moduleSnapshot
	moddir   string
}

func scanModule(ctx context.Context, moddir string) *scanState {
	var snapshot *moduleSnapshot
	var snapshotErr error
	if nextFix {
		snapshot, snapshotErr = captureModule(moddir)
	}
	staging, err := os.MkdirTemp("", "logosnext")
	if err != nil {
		base.Fatalf("logos next: %v", err)
	}
	base.AtExit(func() { _ = os.RemoveAll(staging) })
	defer func() {
		if err := os.RemoveAll(staging); err != nil {
			base.Errorf("logos next: %v", err)
		}
	}()
	sidecar := filepath.Join(staging, "next.jsonl")
	moduleLoader := modload.NewLoader()
	moduleLoader.InitWorkfile()
	work.BuildInit(moduleLoader)
	load.ArmLogosNext(sidecar)
	defer load.DisarmLogosNext()
	opts := load.PackageOpts{AutoVCS: !nextFix}
	pkgs := load.PackagesAndErrors(moduleLoader, ctx, opts, []string{"all"})
	load.CheckPackageErrors(pkgs)
	roots := nextRoots(pkgs)
	if len(roots) == 0 {
		if hasLgoFiles(moddir) {
			out := os.Stdout
			if nextFix {
				out = os.Stderr
			}
			fmt.Fprintln(out, "logos next: already converted")
			base.Exit()
		}
		base.Fatalf("logos next: no Go packages to convert")
	}
	if snapshotErr != nil {
		base.Fatalf("logos next: %v", snapshotErr)
	}
	if nextFix {
		if err := checkStagePackages(roots); err != nil {
			base.Fatalf("logos next: %v", err)
		}
	}
	state := &scanState{files: map[string]*fileWork{}, snapshot: snapshot, moddir: moddir}
	buildNextRoots(moduleLoader, ctx, roots)
	buildNextTests(moduleLoader, ctx, opts, roots)
	state.readSidecar(sidecar, moddir)
	return state
}

func nextRoots(pkgs []*load.Package) []*load.Package {
	var roots []*load.Package
	for _, p := range load.PackageList(pkgs) {
		if !nextRootKeep(p) {
			continue
		}
		roots = append(roots, p)
	}
	slices.SortFunc(roots, func(a, b *load.Package) int { return cmp.Compare(a.ImportPath, b.ImportPath) })
	return roots
}

func nextRootKeep(p *load.Package) bool {
	if p.Module == nil {
		return false
	}
	if !p.Module.Main {
		return false
	}
	if p.Standard {
		return false
	}
	if p.IsLogos() {
		return false
	}
	if p.ForTest != "" {
		return false
	}
	if p.Internal.TestmainGo != nil {
		return false
	}
	return load.HasNextFiles(p)
}

func doBuild(b *work.Builder, moduleLoader *modload.Loader, ctx context.Context, roots []*load.Package) {
	root := &work.Action{Mode: "go build"}
	for _, p := range roots {
		if p.IsTestOnly() && p.Internal.TestmainGo == nil {
			continue
		}
		root.Deps = append(root.Deps, b.AutoAction(moduleLoader, work.ModeBuild, work.ModeBuild, p))
	}
	b.Do(ctx, root)
	if root.Failed != nil {
		base.SetExitStatus(1)
	}
	if base.GetExitStatus() != 0 {
		base.Exit()
	}
}

func buildNextRoots(moduleLoader *modload.Loader, ctx context.Context, roots []*load.Package) {
	b := work.NewBuilder("", moduleLoader.VendorDirOrEmpty)
	defer func() {
		if err := b.Close(); err != nil {
			base.Fatalf("logos next: %v", err)
		}
	}()
	doBuild(b, moduleLoader, ctx, roots)
}

func buildNextTests(moduleLoader *modload.Loader, ctx context.Context, opts load.PackageOpts, roots []*load.Package) {
	b := work.NewBuilder("", moduleLoader.VendorDirOrEmpty)
	defer func() {
		if err := b.Close(); err != nil {
			base.Fatalf("logos next: %v", err)
		}
	}()
	var mains []*load.Package
	for _, p := range roots {
		pmain := buildNextTestMain(ctx, moduleLoader, opts, p)
		if pmain == nil {
			continue
		}
		if err := b.StageTestmain(pmain); err != nil {
			base.Fatalf("logos next: %v", err)
		}
		mains = append(mains, pmain)
	}
	if len(mains) == 0 {
		return
	}
	doBuild(b, moduleLoader, ctx, mains)
}

func buildNextTestMain(ctx context.Context, moduleLoader *modload.Loader, opts load.PackageOpts, p *load.Package) *load.Package {
	if len(p.TestGoFiles) == 0 && len(p.XTestGoFiles) == 0 {
		return nil
	}
	pmain, _, _, perr := load.TestPackagesFor(moduleLoader, ctx, opts, p, nil)
	if perr != nil {
		base.Fatalf("logos next: %v", perr)
	}
	if pmain == nil {
		base.Fatalf("logos next: no test main for %s", p.ImportPath)
	}
	return pmain
}

func (s *scanState) readSidecar(sidecar, moddir string) {
	var known map[string]string
	if s.snapshot != nil {
		known = make(map[string]string, len(s.snapshot.sources))
		for rel := range s.snapshot.sources {
			known[filepath.Join(moddir, rel)] = rel
		}
	} else {
		known = walkGoFiles(moddir)
	}
	lines := readLines(sidecar)
	byFile := map[string]*fileWork{}
	for _, line := range lines {
		var rep wireReport
		if err := json.Unmarshal([]byte(line), &rep); err != nil {
			base.Fatalf("logos next: bad sidecar: %v", err)
		}
		if rep.Version != "logos-next-scan-2" {
			base.Fatalf("logos next: unsupported compiler scan version %q", rep.Version)
		}
		for _, f := range rep.Files {
			rel, ok := known[filepath.Clean(f.File)]
			if !ok {
				continue
			}
			w := byFile[rel]
			if w == nil {
				w = &fileWork{sum: f.Sum, votes: make(map[wireEdit]int)}
				if s.snapshot != nil {
					w.data = s.snapshot.sources[rel].data
				} else {
					data, err := os.ReadFile(filepath.Join(moddir, rel))
					if err != nil {
						base.Fatalf("logos next: %v", err)
					}
					w.data = data
				}
				byFile[rel] = w
			}
			if w.sum != f.Sum || !applySumOk(w.data, f.Sum) {
				base.Fatalf("logos next: %s changed during scan", rel)
			}
			w.reports++
			for _, edit := range dedupEdits(f.Edits) {
				w.votes[edit]++
			}
			w.tasks = append(w.tasks, f.Tasks...)
			if f.RenameIssue != nil {
				w.renameIssue = f.RenameIssue
				w.tasks = append(w.tasks, *f.RenameIssue)
			}
		}
	}
	for rel, w := range byFile {
		var candidates []wireEdit
		for edit := range w.votes {
			candidates = append(candidates, edit)
		}
		candidates = dedupEdits(candidates)
		for _, e := range candidates {
			if e.Rule == "sprintf-interp" && w.votes[e] == w.reports {
				w.edits = append(w.edits, e)
				continue
			}
			w.tasks = append(w.tasks, reviewTask(e))
		}
		checkSpans(rel, w.data, w.edits)
		s.files[rel] = w
		s.order = append(s.order, rel)
	}
	slices.Sort(s.order)
	s.checkRenameOrder()
	s.assignTasks()
}

func reviewTask(e wireEdit) wireTask {
	instruction := "the replacement is not verified in every checked package variant"
	return wireTask{
		Category:    "ai-patch",
		Rule:        e.Rule,
		StartLine:   e.StartLine,
		EndLine:     e.EndLine,
		FuncSig:     e.FuncSig,
		Instruction: instruction,
	}
}

func walkGoFiles(moddir string) map[string]string {
	known := map[string]string{}
	err := walkModuleFiles(moddir, func(path string, d os.DirEntry) error {
		if !walkNextFileOK(path) {
			return nil
		}
		return walkNextCollect(known, moddir, path)
	})
	if err != nil {
		base.Fatalf("logos next: %v", err)
	}
	return known
}

func walkModuleFiles(moddir string, visit func(string, os.DirEntry) error) error {
	return filepath.WalkDir(moddir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return visit(path, d)
		}
		if walkNextSkipDir(path, moddir) {
			return filepath.SkipDir
		}
		return nil
	})
}

func walkNextSkipDir(path, moddir string) bool {
	if path == moddir {
		return false
	}
	dirname := filepath.Base(path)
	if dirname == "vendor" || dirname == "testdata" || strings.HasPrefix(dirname, ".") || strings.HasPrefix(dirname, "_") {
		return true
	}
	if st, err := os.Lstat(filepath.Join(path, "go.mod")); err == nil && st.Mode().IsRegular() {
		return true
	}
	return false
}
func hasLgoFiles(moddir string) bool {
	found := false
	if err := filepath.WalkDir(moddir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found {
			return err
		}
		if d.IsDir() {
			if walkNextSkipDir(path, moddir) {
				return filepath.SkipDir
			}
			return nil
		}
		if logosfile.Is(path) {
			found = true
		}
		return nil
	}); err != nil {
		base.Fatalf("logos next: %v", err)
	}
	return found
}

func walkNextFileOK(path string) bool {
	if logosfile.Is(path) {
		base.Fatalf("logos next: module already holds loGo/s file %s", path)
	}
	if !strings.HasSuffix(path, ".go") {
		return false
	}
	name := filepath.Base(path)
	if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

func walkNextCollect(known map[string]string, moddir, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	modReal, err := filepath.EvalSymlinks(moddir)
	if err != nil {
		return err
	}
	relReal, err := filepath.Rel(modReal, real)
	if err != nil {
		return err
	}
	if relReal == ".." || strings.HasPrefix(relReal, ".."+string(filepath.Separator)) {
		return nil
	}
	rel, err := filepath.Rel(moddir, abs)
	if err != nil {
		return err
	}
	known[filepath.Clean(abs)] = rel
	return nil
}

func readLines(sidecar string) []string {
	data, err := os.ReadFile(sidecar)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		base.Fatalf("logos next: %v", err)
	}
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func dedupEdits(edits []wireEdit) []wireEdit {
	seen := map[wireEdit]bool{}
	var out []wireEdit
	for _, e := range edits {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	slices.SortFunc(out, func(a, b wireEdit) int {
		return cmp.Or(cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.StartCol, b.StartCol))
	})
	return out
}

func checkSpans(rel string, data []byte, edits []wireEdit) {
	starts := lineStartsOf(data)
	last := 0
	for _, e := range edits {
		start := offsetOf(starts, len(data), e.StartLine, e.StartCol)
		end := offsetOf(starts, len(data), e.EndLine, e.EndCol)
		if start < 0 || end <= start {
			base.Fatalf("logos next: rewrite out of range in %s at %d:%d", rel, e.StartLine, e.StartCol)
		}
		if start < last {
			base.Fatalf("logos next: overlapping rewrites in %s", rel)
		}
		last = end
	}
}

func (s *scanState) assignTasks() {
	seen := map[planTask]bool{}
	n := 0
	for _, rel := range s.order {
		w := s.files[rel]
		slices.SortFunc(w.tasks, func(a, b wireTask) int {
			return cmp.Or(
				cmp.Compare(a.StartLine, b.StartLine), cmp.Compare(a.EndLine, b.EndLine),
				cmp.Compare(a.Rule, b.Rule), cmp.Compare(a.FuncSig, b.FuncSig),
				cmp.Compare(a.Instruction, b.Instruction), cmp.Compare(a.Category, b.Category),
			)
		})
		for _, t := range w.tasks {
			task := planTask{
				Category:  t.Category,
				File:      lgoName(rel),
				Span:      taskSpan{Start: remap(w.edits, t.StartLine), End: remap(w.edits, t.EndLine)},
				Rule:      t.Rule,
				FuncSig:   t.FuncSig,
				AiInstr:   t.Instruction,
				VerifyCmd: "logos build ./...",
			}
			if seen[task] {
				continue
			}
			seen[task] = true
			n++
			task.ID = fmt.Sprintf("next-%03d", n)
			s.tasks = append(s.tasks, task)
		}
	}
}

func (s *scanState) checkRenameOrder() {
	var all []string
	if s.snapshot != nil {
		for rel := range s.snapshot.sources {
			all = append(all, rel)
		}
	} else {
		for _, rel := range walkGoFiles(s.moddir) {
			all = append(all, rel)
		}
	}
	dirs := make(map[string][]string)
	for _, rel := range all {
		dir := filepath.Dir(rel)
		dirs[dir] = append(dirs[dir], rel)
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		s.checkDirOrder(dir, dirs[dir])
	}
}
func (s *scanState) checkDirOrder(dir string, files []string) {
	if len(files) < 2 {
		return
	}
	byGo := slices.Clone(files)
	slices.SortFunc(byGo, func(a, b string) int {
		return cmp.Compare(filepath.Base(a), filepath.Base(b))
	})
	byLgo := slices.Clone(files)
	slices.SortFunc(byLgo, func(a, b string) int {
		return cmp.Compare(lgoName(filepath.Base(a)), lgoName(filepath.Base(b)))
	})
	if slices.Equal(byGo, byLgo) {
		return
	}
	for _, rel := range files {
		w := s.files[rel]
		if w == nil || w.renameIssue != nil {
			continue
		}
		issue := &wireTask{
			Category:    "ai-patch",
			Rule:        "rename-order",
			StartLine:   1,
			EndLine:     1,
			Instruction: "renaming every file in this directory to .lgo changes the lexical file order the staged build presents; convert this package by hand",
		}
		w.renameIssue = issue
		w.tasks = append(w.tasks, *issue)
	}
}

func remap(edits []wireEdit, line int) int {
	orig := line
	delta := 0
	for _, e := range edits {
		if e.EndLine < orig {
			delta += newLines(e.Replacement) - (e.EndLine - e.StartLine + 1)
		}
	}
	return orig + delta
}

func newLines(s string) int {
	return strings.Count(s, "\n") + 1
}

func lgoName(rel string) string {
	return strings.TrimSuffix(rel, ".go") + logosfile.Canonical
}

func (s *scanState) planData() []byte {
	data, err := encodePlan(s.tasks)
	if err != nil {
		base.Fatalf("logos next: %v", err)
	}
	return data
}

func encodePlan[T planTask | json.RawMessage](tasks []T) ([]byte, error) {
	if tasks == nil {
		tasks = []T{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(planFile[T]{Version: planVersion, Tasks: tasks}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func mergePlan(before []byte, discovered []planTask) ([]byte, error) {
	if !utf8.Valid(before) {
		return nil, errors.New("invalid UTF-8")
	}
	tokens := json.NewDecoder(bytes.NewReader(before))
	tokens.UseNumber()
	if err := checkPlanJSON(tokens); err != nil {
		return nil, err
	}
	if _, err := tokens.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the plan")
	}
	var plan planFile[json.RawMessage]
	dec := json.NewDecoder(bytes.NewReader(before))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil {
		return nil, err
	}
	if plan.Version != planVersion {
		return nil, fmt.Errorf("unsupported plan version %q", plan.Version)
	}
	if plan.Tasks == nil {
		return nil, errors.New("plan requires a tasks array")
	}

	ids := make(map[string]bool, len(plan.Tasks))
	seen := make(map[planTask]bool, len(plan.Tasks))
	for i, raw := range plan.Tasks {
		var task planTask
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&task); err != nil {
			return nil, fmt.Errorf("task %d: %w", i+1, err)
		}
		if strings.TrimSpace(task.ID) == "" || task.Category == "" || task.File == "" ||
			task.Rule == "" || task.AiInstr == "" || task.VerifyCmd == "" ||
			task.Span.Start < 1 || task.Span.End < task.Span.Start {
			return nil, fmt.Errorf("task %d is missing required data or has an invalid span", i+1)
		}
		if ids[task.ID] {
			return nil, fmt.Errorf("duplicate task ID %q", task.ID)
		}
		ids[task.ID] = true
		task.ID = ""
		seen[task] = true
	}

	oldCount := len(plan.Tasks)
	nextID := 0
	for _, task := range discovered {
		task.ID = ""
		if seen[task] {
			continue
		}
		seen[task] = true
		for {
			nextID++
			task.ID = fmt.Sprintf("next-%03d", nextID)
			if !ids[task.ID] {
				break
			}
		}
		raw, err := json.Marshal(task)
		if err != nil {
			return nil, err
		}
		plan.Tasks = append(plan.Tasks, raw)
	}
	if len(plan.Tasks) == oldCount {
		return before, nil
	}
	return encodePlan(plan.Tasks)
}

func checkPlanJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not supported in a plan")
	}
	var names map[string]bool
	switch token {
	case json.Delim('{'):
		names = make(map[string]bool)
	case json.Delim('['):
	default:
		return nil
	}
	for dec.More() {
		if names != nil {
			member, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := member.(string)
			if !ok || strings.ContainsFunc(name, func(r rune) bool { return r != '_' && (r < 'a' || r > 'z') }) {
				return fmt.Errorf("unsupported JSON member %q", member)
			}
			if names[name] {
				return fmt.Errorf("duplicate JSON member %q", name)
			}
			names[name] = true
		}
		if err := checkPlanJSON(dec); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

func applyEdits(data []byte, edits []wireEdit) []byte {
	if len(edits) == 0 {
		return data
	}
	starts := lineStartsOf(data)
	size := len(data)
	for _, e := range edits {
		start := offsetOf(starts, len(data), e.StartLine, e.StartCol)
		end := offsetOf(starts, len(data), e.EndLine, e.EndCol)
		size += len(e.Replacement) - (end - start)
	}
	out := make([]byte, 0, size)
	last := 0
	for _, e := range edits {
		start := offsetOf(starts, len(data), e.StartLine, e.StartCol)
		end := offsetOf(starts, len(data), e.EndLine, e.EndCol)
		out = append(out, data[last:start]...)
		out = append(out, e.Replacement...)
		last = end
	}
	return append(out, data[last:]...)
}

func lineStartsOf(data []byte) []int {
	starts := []int{0}
	for i, c := range data {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

func offsetOf(starts []int, size, line, col int) int {
	if line < 1 || line > len(starts) || col < 1 {
		return -1
	}
	end := size
	if line < len(starts) {
		end = starts[line] - 1
		if end > size {
			end = size
		}
	}
	start := starts[line-1]
	if col-1 > end-start {
		return -1
	}
	return start + col - 1
}

func applySumOk(data []byte, sum string) bool {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]) == sum
}

func (s *scanState) reportTasks() {
	for _, task := range s.tasks {
		fmt.Fprintf(os.Stderr, "logos next: review only: %s at %s:%d: %s\n", task.Rule, task.File, task.Span.Start, task.AiInstr)
	}
}

func (s *scanState) printDiff() {
	s.reportTasks()
	shown := false
	for _, rel := range s.order {
		w := s.files[rel]
		if len(w.edits) == 0 {
			continue
		}
		shown = true
		fmt.Printf("--- a/%s\n+++ b/%s\n", rel, lgoName(rel))
		for _, hunk := range hunks(w) {
			fmt.Print(hunk)
		}
	}
	if !shown {
		fmt.Println("logos next: no conversions found")
	}
}

const diffContext = 3

type lineSpan struct{ s, e int }

func hunks(w *fileWork) []string {
	oldLines := fileLines(w.data)
	newLines := fileLines(applyEdits(w.data, w.edits))
	var olds []lineSpan
	for _, e := range w.edits {
		olds = append(olds, lineSpan{e.StartLine, e.EndLine})
	}
	var out []string
	for _, m := range mergeSpans(olds) {
		ns := remap(w.edits, m.s)
		ne := ns + (m.e - m.s) + deltaLines(w.edits, m.s, m.e)
		os := clamp(m.s-diffContext, len(oldLines))
		oe := clamp(m.e+diffContext, len(oldLines))
		ns2 := clamp(ns-diffContext, len(newLines))
		ne2 := clamp(ne+diffContext, len(newLines))
		var b strings.Builder
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", os, oe-os+1, ns2, ne2-ns2+1)
		for _, op := range diffOps(oldLines[os-1:oe], newLines[ns2-1:ne2]) {
			switch op.kind {
			case ' ':
				fmt.Fprintf(&b, " %s\n", op.a)
			case '-':
				fmt.Fprintf(&b, "-%s\n", op.a)
			case '+':
				fmt.Fprintf(&b, "+%s\n", op.b)
			}
		}
		out = append(out, b.String())
	}
	return out
}

func fileLines(data []byte) []string {
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

type diffOp struct {
	kind byte
	a    string
	b    string
}

func diffOps(a, b []string) []diffOp {
	rev := diffBacktrack(a, b, diffTable(a, b))
	out := make([]diffOp, 0, len(rev))
	for k := len(rev) - 1; k >= 0; k-- {
		out = append(out, rev[k])
	}
	return out
}

func diffTable(a, b []string) []int {
	n, m := len(a), len(b)
	width := m + 1
	dp := make([]int, (n+1)*width)
	at := func(i, j int) int { return dp[i*width+j] }
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i*width+j] = at(i-1, j-1) + 1
				continue
			}
			if at(i-1, j) >= at(i, j-1) {
				dp[i*width+j] = at(i-1, j)
			} else {
				dp[i*width+j] = at(i, j-1)
			}
		}
	}
	return dp
}

func diffBacktrack(a, b []string, dp []int) []diffOp {
	n, m := len(a), len(b)
	width := m + 1
	at := func(i, j int) int { return dp[i*width+j] }
	var rev []diffOp
	i, j := n, m
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			rev = append(rev, diffOp{kind: ' ', a: a[i-1]})
			i--
			j--
			continue
		}
		if at(i-1, j) > at(i, j-1) {
			rev = append(rev, diffOp{kind: '-', a: a[i-1]})
			i--
			continue
		}
		rev = append(rev, diffOp{kind: '+', b: b[j-1]})
		j--
	}
	for ; i > 0; i-- {
		rev = append(rev, diffOp{kind: '-', a: a[i-1]})
	}
	for ; j > 0; j-- {
		rev = append(rev, diffOp{kind: '+', b: b[j-1]})
	}
	return rev
}

func deltaLines(edits []wireEdit, s, e int) int {
	d := 0
	for _, ed := range edits {
		if ed.StartLine >= s && ed.EndLine <= e {
			d += newLines(ed.Replacement) - (ed.EndLine - ed.StartLine + 1)
		}
	}
	return d
}

func mergeSpans(spans []lineSpan) []lineSpan {
	if len(spans) == 0 {
		return nil
	}
	out := spans[:1]
	for _, sp := range spans[1:] {
		last := &out[len(out)-1]
		if sp.s-last.e-1 <= 2*diffContext {
			if sp.e > last.e {
				last.e = sp.e
			}
			continue
		}
		out = append(out, sp)
	}
	return out
}

func clamp(v, hi int) int {
	if v < 1 {
		return 1
	}
	if v > hi {
		return hi
	}
	return v
}
