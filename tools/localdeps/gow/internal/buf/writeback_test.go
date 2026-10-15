package buf

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeBackFixture keeps the three trees apart that F2 says must never be conflated: the working
// tree when generation starts, the staged copy the templates write into, and the working tree again
// at the moment of the write-back. A test edits the tree between begin() and sync() to stand in for
// a user editing files while the generator runs.
type writeBackFixture struct {
	t          *testing.T
	root       string
	stage      string
	snap       map[string]map[string]string
	roots      []managedRoot
	scope      *pgvScope
	scopeCount int
}

func newWriteBackFixture(t *testing.T, approved []string, managedDirs ...string) *writeBackFixture {
	t.Helper()
	f := &writeBackFixture{t: t, root: t.TempDir(), stage: t.TempDir()}
	for _, d := range managedDirs {
		f.roots = append(f.roots, managedRoot{dir: d, name: "buf.gen.yaml"})
	}
	f.writeScope(approved)
	return f
}

// writeScope installs tools/config/pgv-scope.json and reads it back through the same strict loader the
// generation entry uses. A fixture that does not care about validators still needs one entry, because
// an empty inventory is a hard failure by design.
func (f *writeBackFixture) writeScope(validators []string) *writeBackFixture {
	f.t.Helper()
	if len(validators) == 0 {
		validators = []string{"unused/approved.pb.validate.go"}
	}
	entries := make([]map[string]string, 0, len(validators))
	for _, v := range validators {
		entries = append(entries, map[string]string{"validator": v})
	}
	raw, err := json.Marshal(map[string]any{"validator_count": len(validators), "entries": entries})
	if err != nil {
		f.t.Fatal(err)
	}
	dir := filepath.Join(f.root, "tools", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pgv-scope.json"), raw, 0o644); err != nil {
		f.t.Fatal(err)
	}
	scope, err := readPGVScope(f.root)
	if err != nil {
		f.t.Fatalf("readPGVScope on a valid inventory: %v", err)
	}
	f.scope, f.scopeCount = scope, len(validators)
	return f
}

// write puts content in the working tree before generation starts.
func (f *writeBackFixture) write(rel, content string) *writeBackFixture {
	f.t.Helper()
	writeAt(f.t, f.root, rel, content)
	return f
}

// begin takes the start snapshot and copies the managed roots into the stage. fullChain mirrors
// stageAndGenerate: only a run over the whole accepted template set sweeps the generated classes, so
// that such a run has to make generation produce what it claims to have rebuilt.
func (f *writeBackFixture) begin(fullChain bool) *writeBackFixture {
	f.t.Helper()
	snap, err := snapshotRoots(f.root, f.roots)
	if err != nil {
		f.t.Fatal(err)
	}
	f.snap = snap
	for _, m := range f.roots {
		if _, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(m.dir))); err == nil {
			if err := copyTree(filepath.Join(f.root, filepath.FromSlash(m.dir)), filepath.Join(f.stage, filepath.FromSlash(m.dir))); err != nil {
				f.t.Fatal(err)
			}
		}
	}
	if fullChain {
		if err := sweepGenerated(f.stage, f.roots); err != nil {
			f.t.Fatal(err)
		}
	}
	return f
}

// generate is what the templates leave behind in the staged copy.
func (f *writeBackFixture) generate(rel, content string) *writeBackFixture {
	f.t.Helper()
	writeAt(f.t, f.stage, rel, content)
	return f
}

// editTree changes the working tree after the snapshot was taken.
func (f *writeBackFixture) editTree(rel, content string) *writeBackFixture {
	f.t.Helper()
	writeAt(f.t, f.root, rel, content)
	return f
}

func (f *writeBackFixture) mkdirInTree(rel string) *writeBackFixture {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.root, filepath.FromSlash(rel)), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return f
}

func (f *writeBackFixture) chmodTree(rel string, mode os.FileMode) *writeBackFixture {
	f.t.Helper()
	if err := os.Chmod(filepath.Join(f.root, filepath.FromSlash(rel)), mode); err != nil {
		f.t.Fatal(err)
	}
	return f
}

func (f *writeBackFixture) sync(fullChain bool) error {
	f.t.Helper()
	return syncBack(f.root, f.stage, f.roots, f.snap, f.scope, fullChain)
}

func (f *writeBackFixture) content(rel string) string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err != nil {
		f.t.Fatalf("reading %s: %v", rel, err)
	}
	return string(raw)
}

