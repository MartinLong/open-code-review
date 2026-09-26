// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/alibaba/open-code-review/internal/diff"
	"github.com/alibaba/open-code-review/internal/vcs"
)

// nativeSearch implements code_search without a VCS binary by walking the
// working tree. It backs SVN workspace reviews, where `git grep` is unusable
// and svn itself offers no equivalent search command.
func (p *CodeSearchProvider) nativeSearch(ctx context.Context, searchText string, caseSensitive bool, useRegexp bool, patterns []string) (string, error) {
	if p.FileReader.VCS == vcs.SVN && p.FileReader.Ref != "" {
		return "Error: code_search at a specific revision is not supported for SVN working copies; run the review against the workspace to search files", nil
	}

	matcher, err := buildSearchMatcher(searchText, caseSensitive, useRegexp)
	if err != nil {
		return fmt.Sprintf("Error: invalid search_text: %s", err), nil
	}

	ctx, cancel := context.WithTimeout(ctx, gitGrepTimeout)
	defer cancel()

	ignorePatterns := diff.LoadGitignorePatterns(p.FileReader.RepoDir)

	fileMatches := make(map[string][]searchMatch)
	var fileOrder []string
	matchedFiles := make(map[string]bool)
	matchCount := 0
	truncated := false

	walkErr := filepath.WalkDir(p.FileReader.RepoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(p.FileReader.RepoDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if diff.ProviderDirPrefix(rel) != "" || diff.IsPathExcluded(p.FileReader.RepoDir, rel+"/", ignorePatterns) {
				return filepath.SkipDir
			}
			return nil
		}
		if diff.IsPathExcluded(p.FileReader.RepoDir, rel, ignorePatterns) {
			return nil
		}
		if !matchFilePatterns(rel, patterns) {
			return nil
		}

		matches, err := searchFileLines(path, matcher, matchCount)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			return nil
		}
		matchedFiles[rel] = true
		remaining := gitGrepMaxCount - matchCount
		if remaining < 0 {
			remaining = 0
		}
		if len(matches) > remaining {
			matches = matches[:remaining]
			truncated = true
		}
		if len(matches) > 0 {
			fileOrder = append(fileOrder, rel)
			fileMatches[rel] = matches
		}
		matchCount += len(matches)
		if matchCount >= gitGrepMaxCount {
			truncated = true
			return errSearchLimitReached
		}
		return nil
	})

	if walkErr != nil && walkErr != errSearchLimitReached {
		if ctx.Err() != nil {
			return "", fmt.Errorf("code_search timed out; try narrowing file_patterns to a more specific path: %w", ctx.Err())
		}
		return "", fmt.Errorf("code_search failed: %w", walkErr)
	}

	if matchCount == 0 {
		return "No matches found", nil
	}

	var sb strings.Builder
	if truncated {
		sb.WriteString(fmt.Sprintf("Note: Showing the first %d matches across %d matching files. Some files are partially shown or omitted entirely. Narrow file_patterns to see the rest.\n", gitGrepMaxCount, len(matchedFiles)))
	}
	for _, path := range fileOrder {
		matches := fileMatches[path]
		sb.WriteString(fmt.Sprintf("File: %s\nMatch lines: %d\n", path, len(matches)))
		for _, m := range matches {
			sb.WriteString(fmt.Sprintf("%d|%s\n", m.lineNum, m.content))
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

var errSearchLimitReached = fmt.Errorf("search result limit reached")

type searchMatch struct {
	lineNum int
	content string
}

type searchMatcher func(line string) bool

func buildSearchMatcher(searchText string, caseSensitive bool, useRegexp bool) (searchMatcher, error) {
	if useRegexp {
		pattern := searchText
		if !caseSensitive {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		return re.MatchString, nil
	}
	if caseSensitive {
		return func(line string) bool { return strings.Contains(line, searchText) }, nil
	}
	lower := strings.ToLower(searchText)
	return func(line string) bool { return strings.Contains(strings.ToLower(line), lower) }, nil
}

// matchFilePatterns mirrors the pathspec semantics of the git-grep backend:
// a pattern without a slash matches against the basename, otherwise against
// the full slash-separated relative path or any trailing path segment.
func matchFilePatterns(rel string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	base := rel
	if idx := strings.LastIndexByte(rel, '/'); idx >= 0 {
		base = rel[idx+1:]
	}
	for _, pat := range patterns {
		if !strings.Contains(pat, "/") {
			if ok, err := doublestar.Match(pat, base); err == nil && ok {
				return true
			}
			continue
		}
		if ok, err := doublestar.Match(pat, rel); err == nil && ok {
			return true
		}
		// Allow anchored-in-the-middle matches, e.g. pattern "src/*.go"
		// should match "a/src/b.go" only via the suffix rule below.
		if strings.HasSuffix(rel, "/"+strings.TrimSuffix(pat, "/")) {
			return true
		}
	}
	return false
}

// searchFileLines scans a file line by line, returning up to
// (gitGrepMaxCount - alreadyMatched) matching lines. Files that look binary
// (a NUL byte in the first 8 KiB) are skipped, matching git grep's default.
func searchFileLines(path string, matcher searchMatcher, alreadyMatched int) ([]searchMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Size the buffer above the 8000-byte sniff window so Peek can fill it.
	br := bufio.NewReaderSize(f, 8192)
	if isBinaryReader(br) {
		return nil, nil
	}

	var out []searchMatch
	budget := gitGrepMaxCount - alreadyMatched
	if budget < 0 {
		budget = 0
	}
	lineNum := 0
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lineNum++
			trimmed := strings.TrimSuffix(line, "\n")
			trimmed = strings.TrimSuffix(trimmed, "\r")
			if matcher(trimmed) {
				out = append(out, searchMatch{lineNum: lineNum, content: trimmed})
				if len(out) >= budget {
					return out, nil
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
	}
}

func isBinaryReader(br *bufio.Reader) bool {
	peek, err := br.Peek(8000)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		// Short files return io.EOF with the available bytes.
		if len(peek) == 0 {
			return false
		}
	}
	return bytes.IndexByte(peek, 0) >= 0
}
