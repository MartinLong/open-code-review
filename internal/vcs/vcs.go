// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package vcs identifies the version control system backing a directory, so
// the review pipeline can drive either a git repository or an Apache
// Subversion (SVN) working copy.
package vcs

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Kind identifies a version control system.
type Kind int

const (
	// Git is a git repository. It is the zero value so callers that never set
	// a kind keep their historical behavior.
	Git Kind = iota
	// SVN is an Apache Subversion working copy.
	SVN
)

func (k Kind) String() string {
	switch k {
	case SVN:
		return "svn"
	default:
		return "git"
	}
}

// ParseFlag validates a --vcs flag value. An empty value or "auto" reports
// auto=true, meaning the caller should detect the system from the working
// directory; "git" and "svn" force that system.
func ParseFlag(s string) (kind Kind, auto bool, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return Git, true, nil
	case "git":
		return Git, false, nil
	case "svn":
		return SVN, false, nil
	default:
		return Git, false, fmt.Errorf("invalid --vcs value %q: expected auto, git or svn", s)
	}
}

// SVNWorkingCopyRoot returns the absolute working-copy root containing dir,
// or "" when dir is not inside an SVN working copy or the svn binary is not
// installed. svn resolves the root itself, so dir may be any subdirectory of
// the working copy.
func SVNWorkingCopyRoot(dir string) string {
	// svn info is a local operation, but a hung wrapper script or a stalled
	// network mount must not block startup probing forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A trailing "@" pins an empty peg revision so a directory name
	// containing "@" is not misparsed as a peg revision.
	cmd := exec.CommandContext(ctx, "svn", "info", "--show-item", "wc-root", "--", dir+"@")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return ""
	}
	return root
}
