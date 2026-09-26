// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/svncmd"
)

func TestIsValidSVNRevision(t *testing.T) {
	valid := []string{"0", "1", "123", "HEAD", "head", "Base", "COMMITTED", "prev"}
	for _, ref := range valid {
		if !IsValidSVNRevision(ref) {
			t.Errorf("IsValidSVNRevision(%q) = false, want true", ref)
		}
	}
	invalid := []string{"", "-1", "12a", "{2024-01-01}", "HEAD~1", "HEAD:BASE", "--", "1.5"}
	for _, ref := range invalid {
		if IsValidSVNRevision(ref) {
			t.Errorf("IsValidSVNRevision(%q) = true, want false", ref)
		}
	}
}

func TestSVNDiffToGitModifiedFile(t *testing.T) {
	in := "Index: a.txt\n" +
		"===================================================================\n" +
		"--- a.txt\t(revision 1)\n" +
		"+++ a.txt\t(working copy)\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	out := svnDiffToGit(in)
	for _, want := range []string{
		"diff --git a/a.txt b/a.txt\n",
		"--- a/a.txt\n",
		"+++ b/a.txt\n",
		"@@ -1 +1 @@\n-old\n+new\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("svnDiffToGit output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "new file mode") || strings.Contains(out, "deleted file mode") {
		t.Errorf("modified file must not carry new/deleted mode:\n%s", out)
	}
}

func TestSVNDiffToGitNewAndDeletedFiles(t *testing.T) {
	in := "Index: added.txt\n" +
		"===================================================================\n" +
		"--- added.txt\t(nonexistent)\n" +
		"+++ added.txt\t(working copy)\n" +
		"@@ -0,0 +1 @@\n" +
		"+fresh\n" +
		"Index: gone.txt\n" +
		"===================================================================\n" +
		"--- gone.txt\t(revision 3)\n" +
		"+++ gone.txt\t(nonexistent)\n" +
		"@@ -1 +0,0 @@\n" +
		"-stale\n"
	out := svnDiffToGit(in)
	if !strings.Contains(out, "diff --git a/added.txt b/added.txt\nnew file mode 100644\n--- /dev/null\n+++ b/added.txt\n") {
		t.Errorf("new-file block malformed:\n%s", out)
	}
	if !strings.Contains(out, "diff --git a/gone.txt b/gone.txt\ndeleted file mode 100644\n--- a/gone.txt\n+++ /dev/null\n") {
		t.Errorf("deleted-file block malformed:\n%s", out)
	}
}

func TestSVNDiffToGitBinaryAndPropertyOnly(t *testing.T) {
	in := "Index: bin.dat\n" +
		"===================================================================\n" +
		"Cannot display: file marked as a binary type.\n" +
		"Index: props.txt\n" +
		"===================================================================\n" +
		"--- props.txt\t(revision 2)\n" +
		"+++ props.txt\t(working copy)\n" +
		"\n" +
		"Property changes on: props.txt\n" +
		"___________________________________________________________________\n" +
		"Added: svn:eol-style\n" +
		"## -0,0 +1 ##\n" +
		"+native\n"
	out := svnDiffToGit(in)
	if !strings.Contains(out, "Binary files a/bin.dat and b/bin.dat differ\n") {
		t.Errorf("binary marker missing:\n%s", out)
	}
	if strings.Contains(out, "props.txt") {
		t.Errorf("property-only block must be dropped:\n%s", out)
	}
}

func TestSVNDiffToGitDropsPropertySectionAfterHunks(t *testing.T) {
	in := "Index: mix.txt\n" +
		"===================================================================\n" +
		"--- mix.txt\t(revision 2)\n" +
		"+++ mix.txt\t(working copy)\n" +
		"@@ -1 +1 @@\n" +
		"-a\n" +
		"+b\n" +
		"\n" +
		"Property changes on: mix.txt\n" +
		"___________________________________________________________________\n" +
		"Added: svn:keywords\n" +
		"## -0,0 +1 ##\n" +
		"+Id\n"
	out := svnDiffToGit(in)
	if !strings.Contains(out, "@@ -1 +1 @@\n-a\n+b\n") {
		t.Errorf("content hunk missing:\n%s", out)
	}
	if strings.Contains(out, "svn:keywords") || strings.Contains(out, "Property changes") {
		t.Errorf("property section must not leak into output:\n%s", out)
	}
}

