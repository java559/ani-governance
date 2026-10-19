package buf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// managedRoot is one output directory a template writes, as a path relative to the module root.
type managedRoot struct {
	dir  string
	name string
}

// deriveManagedRoots turns every template's out: values into module-relative output roots and
// refuses anything that would write outside the repository.
func deriveManagedRoots(root, apiPath string, templates []*genTemplate) ([]managedRoot, error) {
	seen := map[string]bool{}
	var out []managedRoot
	for _, t := range templates {
		for _, o := range t.Outs {
			// Every out: is written relative to the api directory, including the ../ ones that
			// land in pkg/localdeps or the embedded assets.
			abs := filepath.Clean(filepath.Join(apiPath, o))
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return nil, fmt.Errorf("%s writes outside the module root: %s", t.Name, o)
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				out = append(out, managedRoot{dir: rel, name: t.Name})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].dir < out[j].dir })
	return out, nil
}

// finalizeScriptRel is the repository's own OpenAPI post-processing step. It belongs to
// generation, not to a follow-up command: scripts/finalize-aksk-openapi.py removes one synthetic
// response and refuses to run twice, so the only safe place for it is inside the staged run,
// before anything is written back.
const finalizeScriptRel = "scripts/finalize-aksk-openapi.py"

// stagingInputs lists what the staged copy needs: the module files, the two source trees the
// templates read and write, both scripts the chain runs, and every managed root outside those
// trees that already holds hand-written files.
func stagingInputs() []string {
	return []string{"go.mod", "go.sum", "api", "pkg", "scripts/build-redact-plugin.sh", finalizeScriptRel}
}

// stageAndGenerate runs the accepted template walk against a throwaway copy of the inputs,
// post-processes the OpenAPI document there, and only then brings managed files back. The point is
// that clean: true in api/buf.gen.yaml can clear a directory all it likes: it clears the copy,
// never api/gen/go or the output roots that hold hand-written runtime files, unless generation
// and its post-processing have both succeeded in full.
//
// Three different trees are in play and are never conflated: the snapshot taken when generation
// starts (the only baseline the comparison uses), the staged output, and the working tree as it is
// again right before anything is written back.
func stageAndGenerate(ctx context.Context, root string, templates []*genTemplate, roots []managedRoot) error {
	// The validator inventory is read once, before generation, and it is mandatory: without it the
	// chain cannot tell an approved artifact from a surprise.
	scope, err := readPGVScope(root)
	if err != nil {
		return err
	}

	snap, err := snapshotRoots(root, roots)
	if err != nil {
		return err
	}

	stage, err := os.MkdirTemp("", "gow-api-stage-")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(stage); err != nil {
			fmt.Fprintf(os.Stderr, "staging cleanup failed: %v\n", err)
		}
	}()

	for _, in := range stagingInputs() {
		if err := copyTree(filepath.Join(root, in), filepath.Join(stage, in)); err != nil {
			return fmt.Errorf("staging %s: %w", in, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(stage, "tools", "bin"), 0o755); err != nil {
		return err
	}
	// Every managed output root has to exist in the copy with its current content, including the
	// ones outside api/ and pkg/. The openapi template writes into
	// app/admin/service/cmd/server/assets, a directory that also holds the hand-written
	// go:embed file assets.go: without this the comparison would call that hand-written file
	// "lost" whenever the chain did not actually touch it.
	for _, m := range roots {
		if strings.HasPrefix(m.dir, "api"+string(os.PathSeparator)) || strings.HasPrefix(m.dir, "pkg"+string(os.PathSeparator)) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, m.dir)); err == nil {
			if err := copyTree(filepath.Join(root, m.dir), filepath.Join(stage, m.dir)); err != nil {
				return fmt.Errorf("staging %s: %w", m.dir, err)
			}
		}
	}

	plugin := filepath.Join(root, "tools", "bin", "protoc-gen-go-redact")
	if _, err := os.Stat(plugin); err == nil {
		if err := copyFile(plugin, filepath.Join(stage, "tools", "bin", "protoc-gen-go-redact")); err != nil {
			return fmt.Errorf("staging protoc-gen-go-redact: %w", err)
		}
	}

	// A full chain may not inherit any of its own output: carrying the previous artifacts into the
	// staged copy would let a run that silently stopped producing one pass it off as a rebuild. A
	// partial run has no such claim on the whole set, so it keeps the seeded copy and is judged only
	// on what it actually writes.
	fullChain := len(templates) == len(activeGenConfigs)
	if fullChain {
		if err := sweepGenerated(stage, roots); err != nil {
			return err
		}
	}

	stageAPI := filepath.Join(stage, "api")
	fmt.Printf("generating into staging copy %s (%d templates, %d managed output roots)\n", stage, len(templates), len(roots))
	for _, t := range templates {
		fmt.Printf("Using template file: %s\n", t.Path)
		if err := runBufGenerateIn(ctx, stageAPI, t.Name); err != nil {
			return err
		}
	}

	if err := finalizeOpenAPI(ctx, stage); err != nil {
		return err
	}

	return syncBack(root, stage, roots, snap, scope, fullChain)
}