func (f *writeBackFixture) exists(rel string) bool {
	f.t.Helper()
	_, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(rel)))
	if err == nil {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		f.t.Fatalf("inspecting %s: %v", rel, err)
	}
	return false
}

func (f *writeBackFixture) tree(rel string) string {
	f.t.Helper()
	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func mustParse(t *testing.T, file string) *genTemplate {
	t.Helper()
	g, err := parseGenTemplate(file)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	return g
}

func writeAt(t *testing.T, base, rel, content string) {
	t.Helper()
	dst := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requireRefused asserts the write-back refused and that the message names what the user is asked to
// look at.
func requireRefused(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected the write-back to refuse, it returned nil")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not mention %q: %v", w, err)
		}
	}
}

// TestWriteBackKeepsAHandWrittenFileEditedDuringGeneration is F2's first regression: the redact
// takeover package is a managed output root that also holds hand-written Go. An edit made to that
// hand-written file while generation runs must survive, and the run must still write what generation
// actually produced.
func TestWriteBackKeepsAHandWrittenFileEditedDuringGeneration(t *testing.T) {
	const root = "pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact"
	hand := root + "/redact/v1/interface.go"
	f := newWriteBackFixture(t, nil, root)
	f.write(hand, "original handwritten code\n")
	f.write(root+"/redact/v1/redact.pb.go", "old generated\n")
	f.begin(false)
	f.generate(root+"/redact/v1/redact.pb.go", "new generated\n")
	f.editTree(hand, "user edit made while generation runs\n")

	if err := f.sync(false); err != nil {
		t.Fatalf("a hand-written edit must not fail the write-back: %v", err)
	}
	if got := f.content(hand); got != "user edit made while generation runs\n" {
		t.Errorf("the tool overwrote a hand-written file: %q", got)
	}
	if got := f.content(root + "/redact/v1/redact.pb.go"); got != "new generated\n" {
		t.Errorf("generation output was not written back either: %q", got)
	}
}

// TestWriteBackRefusesAGeneratedFileEditedDuringGeneration is F2's second regression: when the file
// the user moved is one this run is about to replace, that is a conflict. The user's content wins and
// nothing is written.
func TestWriteBackRefusesAGeneratedFileEditedDuringGeneration(t *testing.T) {
	const managed = "api/gen/go"
	f := newWriteBackFixture(t, nil, managed)
	f.write(managed+"/a.pb.go", "old-a\n")
	f.write(managed+"/b.pb.go", "old-b\n")
	f.begin(false)
	f.generate(managed+"/a.pb.go", "generated-a\n")
	f.generate(managed+"/b.pb.go", "generated-b\n")
	f.editTree(managed+"/b.pb.go", "the user's own edit\n")

	err := f.sync(false)
	requireRefused(t, err, managed+"/b.pb.go", "nothing was written")
	if got := f.content(managed + "/b.pb.go"); got != "the user's own edit\n" {
		t.Errorf("the concurrent edit was overwritten: %q", got)
	}
	if got := f.content(managed + "/a.pb.go"); got != "old-a\n" {
		t.Errorf("a refusal must not write anything at all, a.pb.go became %q", got)
	}
}

// TestWriteBackRefusesADestinationCollisionBeforeWritingAnything is F3's first case: a directory on
// an output path is invisible to a digest walk, so it used to surface as a failed rename after other
// files had already been replaced. It must now be refused before the first byte moves.
func TestWriteBackRefusesADestinationCollisionBeforeWritingAnything(t *testing.T) {
	const managed = "api/gen/go"
	f := newWriteBackFixture(t, nil, managed)
	f.write(managed+"/a.pb.go", "old-a\n")
	f.begin(false)
	f.generate(managed+"/a.pb.go", "generated-a\n")
	f.generate(managed+"/z.pb.go", "generated-z\n")
	f.mkdirInTree(managed + "/z.pb.go")

	err := f.sync(false)
	requireRefused(t, err, managed+"/z.pb.go")
	if got := f.content(managed + "/a.pb.go"); got != "old-a\n" {
		t.Errorf("a.pb.go was replaced even though the write-back failed on z.pb.go: %q", got)
	}
	if info, err := os.Lstat(f.tree(managed + "/z.pb.go")); err != nil || !info.IsDir() {
		t.Errorf("the user's directory on the destination path was disturbed: %v %v", info, err)
	}
}

