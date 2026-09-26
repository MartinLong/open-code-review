// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package vcs

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFlag(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    Kind
		auto    bool
		wantErr bool
	}{
		{name: "empty is auto", input: "", want: Git, auto: true},
		{name: "auto", input: "auto", want: Git, auto: true},
		{name: "git", input: "git", want: Git, auto: false},
		{name: "svn", input: "svn", want: SVN, auto: false},
		{name: "case insensitive", input: "SVN", want: SVN, auto: false},
		{name: "surrounding space", input: " git ", want: Git, auto: false},
		{name: "invalid", input: "hg", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, auto, err := ParseFlag(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseFlag(%q) = nil error, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFlag(%q) error: %v", tt.input, err)
			}
			if kind != tt.want || auto != tt.auto {
				t.Fatalf("ParseFlag(%q) = (%v, %v), want (%v, %v)", tt.input, kind, auto, tt.want, tt.auto)
			}
		})
	}
}

func TestKindString(t *testing.T) {
	if Git.String() != "git" {
		t.Fatalf("Git.String() = %q", Git.String())
	}
	if SVN.String() != "svn" {
		t.Fatalf("SVN.String() = %q", SVN.String())
	}
}

func TestSVNWorkingCopyRootNotAWorkingCopy(t *testing.T) {
	if root := SVNWorkingCopyRoot(t.TempDir()); root != "" {
		t.Fatalf("SVNWorkingCopyRoot on plain dir = %q, want empty", root)
	}
}

func TestSVNWorkingCopyRootIntegration(t *testing.T) {
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
	sub := filepath.Join(wc, "sub")
	mkdir := exec.Command("svn", "mkdir", "sub")
	mkdir.Dir = wc
	if out, err := mkdir.CombinedOutput(); err != nil {
		t.Fatalf("svn mkdir: %v\n%s", err, out)
	}

	for _, dir := range []string{wc, sub} {
		root := SVNWorkingCopyRoot(dir)
		if root == "" {
			t.Fatalf("SVNWorkingCopyRoot(%q) = empty, want %q", dir, wc)
		}
		if !strings.EqualFold(filepath.Clean(root), filepath.Clean(wc)) {
			t.Errorf("SVNWorkingCopyRoot(%q) = %q, want %q", dir, root, wc)
		}
	}
}