// finalizeOpenAPI runs the repository's existing OpenAPI post-processing inside the staged copy,
// so a document with a synthetic response still in it can never be written back and no follow-up
// command is needed to make the tree final. The script is self-locating relative to its own parent
// directory, which is why running the staged copy is enough; its rules, plugin and paths are
// unchanged.
func finalizeOpenAPI(ctx context.Context, stage string) error {
	script := filepath.Join(stage, filepath.FromSlash(finalizeScriptRel))
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("staged %s missing: %w", finalizeScriptRel, err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		return fmt.Errorf("python3 is required to run %s: %w", finalizeScriptRel, err)
	}
	cmd := exec.CommandContext(ctx, python, script)
	cmd.Dir = stage
	cmd.Env = os.Environ()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", finalizeScriptRel, err)
	}
	fmt.Printf("post-processed the OpenAPI document with %s\n", finalizeScriptRel)
	return nil
}

// generatedSuffixes are the file shapes this repository's plugins can emit. Everything else that
// lives under an output root is hand-written - the redact takeover package and the go:embed assets
// directory both mix the two - and is never a write-back candidate.
var generatedSuffixes = []string{".pb.go", ".pb.validate.go", ".pb.redact.go"}

// generatedExactPaths names the non-Go artifact of the chain, matched module-relative so the rule
// cannot widen to the hand-written files that share its directory.
var generatedExactPaths = map[string]bool{
	"app/admin/service/cmd/server/assets/openapi.yaml": true,
}

func isGeneratedArtifact(rel string) bool {
	if generatedExactPaths[rel] {
		return true
	}
	base := path.Base(rel)
	for _, s := range generatedSuffixes {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

func isValidatorArtifact(rel string) bool {
	return strings.HasSuffix(path.Base(rel), ".pb.validate.go")
}

// snapshotRoots records the working tree's content for every managed root at the moment generation
// begins. That snapshot, and not the live tree, is the baseline for deciding what generation
// produced; the live tree is consulted again only to refuse clobbering a concurrent edit.
func snapshotRoots(root string, roots []managedRoot) (map[string]map[string]string, error) {
	snap := make(map[string]map[string]string, len(roots))
	for _, m := range roots {
		digests, err := treeDigests(filepath.Join(root, filepath.FromSlash(m.dir)))
		if err != nil {
			return nil, err
		}
		snap[m.dir] = digests
	}
	return snap, nil
}

// sweepGenerated deletes every generated-class file from the staged copy of the managed roots, so
// what the write-back finds there afterwards is what this run actually rebuilt. The hand-written
// files that share those directories are deliberately left in place: they are the reason the seed
// exists at all, and removing them would turn them into phantom losses.
func sweepGenerated(stage string, roots []managedRoot) error {
	for _, m := range roots {
		dir := filepath.Join(stage, filepath.FromSlash(m.dir))
		var doomed []string
		if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			if isGeneratedArtifact(path.Join(m.dir, filepath.ToSlash(rel))) {
				doomed = append(doomed, p)
			}
			return nil
		}); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("sweeping the staged copy of %s: %w", m.dir, err)
		}
		for _, p := range doomed {
			if err := os.Remove(p); err != nil {
				return fmt.Errorf("removing %s from the staged copy: %w", p, err)
			}
		}
	}
	return nil
}

