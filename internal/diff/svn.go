// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/svncmd"
)

// IsValidSVNRevision reports whether ref is usable as an svn revision
// specifier: a non-negative revision number, or one of the keyword revisions
// HEAD, BASE, COMMITTED and PREV (case-insensitive). Date revisions ({...})
// are rejected: they resolve on the server at query time, so the revision a
// review froze and the revision the diff used could silently differ.
func IsValidSVNRevision(ref string) bool {
	if isAllDigits(ref) {
		return true
	}
	switch strings.ToUpper(ref) {
	case "HEAD", "BASE", "COMMITTED", "PREV":
		return true
	}
	return false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// SVNProvider retrieves and parses diffs from an Apache Subversion working
// copy. It shells out to svn diff and converts the output into git-style
// unified diff text so the shared parser and review pipeline apply unchanged.
//
// Supported inputs mirror the git provider: workspace changes, a single
// revision (--commit N, diffed as r(N-1):rN), and a revision range
// (--from M --to N, diffed as rM:rN). Revision keywords are frozen to their
// concrete numbers before the diff runs, so the diff and every follow-up
// svn cat read pin the same revisions.
type SVNProvider struct {
	wcDir  string
	mode   Mode
	from   string
	to     string
	commit string
	runner *svncmd.Runner
}

// NewSVNProvider creates an SVNProvider for range mode: diff of r(from):r(to).
func NewSVNProvider(wcDir, from, to string, runner *svncmd.Runner) *SVNProvider {
	return &SVNProvider{wcDir: wcDir, mode: ModeRange, from: from, to: to, runner: runner}
}

// NewSVNCommitProvider creates an SVNProvider for commit mode: the changes
// introduced by a single revision, diffed against its predecessor.
func NewSVNCommitProvider(wcDir, commit string, runner *svncmd.Runner) *SVNProvider {
	return &SVNProvider{wcDir: wcDir, mode: ModeCommit, commit: commit, runner: runner}
}

// NewSVNWorkspaceProvider creates an SVNProvider for workspace mode: current
// uncommitted changes, including unversioned files.
func NewSVNWorkspaceProvider(wcDir string, runner *svncmd.Runner) *SVNProvider {
	return &SVNProvider{wcDir: wcDir, mode: ModeWorkspace, runner: runner}
}

// IsRangeMode returns true when comparing two revisions.
func (p *SVNProvider) IsRangeMode() bool { return p.mode == ModeRange }

// IsCommitMode returns true when analyzing a single revision.
func (p *SVNProvider) IsCommitMode() bool { return p.mode == ModeCommit }

// MergeBase satisfies the same surface as the git Provider. SVN has no
// merge-base concept (a range diff is r(from):r(to) directly), so it always
// returns "".
func (p *SVNProvider) MergeBase(ctx context.Context) string { return "" }

// GetDiff returns the changes available to a review as parsed model.Diff structs.
func (p *SVNProvider) GetDiff(ctx context.Context) ([]model.Diff, error) {
	set, err := p.GetDiffSet(ctx)
	if err != nil {
		return nil, err
	}
	return set.Included, nil
}

// GetDiffSet returns both reviewable diffs and those excluded by the
// provider's built-in directory rules, mirroring the git Provider.
func (p *SVNProvider) GetDiffSet(ctx context.Context) (DiffSet, error) {
	var diffArgs []string
	var ref string // revision new-file content is read at; empty = working copy
	switch p.mode {
	case ModeRange:
		from, err := p.resolveRev(ctx, p.from)
		if err != nil {
			return DiffSet{}, fmt.Errorf("resolve --from revision %q: %w", p.from, err)
		}
		to, err := p.resolveRev(ctx, p.to)
		if err != nil {
			return DiffSet{}, fmt.Errorf("resolve --to revision %q: %w", p.to, err)
		}
		diffArgs = []string{"diff", "--internal-diff", "-r", from + ":" + to}
		ref = to
	case ModeCommit:
		head, err := p.resolveRev(ctx, p.commit)
		if err != nil {
			return DiffSet{}, fmt.Errorf("resolve --commit revision %q: %w", p.commit, err)
		}
		diffArgs = []string{"diff", "--internal-diff", "-c", head}
		ref = head
	case ModeWorkspace:
		diffArgs = []string{"diff", "--internal-diff"}
	default:
		return DiffSet{}, fmt.Errorf("unsupported diff mode %d", p.mode)
	}

	out, stderr, err := p.runSVNSplit(ctx, diffArgs...)
	if err != nil {
		return DiffSet{}, svnFailure("svn diff", stderr, err)
	}
	var combined strings.Builder
	combined.WriteString(svnDiffToGit(out))

	if p.mode == ModeWorkspace {
		unversioned, err := p.unversionedFileDiffs(ctx)
		if err != nil {
			return DiffSet{}, err
		}
		for _, ud := range unversioned {
			combined.WriteString(ud)
			combined.WriteString("\n\n")
		}
	}

	diffs, err := ParseDiffTextWithReader(ctx, combined.String(), p.wcDir, ref, nil, p.catReader(ref))
	if err != nil {
		return DiffSet{}, err
	}
	return partitionDiffs(p.wcDir, diffs), nil
}

// ResolveInput freezes this run's revision endpoints by asking svn, following
// the same contract as the git Provider: unresolvable endpoints are reported
// as empty fields, never fabricated.
//
//   - range:     base = r(from), head = r(to), exact_range = base..head.
//   - commit:    head = r(commit); base = head-1, the revision svn diff -c
//     compares against. Revision 1 has no predecessor, so base stays empty.
//   - workspace: base = the working copy's current revision; head stays empty
//     (a workspace has no immutable head).
func (p *SVNProvider) ResolveInput(ctx context.Context) InputResolution {
	switch p.mode {
	case ModeRange:
		base, berr := p.resolveRev(ctx, p.from)
		head, herr := p.resolveRev(ctx, p.to)
		if berr != nil || herr != nil {
			return InputResolution{}
		}
		return InputResolution{ResolvedBase: base, ResolvedHead: head, ExactRange: base + ".." + head}
	case ModeCommit:
		head, err := p.resolveRev(ctx, p.commit)
		if err != nil {
			return InputResolution{}
		}
		r := InputResolution{ResolvedHead: head}
		if n, cerr := strconv.Atoi(head); cerr == nil && n > 1 {
			base := strconv.Itoa(n - 1)
			r.ResolvedBase = base
			r.ExactRange = base + ".." + head
		}
		return r
	case ModeWorkspace:
		out, _, err := p.runSVNSplit(ctx, "info", "--show-item", "revision")
		if err != nil {
			return InputResolution{}
		}
		rev := strings.TrimSpace(out)
		if !isAllDigits(rev) {
			return InputResolution{}
		}
		return InputResolution{ResolvedBase: rev}
	default:
		return InputResolution{}
	}
}

// RemoteIdentity returns a stable, credential-free identity string derived
// from the working copy's repository root URL, canonicalized the same way as
// a git remote. It returns "" when the URL cannot be determined.
func (p *SVNProvider) RemoteIdentity(ctx context.Context) string {
	out, err := p.runSVN(ctx, "info", "--show-item", "repos-root-url")
	if err != nil {
		return ""
	}
	return canonicalRemote(firstLine(out))
}

// resolveRev freezes a revision keyword to the concrete revision number svn
// reports for it, so the diff and every follow-up read pin the same revision.
// Numeric revisions pass through unchanged.
func (p *SVNProvider) resolveRev(ctx context.Context, ref string) (string, error) {
	if !IsValidSVNRevision(ref) {
		return "", fmt.Errorf("invalid svn revision %q", ref)
	}
	if isAllDigits(ref) {
		return ref, nil
	}
	out, stderr, err := p.runSVNSplit(ctx, "info", "-r", ref, "--show-item", "revision")
	if err != nil {
		return "", svnFailure("svn info", stderr, err)
	}
	rev := strings.TrimSpace(out)
	if !isAllDigits(rev) {
		return "", fmt.Errorf("svn info returned no numeric revision for %q", ref)
	}
	return rev, nil
}

// catReader returns the ContentReader that reads a working-copy-relative path
// at the given revision via svn cat, or nil when ref is empty (workspace
// mode reads the working tree instead).
func (p *SVNProvider) catReader(ref string) ContentReader {
	if ref == "" {
		return nil
	}
	return func(ctx context.Context, path string) (string, error) {
		return p.catAtRef(ctx, ref, path)
	}
}

func (p *SVNProvider) catAtRef(ctx context.Context, ref, path string) (string, error) {
	if strings.HasPrefix(path, "-") {
		return "", fmt.Errorf("path %q must not start with '-'", path)
	}
	// A trailing "@" pins an empty peg revision; without it svn parses an
	// "@" inside the file name (icon@2x.png, @scope/pkg) as a peg revision
	// and fails with E200004.
	out, err := p.runSVNOutput(ctx, "cat", "-r", ref, "--", path+"@")
	if err != nil {
		return "", fmt.Errorf("svn cat -r %s %s: %w", ref, path, err)
	}
	return string(out), nil
}

func (p *SVNProvider) unversionedFileDiffs(ctx context.Context) ([]string, error) {
	files, err := p.unversionedFilesList(ctx)
	if err != nil {
		return nil, err
	}
	return synthesizeNewFileDiffs(p.wcDir, files), nil
}

// unversionedFilesList returns the working-copy-relative paths svn status
// reports as unversioned ("?"). svn status lists an unversioned directory
// alone, unlike git ls-files which recurses into untracked directories, so
// directories are walked here; otherwise every file inside a new directory
// would silently escape review.
func (p *SVNProvider) unversionedFilesList(ctx context.Context) ([]string, error) {
	out, stderr, err := p.runSVNSplit(ctx, "status")
	if err != nil {
		return nil, svnFailure("svn status", stderr, err)
	}
	patterns := loadGitignorePatterns(p.wcDir)
	var files []string
	for _, rel := range svnStatusUnversioned(out) {
		if isProviderDirExcluded(rel) {
			continue
		}
		info, statErr := os.Stat(filepath.Join(p.wcDir, rel))
		if statErr != nil {
			continue
		}
		if !info.IsDir() {
			// svn status prints native separators on Windows; normalize so
			// ignore matching and synthesized diff headers stay slash-based.
			rel = filepath.ToSlash(rel)
			if !isPathExcluded(rel, patterns) {
				files = append(files, rel)
			}
			continue
		}
		_ = filepath.WalkDir(filepath.Join(p.wcDir, rel), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			relPath, rerr := filepath.Rel(p.wcDir, path)
			if rerr != nil {
				return nil
			}
			relPath = filepath.ToSlash(relPath)
			if d.IsDir() {
				if isProviderDirExcluded(relPath) {
					return filepath.SkipDir
				}
				return nil
			}
			if !isPathExcluded(relPath, patterns) {
				files = append(files, relPath)
			}
			return nil
		})
	}
	return files, nil
}

