// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package svncmd executes svn subprocesses under a shared concurrency limit,
// mirroring internal/gitcmd for Subversion working copies.
package svncmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

const defaultMaxConcurrent = 16

// Runner limits the number of concurrent svn subprocesses via an internal
// semaphore. All svn command invocations should go through a shared Runner
// instance so that the total system-wide subprocess count stays bounded.
type Runner struct {
	binary string
	sem    chan struct{}
}

// New creates a Runner for the "svn" binary that allows at most
// maxConcurrent simultaneous svn subprocesses. If maxConcurrent <= 0 the
// default (16) is used.
func New(maxConcurrent int) *Runner {
	return NewBinary("svn", maxConcurrent)
}

// NewBinary creates a Runner for an explicit binary, which tests use to point
// at a fixture executable.
func NewBinary(binary string, maxConcurrent int) *Runner {
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrent
	}
	return &Runner{binary: binary, sem: make(chan struct{}, maxConcurrent)}
}

func (r *Runner) acquire(ctx context.Context) error {
	if r.sem == nil {
		return fmt.Errorf("svncmd.Runner not initialized; use svncmd.New()")
	}
	select {
	case r.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) release() { <-r.sem }

// Run executes an svn command and returns the combined stdout+stderr output.
func (r *Runner) Run(ctx context.Context, wcDir string, args ...string) (string, error) {
	if err := r.acquire(ctx); err != nil {
		return "", err
	}
	defer r.release()

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = wcDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// Output executes an svn command and returns stdout only.
func (r *Runner) Output(ctx context.Context, wcDir string, args ...string) ([]byte, error) {
	if err := r.acquire(ctx); err != nil {
		return nil, err
	}
	defer r.release()

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = wcDir
	return cmd.Output()
}

// RunSplit executes an svn command and returns stdout and stderr separately,
// for the callers that quote the failure back to the user: svn writes the
// diff to stdout and its diagnosis to stderr, so only stderr is safe to echo.
func (r *Runner) RunSplit(ctx context.Context, wcDir string, args ...string) (string, string, error) {
	if err := r.acquire(ctx); err != nil {
		return "", "", err
	}
	defer r.release()

	cmd := exec.CommandContext(ctx, r.binary, args...)
	cmd.Dir = wcDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