// syncBack classifies the staged output against the start snapshot, refuses anything it cannot
// account for, and only then writes. Two sets are kept apart: the files it may write (generated
// artifacts whose staged content differs from the snapshot) and everything else under an output
// root, which is left exactly as the user has it.
//
// fullChain says whether this run is the whole accepted template set; only then may the run be held
// to producing every approved validator, because a partial slice legitimately produces a subset.
func syncBack(root, stage string, roots []managedRoot, snap map[string]map[string]string, scope *pgvScope, fullChain bool) error {
	var write []pendingWrite
	var problems []string
	var carried []string
	stagedCount := 0
	stagedValidators := map[string]bool{}
	treeValidators := map[string]bool{}

	for _, m := range roots {
		staged, err := treeDigests(filepath.Join(stage, filepath.FromSlash(m.dir)))
		if err != nil {
			return err
		}
		before := snap[m.dir]
		if before == nil {
			before = map[string]string{}
		}
		stagedCount += len(staged)

		for _, rel := range sortedKeys(staged) {
			full := path.Join(m.dir, rel)
			if !isGeneratedArtifact(full) {
				// Hand-written: never a write-back target. Generation changing one means a template
				// writes where it has no business writing, so that is reported rather than carried.
				if old, ok := before[rel]; ok && old != staged[rel] {
					problems = append(problems, fmt.Sprintf("%s: generation rewrote the hand-written file %s", m.dir, rel))
					continue
				}
				if _, ok := before[rel]; !ok {
					problems = append(problems, fmt.Sprintf("%s: generation created the unmanaged file %s", m.dir, rel))
					continue
				}
				carried = append(carried, full)
				continue
			}
			if isValidatorArtifact(full) {
				stagedValidators[full] = true
				if !scope.approved[full] {
					problems = append(problems, fmt.Sprintf("%s: validator outside %s", full, scope.file))
					continue
				}
			}
			if old, ok := before[rel]; ok && old == staged[rel] {
				continue // unchanged by generation
			}
			write = append(write, pendingWrite{root: m.dir, rel: rel, full: full, digest: staged[rel], replaced: hasKey(before, rel)})
		}

		for _, rel := range sortedKeys(before) {
			full := path.Join(m.dir, rel)
			if !isGeneratedArtifact(full) {
				continue // a hand-written file generation does not emit is not "lost"
			}
			if isValidatorArtifact(full) {
				treeValidators[full] = true
			}
			if _, ok := staged[rel]; !ok {
				problems = append(problems, fmt.Sprintf("%s: no longer produced by the chain", full))
			}
		}
	}

	if fullChain {
		// The whole accepted chain owes the repository exactly the approved validator set: an
		// approved artifact it did not produce, and any validator nobody approved, are scope changes
		// and never something to write back quietly. A partial slice is held to its own output only,
		// so it is not required to produce the full list.
		//
		// The list says what this run must generate, not what had to exist before it: an approved
		// validator that is missing from the tree is exactly the artifact this run is expected to
		// rebuild, so it is restored rather than refused.
		for _, v := range sortedKeys(scope.approved) {
			if !stagedValidators[v] {
				problems = append(problems, fmt.Sprintf("%s: approved in %s but not produced by this chain", v, scope.file))
			}
		}
		for _, v := range sortedKeys(treeValidators) {
			if !scope.approved[v] {
				problems = append(problems, fmt.Sprintf("%s: already in the tree but outside %s", v, scope.file))
			}
		}
	}

	// The tree as it is now, not as it was at the start: an edit made while generation ran must
	// survive, so a write whose target moved is a conflict rather than an overwrite.
	var conflicts []string
	for _, w := range write {
		dst := filepath.Join(root, filepath.FromSlash(w.full))
		now, ok, err := liveDigest(dst)
		if err != nil {
			// A symlink or a directory sitting on a destination path is invisible to a digest walk,
			// so this is where it is first seen - as a problem to report, before any write.
			conflicts = append(conflicts, fmt.Sprintf("%s: %v", w.full, err))
			continue
		}
		if !ok {
			if _, was := snap[w.root][w.rel]; was {
				conflicts = append(conflicts, fmt.Sprintf("%s: removed after the snapshot was taken", w.full))
			}
			continue
		}
		if want, was := snap[w.root][w.rel]; !was || want != now {
			conflicts = append(conflicts, fmt.Sprintf("%s: modified after the snapshot was taken", w.full))
		}
	}
	// A file generation left alone is not ours to touch, but the user editing it mid-run means the
	// snapshot they were compared against is stale, so it is reported instead of silently ignored.
	for _, m := range roots {
		for _, rel := range sortedKeys(snap[m.dir]) {
			full := path.Join(m.dir, rel)
			if isGeneratedArtifact(full) {
				continue
			}
			now, ok, err := liveDigest(filepath.Join(root, filepath.FromSlash(full)))
			if err != nil {
				return err
			}
			if !ok || now == snap[m.dir][rel] {
				continue
			}
			fmt.Printf("  preserved hand-written edit: %s\n", full)
		}
	}

	problems = append(problems, conflicts...)
	if len(problems) > 0 {
		sort.Strings(problems)
		for _, p := range problems {
			_, _ = fmt.Fprintf(os.Stderr, "write-back refused: %s\n", p)
		}
		return fmt.Errorf("write-back refused with %d problem(s); nothing was written and every current file was kept: %s",
			len(problems), strings.Join(problems, "; "))
	}

	if err := applyWrites(root, stage, write); err != nil {
		return err
	}

	detail := make([]string, 0, len(roots))
	for _, m := range roots {
		detail = append(detail, fmt.Sprintf("%s:%d", m.dir, len(snap[m.dir])))
	}
	sort.Strings(detail)
	fmt.Printf("staged generation ok: %d to write, %d unchanged hand-written kept, %d staged files under %d managed roots [%s]\n",
		len(write), len(carried), stagedCount, len(roots), strings.Join(detail, " "))
	for _, w := range write {
		if w.replaced {
			fmt.Printf("  updated: %s\n", w.full)
		} else {
			fmt.Printf("  new:     %s\n", w.full)
		}
	}
	return nil
}

