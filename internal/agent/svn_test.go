// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/diff"
	"github.com/alibaba/open-code-review/internal/vcs"
)

// setupSVNAgentWC builds a two-revision repository (r1 adds a.txt, r2 modifies
// it) and returns the working copy path.
func setupSVNAgentWC(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn not installed")
	}
	if _, err := exec.LookPath("svnadmin"); err != nil {
		t.Skip("svnadmin not installed")
	}

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	tmp := t.TempDir()
	repoDir := filepath.Join(tmp, "repo")
	run(tmp, "svnadmin", "create", repoDir)
	repoURL := "file:///" + strings.TrimPrefix(filepath.ToSlash(repoDir), "/")
	wc := filepath.Join(tmp, "wc")
	run(tmp, "svn", "checkout", "--non-interactive", repoURL, wc)

	a := filepath.Join(wc, "a.txt")
	if err := os.WriteFile(a, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wc, "svn", "add", "a.txt")
	run(wc, "svn", "commit", "--non-interactive", "-m", "r1")
	if err := os.WriteFile(a, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(wc, "svn", "commit", "--non-interactive", "-m", "r2")
	run(wc, "svn", "update", "--non-interactive")
	return wc
}

func TestNewDiffProviderSVN(t *testing.T) {
	cases := []struct {
		name string
		args Args
	}{
		{"workspace", Args{RepoDir: "wc", VCS: vcs.SVN}},
		{"commit", Args{RepoDir: "wc", VCS: vcs.SVN, Commit: "2"}},
		{"range", Args{RepoDir: "wc", VCS: vcs.SVN, From: "1", To: "2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &Agent{args: tc.args}
			if _, ok := a.newDiffProvider().(*diff.SVNProvider); !ok {
				t.Fatalf("newDiffProvider = %T, want *diff.SVNProvider", a.newDiffProvider())
			}
		})
	}
}

func TestResolveSVNInputBeforeDiff(t *testing.T) {
	wc := setupSVNAgentWC(t)
	ctx := context.Background()

	res, err := resolveSVNInputBeforeDiff(ctx, Args{RepoDir: wc, VCS: vcs.SVN})
	if err != nil || res != nil {
		t.Errorf("workspace resolve = %+v, %v; want nil, nil", res, err)
	}

	res, err = resolveSVNInputBeforeDiff(ctx, Args{RepoDir: wc, VCS: vcs.SVN, Commit: "HEAD"})
	if err != nil {
		t.Fatalf("commit resolve: %v", err)
	}
	if res.ResolvedHead != "2" {
		t.Errorf("commit HEAD resolved to %q, want 2", res.ResolvedHead)
	}

	res, err = resolveSVNInputBeforeDiff(ctx, Args{RepoDir: wc, VCS: vcs.SVN, From: "1", To: "HEAD"})
	if err != nil {
		t.Fatalf("range resolve: %v", err)
	}
	if res.ResolvedBase != "1" || res.ResolvedHead != "2" || res.ExactRange != "1..2" {
		t.Errorf("range resolve = %+v, want base=1 head=2 exact=1..2", res)
	}

	// Numeric revisions pass through unchecked (svn reports nonexistent ones
	// when the diff runs); unresolvable keyword revisions fail here.
	if _, err := resolveSVNInputBeforeDiff(ctx, Args{RepoDir: wc, VCS: vcs.SVN, Commit: "bogus"}); err == nil {
		t.Error("commit resolve of invalid revision: want error")
	}
	if _, err := resolveSVNInputBeforeDiff(ctx, Args{RepoDir: wc, VCS: vcs.SVN, From: "bogus", To: "HEAD"}); err == nil {
		t.Error("range resolve of invalid revisions: want error")
	}
}

func TestResolveIdentitySVNCommit(t *testing.T) {
	wc := setupSVNAgentWC(t)
	args := Args{
		RepoDir:  wc,
		VCS:      vcs.SVN,
		Commit:   "HEAD",
		Template: template.Template{MaxTokens: 4000},
	}
	sealed, err := ResolveIdentity(context.Background(), args)
	if err != nil {
		t.Fatalf("ResolveIdentity: %v", err)
	}
	if sealed.Resolution.ResolvedHead != "2" {
		t.Errorf("sealed head = %q, want 2", sealed.Resolution.ResolvedHead)
	}
	if sealed.Identity.Mode == "" || sealed.Identity.SourceArtifactSHA256 == "" {
		t.Errorf("identity incomplete: %+v", sealed.Identity)
	}
}

func TestDetectSVNBranch(t *testing.T) {
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
	if out, err := exec.Command("svn", "mkdir", "--non-interactive", "-m", "trunk", repoURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn mkdir: %v\n%s", err, out)
	}
	wc := filepath.Join(tmp, "wc")
	if out, err := exec.Command("svn", "checkout", "--non-interactive", repoURL+"/trunk", wc).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, out)
	}

	if got := detectSVNBranch(context.Background(), wc); got != "trunk" {
		t.Errorf("detectSVNBranch = %q, want trunk", got)
	}
	if got := detectSVNBranch(context.Background(), t.TempDir()); got != "" {
		t.Errorf("detectSVNBranch outside a working copy = %q, want empty", got)
	}
}