// TestWriteBackUndoesAPartialWrite is F3's second case: once the first output is safely in place a
// later one can still fail on the filesystem, and the run has to take back what it already did.
func TestWriteBackUndoesAPartialWrite(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires an unprivileged user: root bypasses the chmod 0555 failure this test simulates")
	}
	const managed = "api/gen/go"
	f := newWriteBackFixture(t, nil, managed)
	f.write(managed+"/a.pb.go", "old-a\n")
	f.mkdirInTree(managed + "/locked")
	f.begin(false)
	f.generate(managed+"/a.pb.go", "generated-a\n")
	f.generate(managed+"/locked/b.pb.go", "generated-b\n")
	// The first destination is writable and the second is not, so the run gets one file in and then
	// has to undo it.
	f.chmodTree(managed+"/locked", 0o555)
	t.Cleanup(func() { _ = os.Chmod(f.tree(managed+"/locked"), 0o755) })

	err := f.sync(false)
	if err == nil {
		t.Fatal("expected the second write to fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "restored") || !strings.Contains(msg, managed+"/a.pb.go") {
		t.Fatalf("the report must say what was taken back: %v", err)
	}
	if got := f.content(managed + "/a.pb.go"); got != "old-a\n" {
		t.Errorf("the rollback did not restore the replaced file: %q", got)
	}
	if f.exists(managed + "/locked/b.pb.go") {
		t.Error("a file the failed run never wrote must not appear")
	}
}

// TestRollbackWritesKeepsUserContentThatArrivedAfterTheWrite pins the limit of the rollback: a file
// whose content is no longer what this run wrote belongs to the user and is never overwritten
// again, while the files this run did write are restored or removed.
func TestRollbackWritesKeepsUserContentThatArrivedAfterTheWrite(t *testing.T) {
	const managed = "api/gen/go"
	root, backups := t.TempDir(), t.TempDir()

	digestOf := func(rel string) string {
		sum, err := fileSum(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}

	writeAt(t, root, managed+"/added.pb.go", "what this run wrote\n")
	writeAt(t, root, managed+"/restored.pb.go", "what this run wrote\n")
	writeAt(t, backups, managed+"/restored.pb.go", "the pre-run content\n")
	writeAt(t, root, managed+"/kept.pb.go", "the user saved over this run\n")

	done := []appliedWrite{
		{w: pendingWrite{root: managed, rel: "added.pb.go", full: managed + "/added.pb.go", digest: digestOf(managed + "/added.pb.go")}},
		{w: pendingWrite{root: managed, rel: "restored.pb.go", full: managed + "/restored.pb.go", digest: digestOf(managed + "/restored.pb.go"), replaced: true},
			backup: filepath.Join(backups, filepath.FromSlash(managed+"/restored.pb.go"))},
		{w: pendingWrite{root: managed, rel: "kept.pb.go", full: managed + "/kept.pb.go", digest: "not-what-is-on-disk", replaced: true},
			backup: filepath.Join(backups, filepath.FromSlash(managed+"/kept.pb.go"))},
	}

	err := rollbackWrites(root, &done[2].w, "writing", done, nil, backups, errors.New("simulated failure on the next file"))
	if err == nil {
		t.Fatal("the rollback has to report what it did")
	}
	want := []string{"simulated failure", "restored 1 replaced file", "removed 1 file(s) it had added", "left untouched because they changed after this run wrote them"}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the report is missing %q: %v", w, err)
		}
	}
	if f := f2Content(t, root, managed+"/kept.pb.go"); f != "the user saved over this run\n" {
		t.Errorf("the user's newer content was overwritten by the rollback: %q", f)
	}
	if f := f2Content(t, root, managed+"/restored.pb.go"); f != "the pre-run content\n" {
		t.Errorf("the backup was not restored: %q", f)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(managed+"/added.pb.go"))); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file this run added was left behind: %v", err)
	}
}