// pendingWrite is one file the write-back has decided it may replace or create.
type pendingWrite struct {
	root     string // managed output root, module-relative
	rel      string // path inside that root
	full     string // module-relative destination
	digest   string // digest of the staged content being written
	replaced bool   // the snapshot held this file, so a user file is being replaced
}

// applyWrites writes the prepared set. Every destination is checked before the first byte moves,
// each file being replaced is backed up outside the tree, and a failure part-way through restores
// what this call replaced and removes what this call added. A rollback that itself fails is
// reported on its own with the backups left in place: the tree is never claimed to be unchanged.
//
// This is a bounded undo for one write-back. It makes no atomicity promise for the whole directory
// against a power loss or a kill between two renames, and it is not a general transaction layer.
func applyWrites(root, stage string, write []pendingWrite) error {
	if len(write) == 0 {
		return nil
	}
	backups, err := os.MkdirTemp("", "gow-api-backup-")
	if err != nil {
		return err
	}

	// Phase one inspects every target, so a collision fails before anything has been touched.
	var createdDirs []string
	for _, w := range write {
		dst := filepath.Join(root, filepath.FromSlash(w.full))
		info, err := os.Lstat(dst)
		switch {
		case err == nil && !info.Mode().IsRegular():
			// A directory or symlink on a destination path is invisible to a digest walk, so this
			// is the only place that can catch it before a rename fails on top of it.
			os.RemoveAll(backups)
			return fmt.Errorf("%s: destination exists as %q, not a regular file; nothing was written", w.full, info.Mode().Type())
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			os.RemoveAll(backups)
			return fmt.Errorf("%s: destination cannot be inspected: %w", w.full, err)
		}
		dirs, err := ensureParents(root, filepath.Dir(dst))
		if err != nil {
			os.RemoveAll(backups)
			return fmt.Errorf("%s: cannot prepare its directory: %w", w.full, err)
		}
		createdDirs = append(createdDirs, dirs...)
	}

	var done []appliedWrite
	for _, w := range write {
		dst := filepath.Join(root, filepath.FromSlash(w.full))
		backup := ""
		if w.replaced {
			backup = filepath.Join(backups, filepath.FromSlash(w.full))
			if err := copyFile(dst, backup); err != nil {
				return rollbackWrites(root, &w, "backing up", done, createdDirs, backups, err)
			}
		}
		if err := copyFile(filepath.Join(stage, filepath.FromSlash(w.full)), dst); err != nil {
			return rollbackWrites(root, &w, "writing", done, createdDirs, backups, err)
		}
		now, _, err := liveDigest(dst)
		if err != nil || now != w.digest {
			return rollbackWrites(root, &w, "verifying", done, createdDirs, backups,
				fmt.Errorf("the content on disk is %s, the staged content was %s (%v)", now, w.digest, err))
		}
		done = append(done, appliedWrite{w: w, backup: backup})
	}

	for i := len(createdDirs) - 1; i >= 0; i-- {
		_ = os.Remove(createdDirs[i]) // only a directory still empty is removed; a busy one stays
	}
	if err := os.RemoveAll(backups); err != nil {
		fmt.Fprintf(os.Stderr, "write-back completed but its backups stayed in %s: %v\n", backups, err)
	}
	return nil
}

