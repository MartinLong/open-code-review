// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/svncmd"
	"github.com/alibaba/open-code-review/internal/vcs"
)

// setupSVNFileReaderWC builds a one-revision repository containing a.txt and
// returns the working copy path.
func setupSVNFileReaderWC(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}

	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	if out, err := exec.Command("svnadmin", "create", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, out)
	}
	repoURL := "file:///" + strings.TrimPrefix(filepath.ToSlash(repoDir), "/")
	wc := filepath.Join(tmp, "wc")
	if out, err := exec.Command("svn", "checkout", "--non-interactive", repoURL, wc).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, out)
	}

	a := filepath.Join(wc, "a.txt")
	if err := os.WriteFile(a, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("svn", "add", "a.txt")
	add.Dir = wc
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("svn add: %v\n%s", err, out)
	}
	commit := exec.Command("svn", "commit", "--non-interactive", "-m", "add a.txt")
	commit.Dir = wc
	if out, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, out)
	}
	return wc
}

func TestFileReaderSVNReadAtRevision(t *testing.T) {
	wc := setupSVNFileReaderWC(t)
	// Diverge the working copy so a successful read provably came from r1.
	if err := os.WriteFile(filepath.Join(wc, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &FileReader{RepoDir: wc, Mode: ModeCommit, Ref: "1", VCS: vcs.SVN}
	content, err := fr.Read(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if content != "alpha\nbeta\ngamma\n" {
		t.Errorf("Read = %q, want r1 content", content)
	}

	lines, total, err := fr.ReadLines(context.Background(), "a.txt", 2, 1)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	// Total follows strings.Split semantics: a trailing newline counts a
	// final empty line.
	if total != 4 || len(lines) != 1 || lines[0] != "beta" {
		t.Errorf("ReadLines = %v (total %d), want [beta] of 4", lines, total)
	}
}

func TestFileReaderSVNReadWithRunner(t *testing.T) {
	wc := setupSVNFileReaderWC(t)
	fr := &FileReader{RepoDir: wc, Mode: ModeRange, Ref: "1", VCS: vcs.SVN, SVNRunner: svncmd.New(1)}
	content, err := fr.Read(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("Read via runner: %v", err)
	}
	if !strings.Contains(content, "alpha") {
		t.Errorf("Read via runner = %q", content)
	}
}

func TestFileReaderSVNReadErrors(t *testing.T) {
	wc := setupSVNFileReaderWC(t)

	fr := &FileReader{RepoDir: wc, Mode: ModeCommit, Ref: "-1", VCS: vcs.SVN}
	if _, err := fr.Read(context.Background(), "a.txt"); err == nil {
		t.Fatal("Read with dash-prefixed ref: want error")
	}

	fr = &FileReader{RepoDir: wc, Mode: ModeCommit, Ref: "1", VCS: vcs.SVN}
	if _, err := fr.Read(context.Background(), "-file"); err == nil {
		t.Fatal("Read with dash-prefixed path: want error")
	}
	if _, err := fr.Read(context.Background(), "missing.txt"); err == nil {
		t.Fatal("Read of missing file: want error")
	}
	if _, _, err := fr.ReadLines(context.Background(), "missing.txt", 1, 10); err == nil {
		t.Fatal("ReadLines of missing file: want error")
	}
}