func (p *SVNProvider) runSVN(ctx context.Context, args ...string) (string, error) {
	if p.runner != nil {
		return p.runner.Run(ctx, p.wcDir, args...)
	}
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = p.wcDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (p *SVNProvider) runSVNOutput(ctx context.Context, args ...string) ([]byte, error) {
	if p.runner != nil {
		return p.runner.Output(ctx, p.wcDir, args...)
	}
	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = p.wcDir
	return cmd.Output()
}

// runSVNSplit mirrors the git Provider's runGitSplit: svn writes the diff to
// stdout and its diagnosis to stderr, and only stderr is safe to quote back
// to the user.
func (p *SVNProvider) runSVNSplit(ctx context.Context, args ...string) (string, string, error) {
	if p.runner != nil {
		stdout, stderr, err := p.runner.RunSplit(ctx, p.wcDir, args...)
		if ctx.Err() != nil && err != nil {
			return "", "", ctx.Err()
		}
		return stdout, stderr, err
	}

	cmd := exec.CommandContext(ctx, "svn", args...)
	cmd.Dir = p.wcDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil && err != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == -1 {
		return "", "", ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

// svnFailure builds an error that carries svn's own stderr diagnosis, the
// same contract gitFailure gives the git commands.
func svnFailure(op, stderr string, err error) error {
	diag := strings.TrimSpace(stderr)
	if diag == "" {
		return fmt.Errorf("%s failed: %w", op, err)
	}
	if len(diag) > gitDiagLimit {
		diag = diag[len(diag)-gitDiagLimit:]
		for len(diag) > 0 && !utf8.RuneStart(diag[0]) {
			diag = diag[1:]
		}
		diag = "..." + diag
	}
	return fmt.Errorf("%s failed: %w: %s", op, err, diag)
}

// svnStatusUnversioned extracts the paths svn status marks unversioned ("?").
// A status line is seven flag columns, one space, then the path; the path is
// taken verbatim because leading and trailing spaces are legal filename bytes.
func svnStatusUnversioned(statusOut string) []string {
	var paths []string
	for _, line := range splitDiffLines(statusOut) {
		if len(line) > 8 && line[0] == '?' {
			paths = append(paths, line[8:])
		}
	}
	return paths
}

// svnDiffToGit converts svn diff output into the git-style unified diff
// format the shared parser understands.
//
// svn prints one block per changed file: an "Index: <path>" banner, a line of
// "=", the "---"/"+++" headers annotated with "(revision N)", "(working
// copy)" or "(nonexistent)", then unified hunks. Binary files get a "Cannot
// display" notice instead of hunks, and property changes follow the content
// hunks under a "Property changes on:" section. Blocks with neither content
// hunks nor a binary notice (property-only changes) are dropped: they carry
// no reviewable content.
func svnDiffToGit(text string) string {
	type fileBlock struct {
		path      string
		isNew     bool
		isDeleted bool
		binary    bool
		inHunks   bool
		inProps   bool
		hunks     strings.Builder
	}
	var blocks []*fileBlock
	var cur *fileBlock
	for _, line := range splitDiffLines(text) {
		if strings.HasPrefix(line, "Index: ") {
			cur = &fileBlock{path: strings.TrimPrefix(line, "Index: ")}
			blocks = append(blocks, cur)
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(line, "==="):
			// Banner underline under "Index:".
		case strings.HasPrefix(line, "@@"):
			// Once hunks start, "--- "/"+++ " prefixed lines are deleted/added
			// content (e.g. a removed SQL "-- note" comment shows as
			// "--- note"), not file headers; the header pair always precedes
			// the first "@@" of a block.
			cur.inHunks = true
			cur.hunks.WriteString(line)
			cur.hunks.WriteString("\n")
		case !cur.inHunks && strings.HasPrefix(line, "--- "):
			if svnHeaderAnnotation(line) == "nonexistent" {
				cur.isNew = true
			}
		case !cur.inHunks && strings.HasPrefix(line, "+++ "):
			if svnHeaderAnnotation(line) == "nonexistent" {
				cur.isDeleted = true
			}
		case line == "Cannot display: file marked as a binary type.":
			cur.binary = true
		case strings.HasPrefix(line, "Property changes on: "):
			cur.inProps = true
		case cur.inProps:
			// Property changes (including their "## ... ##" pseudo-hunks) are
			// not reviewable file content; skip until the next Index block.
		default:
			cur.hunks.WriteString(line)
			cur.hunks.WriteString("\n")
		}
	}

	var out strings.Builder
	for _, b := range blocks {
		hunks := b.hunks.String()
		if !b.binary && strings.TrimSpace(hunks) == "" {
			continue
		}
		path := b.path
		out.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
		switch {
		case b.isNew:
			out.WriteString("new file mode 100644\n")
		case b.isDeleted:
			out.WriteString("deleted file mode 100644\n")
		}
		if b.binary {
			out.WriteString(fmt.Sprintf("Binary files a/%s and b/%s differ\n", path, path))
			continue
		}
		if b.isNew {
			out.WriteString("--- /dev/null\n")
		} else {
			out.WriteString(fmt.Sprintf("--- a/%s\n", path))
		}
		if b.isDeleted {
			out.WriteString("+++ /dev/null\n")
		} else {
			out.WriteString(fmt.Sprintf("+++ b/%s\n", path))
		}
		out.WriteString(hunks)
		out.WriteString("\n")
	}
	return out.String()
}

// svnHeaderAnnotation extracts the parenthesized annotation from an svn
// "---"/"+++" header line: "revision 42", "working copy" or "nonexistent".
// The annotation is separated from the path by a tab, so paths containing
// spaces stay intact.
func svnHeaderAnnotation(line string) string {
	body := line[4:]
	tab := strings.IndexByte(body, '\t')
	if tab < 0 {
		return ""
	}
	ann := strings.TrimSpace(body[tab+1:])
	ann = strings.TrimPrefix(ann, "(")
	ann = strings.TrimSuffix(ann, ")")
	return ann
}
