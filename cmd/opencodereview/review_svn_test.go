// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/vcs"
)

func TestValidateReviewRefsForVCSSVN(t *testing.T) {
	cc := &commonContext{VCS: vcs.SVN}

	valid := []reviewOptions{
		{commit: "12"},
		{commit: "HEAD"},
		{from: "1", to: "BASE"},
		{from: "", to: ""},
	}
	for _, opts := range valid {
		if err := validateReviewRefsForVCS(cc, opts); err != nil {
			t.Errorf("validateReviewRefsForVCS(%+v): %v", opts, err)
		}
	}

	invalid := []reviewOptions{
		{commit: "abc;x"},
		{from: "HEAD~1", to: "2"},
		{to: "{2024-01-01}"},
	}
	for _, opts := range invalid {
		if err := validateReviewRefsForVCS(cc, opts); err == nil {
			t.Errorf("validateReviewRefsForVCS(%+v): want error", opts)
		}
	}
}

func TestValidateReviewRefsForVCSDispatchesToGit(t *testing.T) {
	// A git commonContext must run git ref validation, which fails outside a
	// git repository even for a syntactically plausible ref.
	cc := &commonContext{VCS: vcs.Git, RepoDir: t.TempDir()}
	if err := validateReviewRefsForVCS(cc, reviewOptions{commit: "HEAD"}); err == nil {
		t.Fatal("git validation outside a repository: want error")
	}
}

// setupSVNCmdWC builds a one-revision repository and returns the working
// copy path.
func setupSVNCmdWC(t *testing.T) string {
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
	return wc
}

func TestResolveWorkingDirVCSSVN(t *testing.T) {
	wc := setupSVNCmdWC(t)
	sub := filepath.Join(wc, "sub")
	mkdir := exec.Command("svn", "mkdir", "sub")
	mkdir.Dir = wc
	if out, err := mkdir.CombinedOutput(); err != nil {
		t.Fatalf("svn mkdir: %v\n%s", err, out)
	}

	dir, kind, detected, err := resolveWorkingDirVCS(wc, true, "auto")
	if err != nil {
		t.Fatalf("auto detect: %v", err)
	}
	if !detected || kind != vcs.SVN {
		t.Errorf("auto detect = (%v, %v), want SVN detected", kind, detected)
	}
	if !strings.EqualFold(filepath.Clean(dir), filepath.Clean(wc)) {
		t.Errorf("auto detect dir = %q, want %q", dir, wc)
	}

	// A subdirectory anchors at the working copy root for the review path.
	dir, kind, detected, err = resolveWorkingDirVCS(sub, true, "svn")
	if err != nil {
		t.Fatalf("forced svn: %v", err)
	}
	if !detected || kind != vcs.SVN || !strings.EqualFold(filepath.Clean(dir), filepath.Clean(wc)) {
		t.Errorf("forced svn = (%q, %v, %v), want root %q", dir, kind, detected, wc)
	}

	if _, _, _, err := resolveWorkingDirVCS(wc, true, "git"); err == nil {
		t.Error("forced git on an SVN working copy: want error")
	}
	if _, _, _, err := resolveWorkingDirVCS(wc, true, "hg"); err == nil {
		t.Error("invalid --vcs value: want error")
	}

	// Without requireVCS a plain directory passes through undetected; the
	// kind is only meaningful when detected is true.
	plain := t.TempDir()
	dir, _, detected, err = resolveWorkingDirVCS(plain, false, "svn")
	if err != nil {
		t.Fatalf("non-required resolve: %v", err)
	}
	if detected || !strings.EqualFold(filepath.Clean(dir), filepath.Clean(plain)) {
		t.Errorf("non-required resolve = (%q, %v), want passthrough of %q", dir, detected, plain)
	}

	// With requireVCS a plain directory fails for both backends.
	if _, _, _, err := resolveWorkingDirVCS(plain, true, "auto"); err == nil {
		t.Error("required resolve on plain dir: want error")
	}
}