type appliedWrite struct {
	w      pendingWrite
	backup string // "" when this write created the file
}

// rollbackWrites undoes exactly the writes that already happened, newest first, and leaves any file
// whose content is no longer what this run wrote: that content is the user's, not ours to replace.
func rollbackWrites(root string, failed *pendingWrite, phase string, done []appliedWrite, createdDirs []string, backups string, cause error) error {
	var restored, undone, kept, broken []string
	for i := len(done) - 1; i >= 0; i-- {
		a := done[i]
		dst := filepath.Join(root, filepath.FromSlash(a.w.full))
		now, exists, err := liveDigest(dst)
		switch {
		case err != nil:
			broken = append(broken, fmt.Sprintf("%s: %v", a.w.full, err))
			continue
		case !exists: // removed after this run wrote it: there is nothing left to take back
			continue
		case now != a.w.digest:
			kept = append(kept, a.w.full)
			continue
		}
		if a.backup == "" {
			err = os.Remove(dst)
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
			if err == nil {
				undone = append(undone, a.w.full)
			}
		} else {
			err = copyFile(a.backup, dst)
			if err == nil {
				restored = append(restored, a.w.full)
			}
		}
		if err != nil {
			broken = append(broken, fmt.Sprintf("%s: %v", a.w.full, err))
		}
	}
	for i := len(createdDirs) - 1; i >= 0; i-- {
		_ = os.Remove(createdDirs[i]) // a directory that is not empty any more stays
	}

	msg := fmt.Sprintf("write-back failed while %s %s: %v", phase, failed.full, cause)
	if len(broken) > 0 {
		sort.Strings(broken)
		msg += fmt.Sprintf("; THE ROLLBACK IS INCOMPLETE AND THE TREE IS NOT RESTORED: %s. The backups are preserved in %s",
			strings.Join(broken, ", "), backups)
	} else if err := os.RemoveAll(backups); err != nil {
		msg += fmt.Sprintf("; the rollback itself succeeded but the backup directory stayed in %s: %v", backups, err)
	}
	if len(restored) > 0 {
		sort.Strings(restored)
		msg += fmt.Sprintf("; this run restored %d replaced file(s) [%s]", len(restored), strings.Join(restored, ", "))
	}
	if len(undone) > 0 {
		sort.Strings(undone)
		msg += fmt.Sprintf("; this run removed %d file(s) it had added [%s]", len(undone), strings.Join(undone, ", "))
	}
	if len(kept) > 0 {
		sort.Strings(kept)
		msg += fmt.Sprintf("; %d file(s) were left untouched because they changed after this run wrote them: [%s]",
			len(kept), strings.Join(kept, ", "))
	}
	return errors.New(msg)
}