func f2Content(t *testing.T, base, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestWriteBackLeavesUnchangedOutputAlone proves the write set really is only what generation
// produced: a file whose staged copy is byte-identical to the snapshot is not rewritten, so its
// inode age survives.
func TestWriteBackLeavesUnchangedOutputAlone(t *testing.T) {
	const managed = "api/gen/go"
	f := newWriteBackFixture(t, nil, managed)
	f.write(managed+"/same.pb.go", "unchanged\n")
	f.write(managed+"/moves.pb.go", "old\n")
	f.begin(false)
	f.generate(managed+"/same.pb.go", "unchanged\n")
	f.generate(managed+"/moves.pb.go", "new\n")

	old := time.Unix(946684800, 0)
	if err := os.Chtimes(f.tree(managed+"/same.pb.go"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := f.sync(false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.tree(managed + "/same.pb.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Errorf("an unchanged generated file was rewritten anyway: mtime %v", info.ModTime())
	}
	if got := f.content(managed + "/moves.pb.go"); got != "new\n" {
		t.Errorf("the changed file was not updated: %q", got)
	}
}

// TestWriteBackScopeRules covers F5: the approved inventory is mandatory, an out-of-list validator
// is refused whether it is new or already in the tree, and only the full chain is held to producing
// every approved artifact.
func TestWriteBackScopeRules(t *testing.T) {
	const managed = "api/gen/go"
	approved := managed + "/approved.pb.validate.go"

	t.Run("new out-of-scope validator is refused and nothing is written", func(t *testing.T) {
		f := newWriteBackFixture(t, []string{approved}, managed)
		f.write(managed+"/a.pb.go", "old\n")
		f.begin(false)
		f.generate(managed+"/a.pb.go", "new\n")
		f.generate(approved, "ok\n")
		f.generate(managed+"/sneak.pb.validate.go", "bad\n")
		err := f.sync(false)
		requireRefused(t, err, "sneak.pb.validate.go", "outside")
		if f.exists(managed + "/sneak.pb.validate.go") {
			t.Error("the unapproved validator was written")
		}
		if got := f.content(managed + "/a.pb.go"); got != "old\n" {
			t.Errorf("the earlier output was written even though the run refused: %q", got)
		}
	})

	t.Run("existing out-of-scope validator is refused even when generation rewrites it", func(t *testing.T) {
		f := newWriteBackFixture(t, []string{approved}, managed)
		f.write(approved, "approved\n")
		f.write(managed+"/sneak.pb.validate.go", "pre-existing unapproved validator\n")
		f.begin(false)
		f.generate(approved, "approved\n")
		f.generate(managed+"/sneak.pb.validate.go", "new unapproved content\n")
		err := f.sync(false)
		requireRefused(t, err, "sneak.pb.validate.go")
		if got := f.content(managed + "/sneak.pb.validate.go"); got != "pre-existing unapproved validator\n" {
			t.Errorf("the user's file was replaced by an out-of-scope validator: %q", got)
		}
	})

	t.Run("the legitimate set writes back", func(t *testing.T) {
		f := newWriteBackFixture(t, []string{approved}, managed)
		f.write(approved, "old\n")
		f.begin(false)
		f.generate(approved, "ok\n")
		if err := f.sync(false); err != nil {
			t.Fatalf("an approved validator must be writable: %v", err)
		}
		if got := f.content(approved); got != "ok\n" {
			t.Errorf("approved validator not updated: %q", got)
		}
	})

	t.Run("full chain must produce every approved artifact", func(t *testing.T) {
		second := managed + "/second.pb.validate.go"
		f := newWriteBackFixture(t, []string{approved, second}, managed)
		f.write(approved, "a\n")
		f.write(second, "b\n")
		f.begin(true)
		f.generate(approved, "a\n")
		f.generate(second, "changed\n")
		if err := f.sync(true); err != nil {
			t.Fatalf("the full chain with both approved artifacts: %v", err)
		}
		if got := f.content(second); got != "changed\n" {
			t.Errorf("the changed approved validator was not written back: %q", got)
		}

		dropped := newWriteBackFixture(t, []string{approved, second}, managed)
		dropped.write(approved, "a\n")
		dropped.write(second, "b\n")
		dropped.begin(true)
		dropped.generate(approved, "a\n")
		err := dropped.sync(true)
		requireRefused(t, err, second, "not produced")
		if got := dropped.content(approved); got != "a\n" {
			t.Errorf("a refused run must write nothing, approved became %q", got)
		}

		// The same result from a partial slice is legitimate: it never claimed the whole list, so it
		// is not required to produce every approved artifact and is judged only on what it writes.
		slice := newWriteBackFixture(t, []string{approved, second}, managed)
		slice.write(approved, "a\n")
		slice.write(second, "b\n")
		slice.begin(false)
		slice.generate(approved, "rewritten\n")
		if err := slice.sync(false); err != nil {
			t.Fatalf("a partial slice may not be required to produce every approved artifact: %v", err)
		}
		if got := slice.content(approved); got != "rewritten\n" {
			t.Errorf("the slice's own output was not written back: %q", got)
		}
		if got := slice.content(second); got != "b\n" {
			t.Errorf("a slice must not touch an artifact it did not generate: %q", got)
		}
		// The list says what the run must generate, not what had to exist before it. A missing
		// approved artifact is what this run is for, so it comes back rather than being refused.
		t.Run("one approved artifact missing before the run is rebuilt", func(t *testing.T) {
			f := newWriteBackFixture(t, []string{approved, second}, managed)
			f.write(approved, "a\n")
			f.begin(true)
			f.generate(approved, "a\n")
			f.generate(second, "rebuilt\n")
			if err := f.sync(true); err != nil {
				t.Fatalf("a full chain that rebuilds the one artifact the tree lacked must pass: %v", err)
			}
			if got := f.content(second); got != "rebuilt\n" {
				t.Errorf("the rebuilt validator was not written back: %q", got)
			}
			if got := f.content(approved); got != "a\n" {
				t.Errorf("an artifact generation did not change must be left alone: %q", got)
			}
		})

		t.Run("every approved artifact missing before the run is rebuilt", func(t *testing.T) {
			third := managed + "/third.pb.validate.go"
			f := newWriteBackFixture(t, []string{approved, second, third}, managed)
			f.write(managed+"/types.pb.go", "hand of the chain\n")
			f.begin(true)
			f.generate(managed+"/types.pb.go", "hand of the chain\n")
			f.generate(approved, "A\n")
			f.generate(second, "B\n")
			f.generate(third, "C\n")
			if err := f.sync(true); err != nil {
				t.Fatalf("an empty validator tree is the state a rebuild must recover from: %v", err)
			}
			for rel, want := range map[string]string{approved: "A\n", second: "B\n", third: "C\n"} {
				if got := f.content(rel); got != want {
					t.Errorf("%s was not restored: %q", rel, got)
				}
			}
		})
	})

	t.Run("a missing scope file is a hard failure", func(t *testing.T) {
		dir := t.TempDir()
		if _, err := readPGVScope(dir); err == nil {
			t.Fatal("no tools/config/pgv-scope.json must not mean no rule")
		} else if !strings.Contains(err.Error(), "pgv-scope.json") {
			t.Errorf("the message must name the file: %v", err)
		}
	})
}

// TestReadPGVScopeRejectsADamagedInventory covers the other F5 shapes: empty, corrupt, duplicated,
// or self-inconsistent.
func TestReadPGVScopeRejectsADamagedInventory(t *testing.T) {
	cases := map[string]string{
		"empty entries":         `{"validator_count":0,"entries":[]}`,
		"no entries key":        `{}`,
		"corrupt json":          `{"entries":[`,
		"count mismatch":        `{"validator_count":2,"entries":[{"validator":"api/gen/go/a.pb.validate.go"}]}`,
		"duplicate validator":   `{"validator_count":2,"entries":[{"validator":"api/gen/go/a.pb.validate.go"},{"validator":"api/gen/go/a.pb.validate.go"}]}`,
		"escaping validator":    `{"validator_count":1,"entries":[{"validator":"../elsewhere/a.pb.validate.go"}]}`,
		"absolute validator":    `{"validator_count":1,"entries":[{"validator":"/tmp/a.pb.validate.go"}]}`,
		"wrong json type":       `{"entries":{"validator":"api/gen/go/a.pb.validate.go"}}`,
		"empty validator value": `{"validator_count":1,"entries":[{"validator":""}]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "tools", "config"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "tools", "config", "pgv-scope.json"), []byte(doc), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readPGVScope(root); err == nil {
				t.Fatalf("%s was accepted: %s", name, doc)
			}
		})
	}

	t.Run("the real inventory is accepted and complete", func(t *testing.T) {
		scope, err := readPGVScope(repoRoot(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(scope.approved) != 105 {
			t.Errorf("the committed scope has %d validators, the accepted contract is 105", len(scope.approved))
		}
		for v := range scope.approved {
			if !strings.HasPrefix(v, "api/gen/go/") || !strings.HasSuffix(v, ".pb.validate.go") {
				t.Errorf("scope entry %q is not a validator under api/gen/go", v)
			}
		}
	})
}

// TestGeneratedArtifactClasses pins the line between what the chain may write back and the
// hand-written files that share its output roots. A new class has to be added on purpose, with the
// template that emits it.
func TestGeneratedArtifactClasses(t *testing.T) {
	generated := []string{
		"api/gen/go/admin/service/v1/i_user.pb.go",
		"api/gen/go/admin/service/v1/i_user_grpc.pb.go",
		"api/gen/go/admin/service/v1/i_user_http.pb.go",
		"api/gen/go/admin/service/v1/user_error_errors.pb.go",
		"api/gen/go/admin/service/v1/i_user.pb.validate.go",
		"api/gen/go/identity/service/v1/user.pb.redact.go",
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1/redact.pb.go",
		"pkg/localdeps/go-crud/api/gen/go/pagination/v1/pagination.pb.go",
		"app/admin/service/cmd/server/assets/openapi.yaml",
	}
	handWritten := []string{
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1/interface.go",
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1/stream.go",
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/main.go",
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/generator_test.go",
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact/redact/v1/redact.proto",
		"app/admin/service/cmd/server/assets/assets.go",
		"app/admin/service/cmd/server/assets/modeldev_openapi_test.go",
		"app/admin/service/cmd/server/assets/go.mod",
		"api/gen/go/notes.md",
	}
	for _, p := range generated {
		if !isGeneratedArtifact(p) {
			t.Errorf("%s must be writable back as generation output", p)
		}
	}
	for _, p := range handWritten {
		if isGeneratedArtifact(p) {
			t.Errorf("%s is hand-written and must never be a write-back target", p)
		}
	}
}

// TestEveryCommittedArtifactUnderAManagedRootIsClassified reads the repository's own tree: it is the
// check that the classification rule above still describes what the accepted chain emits. The
// hand-written counts are the files that share an output root and must survive the staging sweep, so
// a new one here means either a new hand-written file in a generated directory or a new output shape,
// and both deserve a deliberate decision rather than a silent rule change.
func TestEveryCommittedArtifactUnderAManagedRootIsClassified(t *testing.T) {
	expectedHandWritten := map[string]int{
		"api/gen/go":                                         0,
		"api/quota/gen/go":                                   0,
		"pkg/localdeps/go-crud/api/gen/go":                   0,
		"pkg/localdeps/kratos-bootstrap/api/gen/go":          0,
		"app/admin/service/cmd/server/assets":                2,  // assets.go and the hand-written modeldev_openapi_test.go
		"pkg/localdeps/go-wind-toolkit/protoc-gen-go-redact": 28, // the takeover package around redact/v1/redact.pb.go
	}

	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "api"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed []*genTemplate
	for _, e := range entries {
		// buf.model.gen.yaml is parked and must stay out of the derived roots.
		if !strings.HasSuffix(e.Name(), ".gen.yaml") || e.Name() == "buf.model.gen.yaml" {
			continue
		}
		parsed = append(parsed, mustParse(t, filepath.Join(root, "api", e.Name())))
	}
	roots, err := deriveManagedRoots(root, filepath.Join(root, "api"), parsed)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != len(expectedHandWritten) {
		t.Fatalf("the chain now has %d managed roots %v, the baseline knows %d", len(roots), roots, len(expectedHandWritten))
	}
	for _, m := range roots {
		// The sweep is what turns this from a count into a promise: everything generated-class has to
		// come back from generation itself.
		stage := t.TempDir()
		if err := copyTree(filepath.Join(root, filepath.FromSlash(m.dir)), filepath.Join(stage, filepath.FromSlash(m.dir))); err != nil {
			t.Fatal(err)
		}
		before, err := treeDigests(filepath.Join(root, filepath.FromSlash(m.dir)))
		if err != nil {
			t.Fatal(err)
		}
		if err := sweepGenerated(stage, []managedRoot{m}); err != nil {
			t.Fatal(err)
		}
		after, err := treeDigests(filepath.Join(stage, filepath.FromSlash(m.dir)))
		if err != nil {
			t.Fatal(err)
		}
		hand := 0
		for rel := range after {
			if !isGeneratedArtifact(path.Join(m.dir, rel)) {
				hand++
			}
		}
		if want, ok := expectedHandWritten[m.dir]; !ok {
			t.Errorf("managed root %s has no hand-written baseline", m.dir)
		} else if hand != want {
			t.Errorf("%s kept %d hand-written files after the sweep, the baseline is %d", m.dir, hand, want)
		}
		if swept := len(before) - len(after); swept <= 0 {
			t.Errorf("%s: the sweep removed %d generated files, expected the whole generated set", m.dir, swept)
		}
	}
}
