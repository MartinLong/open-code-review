// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/vcs"
)

func newSVNSearchProvider(t *testing.T, files map[string]string) *CodeSearchProvider {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewCodeSearch(&FileReader{RepoDir: dir, Mode: ModeWorkspace, VCS: vcs.SVN})
}

func execSearch(t *testing.T, p *CodeSearchProvider, args map[string]any) string {
	t.Helper()
	out, err := p.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func TestNativeSearchLiteralCaseInsensitive(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{
		"a.go":     "package a\nfunc FindMe() {}\n",
		"sub/b.go": "package sub\n// findme here\n",
	})
	out := execSearch(t, p, map[string]any{"search_text": "FINDME"})
	if !strings.Contains(out, "File: a.go\nMatch lines: 1\n2|func FindMe() {}\n") {
		t.Errorf("a.go match malformed:\n%s", out)
	}
	if !strings.Contains(out, "File: sub/b.go\nMatch lines: 1\n2|// findme here\n") {
		t.Errorf("sub/b.go match malformed:\n%s", out)
	}
}

func TestNativeSearchCaseSensitive(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{
		"a.go": "FindMe\nfindme\n",
	})
	out := execSearch(t, p, map[string]any{"search_text": "FINDME", "case_sensitive": true})
	if out != "No matches found" {
		t.Errorf("case-sensitive search should not match, got:\n%s", out)
	}
	out = execSearch(t, p, map[string]any{"search_text": "FindMe", "case_sensitive": true})
	if !strings.Contains(out, "1|FindMe") {
		t.Errorf("case-sensitive match missing:\n%s", out)
	}
}

func TestNativeSearchRegexp(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{
		"a.go": "foo123bar\nplain\n",
	})
	out := execSearch(t, p, map[string]any{"search_text": `foo\d+bar`, "use_perl_regexp": true})
	if !strings.Contains(out, "1|foo123bar") {
		t.Errorf("regexp match missing:\n%s", out)
	}
	out = execSearch(t, p, map[string]any{"search_text": "FOO[0-9]+BAR", "use_perl_regexp": true})
	if !strings.Contains(out, "1|foo123bar") {
		t.Errorf("case-insensitive regexp match missing:\n%s", out)
	}
	out = execSearch(t, p, map[string]any{"search_text": "([", "use_perl_regexp": true})
	if !strings.HasPrefix(out, "Error: invalid search_text") {
		t.Errorf("invalid regexp should report an error string, got:\n%s", out)
	}
}

func TestNativeSearchFilePatterns(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{
		"src/a.go":   "needle\n",
		"src/b.txt":  "needle\n",
		"other/c.go": "needle\n",
	})
	out := execSearch(t, p, map[string]any{
		"search_text":   "needle",
		"file_patterns": []any{"*.go"},
	})
	if !strings.Contains(out, "File: src/a.go") || !strings.Contains(out, "File: other/c.go") {
		t.Errorf("basename glob should match both .go files:\n%s", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Errorf("b.txt should be filtered out:\n%s", out)
	}

	out = execSearch(t, p, map[string]any{
		"search_text":   "needle",
		"file_patterns": []any{"src/*.txt"},
	})
	if !strings.Contains(out, "File: src/b.txt") || strings.Contains(out, "a.go") {
		t.Errorf("path glob mismatch:\n%s", out)
	}
}

func TestNativeSearchSkipsBinaryAndExcludedDirs(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{
		"a.go":            "needle\n",
		"bin.dat":         "needle\x00binary\n",
		".svn/hidden.txt": "needle\n",
		"vendor/v.go":     "needle\n",
	})
	out := execSearch(t, p, map[string]any{"search_text": "needle"})
	if !strings.Contains(out, "File: a.go") {
		t.Errorf("a.go match missing:\n%s", out)
	}
	for _, unwanted := range []string{"bin.dat", ".svn", "vendor"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s should be excluded:\n%s", unwanted, out)
		}
	}
}

func TestNativeSearchNoMatches(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{"a.go": "package a\n"})
	out := execSearch(t, p, map[string]any{"search_text": "absent"})
	if out != "No matches found" {
		t.Errorf("got %q, want %q", out, "No matches found")
	}
}

func TestNativeSearchRefRejected(t *testing.T) {
	p := newSVNSearchProvider(t, map[string]string{"a.go": "needle\n"})
	p.FileReader.Ref = "3"
	out := execSearch(t, p, map[string]any{"search_text": "needle"})
	if !strings.HasPrefix(out, "Error: code_search at a specific revision") {
		t.Errorf("ref search should report an error string, got:\n%s", out)
	}
}

func TestNativeSearchTruncation(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < gitGrepMaxCount+20; i++ {
		fmt.Fprintf(&sb, "needle line %d\n", i)
	}
	p := newSVNSearchProvider(t, map[string]string{"big.txt": sb.String()})
	out := execSearch(t, p, map[string]any{"search_text": "needle"})
	if !strings.HasPrefix(out, "Note: Showing the first 100 matches") {
		t.Errorf("truncation note missing:\n%.120s", out)
	}
	if strings.Contains(out, fmt.Sprintf("%d|", gitGrepMaxCount+1)) {
		t.Errorf("more than %d matches rendered", gitGrepMaxCount)
	}
}

func TestNativeSearchGitStillUsesGitGrep(t *testing.T) {
	// Zero-value VCS (git) must keep the git grep path; in a non-repo temp
	// dir git grep falls back to --no-index, which still finds the match.
	p := newSVNSearchProvider(t, map[string]string{"a.go": "needle\n"})
	p.FileReader.VCS = vcs.Git
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	out := execSearch(t, p, map[string]any{"search_text": "needle"})
	if !strings.Contains(out, "File: a.go") {
		t.Errorf("git-mode search lost the match:\n%s", out)
	}
}

func TestIsBinaryReader(t *testing.T) {
	if isBinaryReader(bufio.NewReader(strings.NewReader("plain text\nno nul\n"))) {
		t.Error("plain text misdetected as binary")
	}
	if !isBinaryReader(bufio.NewReader(strings.NewReader("head\x00tail"))) {
		t.Error("NUL-containing content not detected as binary")
	}
	if isBinaryReader(bufio.NewReader(strings.NewReader(""))) {
		t.Error("empty input misdetected as binary")
	}
}