// ensureParents creates the directories a write needs and returns only the ones it created, so a
// rollback can take them away again.
func ensureParents(root, dir string) ([]string, error) {
	var missing, created []string
	for cur := dir; cur != root && strings.HasPrefix(cur, root+string(filepath.Separator)); cur = filepath.Dir(cur) {
		_, err := os.Lstat(cur)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, cur)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0o755); err != nil {
			for _, c := range created {
				_ = os.Remove(c)
			}
			return nil, err
		}
		created = append(created, missing[i])
	}
	return created, nil
}

// pgvScope is the repository's committed validator inventory, read once before generation starts.
type pgvScope struct {
	file     string // module-relative name, used in the messages
	approved map[string]bool
}

// readPGVScope loads tools/config/pgv-scope.json and refuses to continue on a missing, corrupt,
// duplicated or empty inventory: without it the chain cannot tell an approved artifact from a
// surprise, and "no list" must never mean "anything goes".
func readPGVScope(root string) (*pgvScope, error) {
	const scopeRel = "tools/config/pgv-scope.json"
	file := filepath.Join(root, filepath.FromSlash(scopeRel))
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", scopeRel, err)
	}
	var doc struct {
		ValidatorCount *int `json:"validator_count"`
		Entries        []struct {
			Validator string `json:"validator"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", scopeRel, err)
	}
	approved := make(map[string]bool, len(doc.Entries))
	for _, e := range doc.Entries {
		v := path.Clean(filepath.ToSlash(e.Validator))
		if v == "" || v == "." || strings.HasPrefix(v, "../") || filepath.IsAbs(e.Validator) {
			return nil, fmt.Errorf("%s: validator path %q is not a repository-relative file", scopeRel, e.Validator)
		}
		if approved[v] {
			return nil, fmt.Errorf("%s: validator %q is listed twice", scopeRel, v)
		}
		approved[v] = true
	}
	if len(approved) == 0 {
		return nil, fmt.Errorf("%s lists no validators; the generation entry refuses to run without an approved scope", scopeRel)
	}
	if doc.ValidatorCount != nil && *doc.ValidatorCount != len(approved) {
		return nil, fmt.Errorf("%s declares validator_count %d but carries %d entries", scopeRel, *doc.ValidatorCount, len(approved))
	}
	return &pgvScope{file: scopeRel, approved: approved}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func hasKey[V any](m map[string]V, k string) bool {
	_, ok := m[k]
	return ok
}

// liveDigest reads the tree as it is right now, which is a different thing from the start snapshot.
func liveDigest(dst string) (string, bool, error) {
	info, err := os.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, errors.New("exists as a directory, symlink or other non-regular file")
	}
	sum, err := fileSum(dst)
	if err != nil {
		return "", false, err
	}
	return sum, true, nil
}

func treeDigests(dir string) (map[string]string, error) {
	out := map[string]string{}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sum, err := fileSum(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = sum
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking %s: %w", dir, err)
	}
	return out, nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".copy-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return copyFile(src, dst)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(path, target)
	})
}