func TestSVNDiffToGitPathWithSpacesAndCRLF(t *testing.T) {
	in := "Index: my dir/some file.go\r\n" +
		"===================================================================\r\n" +
		"--- my dir/some file.go\t(revision 1)\r\n" +
		"+++ my dir/some file.go\t(working copy)\r\n" +
		"@@ -1 +1 @@\r\n" +
		"-x\r\n" +
		"+y\r\n"
	out := svnDiffToGit(in)
	if !strings.Contains(out, "diff --git a/my dir/some file.go b/my dir/some file.go\n") {
		t.Errorf("path with spaces mangled:\n%s", out)
	}
	if strings.Contains(out, "\r") {
		t.Errorf("CRLF not normalized:\n%q", out)
	}
}

func TestSVNStatusUnversioned(t *testing.T) {
	out := "?       file.txt\nM       mod.txt\n?       dir with space\n!       missing.txt\n"
	got := svnStatusUnversioned(out)
	if len(got) != 2 || got[0] != "file.txt" || got[1] != "dir with space" {
		t.Fatalf("svnStatusUnversioned = %v", got)
	}
}

func TestSVNHeaderAnnotation(t *testing.T) {
	if got := svnHeaderAnnotation("--- a.txt\t(revision 42)"); got != "revision 42" {
		t.Errorf("revision annotation = %q", got)
	}
	if got := svnHeaderAnnotation("+++ dir/a b.txt\t(working copy)"); got != "working copy" {
		t.Errorf("working-copy annotation = %q", got)
	}
	if got := svnHeaderAnnotation("--- a.txt"); got != "" {
		t.Errorf("missing annotation = %q, want empty", got)
	}
}

func TestParseDiffTextWithContentReader(t *testing.T) {
	diffText := "diff --git a/fresh.go b/fresh.go\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ b/fresh.go\n" +
		"@@ -0,0 +1 @@\n" +
		"+package main\n"
	reader := func(ctx context.Context, path string) (string, error) {
		if path != "fresh.go" {
			t.Fatalf("reader got path %q", path)
		}
		return "package main\n", nil
	}
	diffs, err := ParseDiffTextWithReader(context.Background(), diffText, t.TempDir(), "7", nil, reader)
	if err != nil {
		t.Fatalf("ParseDiffTextWithReader: %v", err)
	}
	if len(diffs) != 1 {
		t.Fatalf("got %d diffs, want 1", len(diffs))
	}
	if !diffs[0].IsNew {
		t.Error("IsNew = false, want true")
	}
	if diffs[0].NewFileContent != "package main\n" {
		t.Errorf("NewFileContent = %q", diffs[0].NewFileContent)
	}
}

// --- integration against a real svn binary ---

func svnIntegrationAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}
}

func runSVNCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeWCFile(t *testing.T, wc, rel, content string) {
	t.Helper()
	full := filepath.Join(wc, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupSVNWorkingCopy builds a two-revision repository: r1 adds a.txt, r2
// modifies a.txt and adds sub/b.txt. Returns the updated working copy.
func setupSVNWorkingCopy(t *testing.T) string {
	t.Helper()
	svnIntegrationAvailable(t)

	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	runSVNCmd(t, tmp, "svnadmin", "create", repoDir)
	repoURL := "file:///" + strings.TrimPrefix(filepath.ToSlash(repoDir), "/")
	wc := filepath.Join(tmp, "wc")
	runSVNCmd(t, tmp, "svn", "checkout", "--non-interactive", repoURL, wc)

	writeWCFile(t, wc, "a.txt", "line1\nline2\n")
	runSVNCmd(t, wc, "svn", "add", "a.txt")
	runSVNCmd(t, wc, "svn", "commit", "--non-interactive", "-m", "add a.txt")

	writeWCFile(t, wc, "a.txt", "line1\nline2 changed\n")
	writeWCFile(t, wc, "sub/b.txt", "b content\n")
	runSVNCmd(t, wc, "svn", "add", "sub")
	runSVNCmd(t, wc, "svn", "commit", "--non-interactive", "-m", "modify a, add sub/b.txt")
	runSVNCmd(t, wc, "svn", "update", "--non-interactive")
	return wc
}

func findDiff(t *testing.T, set DiffSet, path string) (string, bool, string) {
	t.Helper()
	for _, d := range set.Included {
		if d.NewPath == path || d.OldPath == path {
			return d.NewPath, d.IsNew, d.NewFileContent
		}
	}
	t.Fatalf("no diff for %s in %+v", path, set.Included)
	return "", false, ""
}

func TestSVNProviderWorkspace(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	writeWCFile(t, wc, "a.txt", "line1\nline2 changed again\n")
	writeWCFile(t, wc, "c.txt", "unversioned\n")

	p := NewSVNWorkspaceProvider(wc, nil)
	set, err := p.GetDiffSet(context.Background())
	if err != nil {
		t.Fatalf("GetDiffSet: %v", err)
	}
	if len(set.Included) != 2 {
		t.Fatalf("got %d diffs, want 2: %+v", len(set.Included), set.Included)
	}
	if _, isNew, content := findDiff(t, set, "c.txt"); !isNew || !strings.Contains(content, "unversioned") {
		t.Errorf("c.txt: isNew=%v content=%q", isNew, content)
	}
	if _, isNew, content := findDiff(t, set, "a.txt"); isNew || !strings.Contains(content, "changed again") {
		t.Errorf("a.txt: isNew=%v content=%q", isNew, content)
	}

	res := p.ResolveInput(context.Background())
	if res.ResolvedBase != "2" {
		t.Errorf("workspace ResolvedBase = %q, want 2", res.ResolvedBase)
	}
}

func TestSVNProviderCommit(t *testing.T) {
	wc := setupSVNWorkingCopy(t)

	p := NewSVNCommitProvider(wc, "2", nil)
	set, err := p.GetDiffSet(context.Background())
	if err != nil {
		t.Fatalf("GetDiffSet: %v", err)
	}
	if len(set.Included) != 2 {
		t.Fatalf("got %d diffs, want 2: %+v", len(set.Included), set.Included)
	}
	if _, isNew, content := findDiff(t, set, "sub/b.txt"); !isNew || !strings.Contains(content, "b content") {
		t.Errorf("sub/b.txt: isNew=%v content=%q", isNew, content)
	}
	if _, isNew, content := findDiff(t, set, "a.txt"); isNew || !strings.Contains(content, "line2 changed") {
		t.Errorf("a.txt: isNew=%v content=%q", isNew, content)
	}

	res := p.ResolveInput(context.Background())
	if res.ResolvedHead != "2" || res.ResolvedBase != "1" || res.ExactRange != "1..2" {
		t.Errorf("commit ResolveInput = %+v, want base=1 head=2 exact=1..2", res)
	}
}

func TestSVNProviderKeywordAndRange(t *testing.T) {
	wc := setupSVNWorkingCopy(t)

	head := NewSVNCommitProvider(wc, "HEAD", nil)
	if res := head.ResolveInput(context.Background()); res.ResolvedHead != "2" {
		t.Errorf("HEAD resolved to %q, want 2", res.ResolvedHead)
	}

	rp := NewSVNProvider(wc, "1", "2", nil)
	set, err := rp.GetDiffSet(context.Background())
	if err != nil {
		t.Fatalf("GetDiffSet: %v", err)
	}
	if len(set.Included) != 2 {
		t.Fatalf("range got %d diffs, want 2", len(set.Included))
	}
	res := rp.ResolveInput(context.Background())
	if res.ResolvedBase != "1" || res.ResolvedHead != "2" || res.ExactRange != "1..2" {
		t.Errorf("range ResolveInput = %+v", res)
	}

	// Local file:// repositories carry no stable network identity, exactly
	// like local git remotes, so RemoteIdentity reports empty here.
	if id := rp.RemoteIdentity(context.Background()); id != "" {
		t.Errorf("RemoteIdentity for file:// repo = %q, want empty", id)
	}
}

func TestSVNProviderInvalidRevision(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	p := NewSVNCommitProvider(wc, "not-a-revision", nil)
	if _, err := p.GetDiffSet(context.Background()); err == nil {
		t.Fatal("GetDiffSet with invalid revision: want error")
	}
}

func TestSVNProviderModes(t *testing.T) {
	rp := NewSVNProvider("wc", "1", "2", nil)
	if !rp.IsRangeMode() || rp.IsCommitMode() {
		t.Error("range provider reports wrong mode")
	}
	cp := NewSVNCommitProvider("wc", "2", nil)
	if cp.IsRangeMode() || !cp.IsCommitMode() {
		t.Error("commit provider reports wrong mode")
	}
	wp := NewSVNWorkspaceProvider("wc", nil)
	if wp.IsRangeMode() || wp.IsCommitMode() {
		t.Error("workspace provider reports wrong mode")
	}
	if got := wp.MergeBase(context.Background()); got != "" {
		t.Errorf("SVN MergeBase = %q, want empty", got)
	}
}

func TestSVNFailure(t *testing.T) {
	errPlain := svnFailure("svn diff", "", os.ErrNotExist)
	if got := errPlain.Error(); !strings.Contains(got, "svn diff failed") {
		t.Errorf("svnFailure without stderr = %q", got)
	}
	errDiag := svnFailure("svn diff", "  svn: E195012: boom  ", os.ErrNotExist)
	if !strings.Contains(errDiag.Error(), "svn: E195012: boom") {
		t.Errorf("svnFailure must carry stderr diagnosis, got %q", errDiag.Error())
	}
	long := strings.Repeat("x", gitDiagLimit) + "tail-end"
	errLong := svnFailure("svn diff", long, os.ErrNotExist)
	if !strings.Contains(errLong.Error(), "tail-end") || !strings.Contains(errLong.Error(), "...") {
		t.Errorf("svnFailure must truncate long stderr keeping the tail, got %q", errLong.Error())
	}
	multi := strings.Repeat("y", gitDiagLimit) + "\xe4\xb8" + "\xad" + "end"
	if got := svnFailure("svn diff", multi, os.ErrNotExist).Error(); !strings.Contains(got, "end") {
		t.Errorf("svnFailure must not split a UTF-8 rune, got %q", got)
	}
}

func TestSVNProviderGetDiff(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	writeWCFile(t, wc, "a.txt", "line1\nline2 changed again\n")

	p := NewSVNWorkspaceProvider(wc, nil)
	diffs, err := p.GetDiff(context.Background())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 || diffs[0].NewPath != "a.txt" {
		t.Fatalf("GetDiff = %+v, want one diff for a.txt", diffs)
	}
}

func TestSVNProviderUnversionedDirectory(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	writeWCFile(t, wc, "newdir/nested/fresh.txt", "fresh\n")

	p := NewSVNWorkspaceProvider(wc, nil)
	files, err := p.unversionedFilesList(context.Background())
	if err != nil {
		t.Fatalf("unversionedFilesList: %v", err)
	}
	if len(files) != 1 || files[0] != "newdir/nested/fresh.txt" {
		t.Fatalf("unversionedFilesList = %v, want [newdir/nested/fresh.txt]", files)
	}
}

func TestSVNProviderWithRunner(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	writeWCFile(t, wc, "a.txt", "line1\nline2 changed again\n")

	p := NewSVNWorkspaceProvider(wc, svncmd.New(2))
	set, err := p.GetDiffSet(context.Background())
	if err != nil {
		t.Fatalf("GetDiffSet via runner: %v", err)
	}
	if len(set.Included) != 1 || set.Included[0].NewPath != "a.txt" {
		t.Fatalf("GetDiffSet via runner = %+v", set.Included)
	}
	if res := p.ResolveInput(context.Background()); res.ResolvedBase != "2" {
		t.Errorf("ResolveInput via runner = %+v, want base 2", res)
	}
	if _, err := p.catAtRef(context.Background(), "1", "a.txt"); err != nil {
		t.Errorf("catAtRef via runner: %v", err)
	}
}

func TestSVNProviderCatAtRefGuards(t *testing.T) {
	p := NewSVNWorkspaceProvider(t.TempDir(), nil)
	if _, err := p.catAtRef(context.Background(), "1", "-file"); err == nil {
		t.Fatal("catAtRef with dash-prefixed path: want error")
	}
}

func TestSVNProviderErrorsOutsideWorkingCopy(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewSVNWorkspaceProvider(dir, nil).GetDiffSet(context.Background()); err == nil {
		t.Fatal("GetDiffSet outside a working copy: want error")
	}
	if res := NewSVNWorkspaceProvider(dir, nil).ResolveInput(context.Background()); res != (InputResolution{}) {
		t.Errorf("ResolveInput outside a working copy = %+v, want empty", res)
	}
	if id := NewSVNWorkspaceProvider(dir, nil).RemoteIdentity(context.Background()); id != "" {
		t.Errorf("RemoteIdentity outside a working copy = %q, want empty", id)
	}
	if _, err := NewSVNProvider(dir, "1", "2", nil).GetDiffSet(context.Background()); err == nil {
		t.Fatal("range GetDiffSet outside a working copy: want error")
	}
}

func TestSVNProviderResolveInputInvalidRefs(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	if res := NewSVNProvider(wc, "bogus", "2", nil).ResolveInput(context.Background()); res != (InputResolution{}) {
		t.Errorf("range with invalid --from = %+v, want empty", res)
	}
	if res := NewSVNProvider(wc, "1", "bogus", nil).ResolveInput(context.Background()); res != (InputResolution{}) {
		t.Errorf("range with invalid --to = %+v, want empty", res)
	}
	if res := NewSVNCommitProvider(wc, "bogus", nil).ResolveInput(context.Background()); res != (InputResolution{}) {
		t.Errorf("commit with invalid ref = %+v, want empty", res)
	}
	if _, err := NewSVNCommitProvider(wc, "bogus", nil).GetDiff(context.Background()); err == nil {
		t.Fatal("GetDiff with invalid ref: want error")
	}
	if res := (&SVNProvider{wcDir: wc, mode: Mode(99)}).ResolveInput(context.Background()); res != (InputResolution{}) {
		t.Errorf("unsupported mode ResolveInput = %+v, want empty", res)
	}
	if _, err := (&SVNProvider{wcDir: wc, mode: Mode(99)}).GetDiffSet(context.Background()); err == nil {
		t.Fatal("unsupported mode GetDiffSet: want error")
	}
}

func TestSVNProviderCommitRevisionOne(t *testing.T) {
	wc := setupSVNWorkingCopy(t)
	res := NewSVNCommitProvider(wc, "1", nil).ResolveInput(context.Background())
	if res.ResolvedHead != "1" || res.ResolvedBase != "" || res.ExactRange != "" {
		t.Errorf("r1 has no predecessor: ResolveInput = %+v, want head only", res)
	}
}

// A deleted line whose content starts with "-- " (SQL comment style) appears
// in the svn diff as "--- ...", and an added line starting with "++ " appears
// as "+++ ...". Both must survive as hunk content, not be mistaken for file
// headers.
func TestSVNDiffToGitHunkLinesResemblingFileHeaders(t *testing.T) {
	in := "Index: q.sql\n" +
		"===================================================================\n" +
		"--- q.sql\t(revision 1)\n" +
		"+++ q.sql\t(working copy)\n" +
		"@@ -1,2 +1,2 @@\n" +
		"--- old note\n" +
		"+++ new note\n"
	out := svnDiffToGit(in)
	if !strings.Contains(out, "@@ -1,2 +1,2 @@\n--- old note\n+++ new note\n") {
		t.Errorf("hunk lines with header-like prefixes dropped:\n%s", out)
	}
	if strings.Contains(out, "new file mode") || strings.Contains(out, "deleted file mode") {
		t.Errorf("header-like hunk lines must not flip new/deleted detection:\n%s", out)
	}
}
