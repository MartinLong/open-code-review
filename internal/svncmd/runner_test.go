// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestZeroRunnerErrors(t *testing.T) {
	r := &Runner{}
	if _, err := r.Run(context.Background(), "."); err == nil {
		t.Fatal("Run on zero Runner: want error")
	}
	if _, err := r.Output(context.Background(), "."); err == nil {
		t.Fatal("Output on zero Runner: want error")
	}
	if _, _, err := r.RunSplit(context.Background(), "."); err == nil {
		t.Fatal("RunSplit on zero Runner: want error")
	}
}

func TestNewDefaultsConcurrency(t *testing.T) {
	r := New(0)
	if r == nil || cap(r.sem) != defaultMaxConcurrent {
		t.Fatalf("New(0) semaphore capacity = %d, want %d", cap(r.sem), defaultMaxConcurrent)
	}
	if r.binary != "svn" {
		t.Fatalf("New(0) binary = %q, want svn", r.binary)
	}
}

func TestNewBinaryMissingBinary(t *testing.T) {
	r := NewBinary("definitely-not-a-real-svn-binary", 2)
	_, err := r.Run(context.Background(), ".", "info")
	if err == nil {
		t.Fatal("Run with missing binary: want error")
	}
	if got := cap(r.sem); got != 2 {
		t.Fatalf("NewBinary(_, 2) semaphore capacity = %d, want 2", got)
	}
}

func TestUninitializedErrorMessage(t *testing.T) {
	r := &Runner{}
	_, err := r.Run(context.Background(), ".")
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("zero Runner error = %v, want 'not initialized'", err)
	}
}

func TestRunnerWithRealSVN(t *testing.T) {
	if _, err := exec.LookPath("svn"); err != nil {
		t.Skip("svn not installed")
	}
	r := New(2)

	out, err := r.Run(context.Background(), ".", "--version", "--quiet")
	if err != nil {
		t.Fatalf("Run --version: %v", err)
	}
	if !strings.Contains(out, "1.") {
		t.Errorf("Run --version output = %q, want a version number", out)
	}

	b, err := r.Output(context.Background(), ".", "--version", "--quiet")
	if err != nil || len(b) == 0 {
		t.Fatalf("Output --version = %q, %v", b, err)
	}

	stdout, stderr, err := r.RunSplit(context.Background(), t.TempDir(), "info")
	if err == nil {
		t.Fatal("RunSplit info outside a working copy: want error")
	}
	if stdout != "" {
		t.Errorf("RunSplit failed command stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Error("RunSplit failed command stderr empty, want svn diagnosis")
	}

	if _, err := r.Output(context.Background(), t.TempDir(), "info"); err == nil {
		t.Fatal("Output info outside a working copy: want error")
	}
}

func TestRunnerRespectsContextCancel(t *testing.T) {
	r := New(1)
	r.sem <- struct{}{} // hold the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Run(ctx, ".", "--version"); err == nil {
		t.Fatal("Run with cancelled context and exhausted semaphore: want error")
	}
	<-r.sem
}
