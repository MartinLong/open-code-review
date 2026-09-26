// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ocr",
	Short: "OpenCodeReview - AI-Powered Code Review CLI",
	Long: `OpenCodeReview - AI-Powered Code Review CLI

An AI-powered code review tool that reads git diffs, sends them to a
configurable LLM service, and generates review comments.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// Runs for every subcommand, always before any RunE: validate --color once
	// flags are parsed, then resolve the color decision from them, and for
	// commands that need git, check the installed git version (warning, not
	// failing).
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := validateColorMode(colorMode); err != nil {
			return err
		}
		colorEnabled = resolveColor()
		if !commandNeedsGit(cmd) || reviewRunsOnSVN(cmd) {
			return nil
		}
		if _, err := exec.LookPath("git"); err != nil {
			// Without a git binary the version check can only fail; git-mode
			// runs report the same problem when they invoke git themselves.
			return nil
		}
		if err := gitcmd.CheckGitVersion(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		v, _ := cmd.Flags().GetBool("version")
		if v {
			printVersion()
			return nil
		}
		return cmd.Help()
	},
}

func init() {
	rootCmd.SetFlagErrorFunc(flagErrorWithSuggestion)
	rootCmd.Flags().BoolP("version", "V", false, "version for ocr")
	addColorFlags(rootCmd)

	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(reviewCmd)
	rootCmd.AddCommand(scanCmd)
	rootCmd.AddCommand(delegateCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(llmCmd)
	rootCmd.AddCommand(rulesCmd)
	rootCmd.AddCommand(viewerCmd)
	rootCmd.AddCommand(completionCmd)
}

// reviewRunsOnSVN reports whether this invocation was pinned to SVN via
// --vcs=svn, in which case the git version check is irrelevant.
func reviewRunsOnSVN(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("vcs")
	if f == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(f.Value.String()), "svn")
}

func commandNeedsGit(cmd *cobra.Command) bool {
	topLevel := cmd
	for topLevel.Parent() != nil && topLevel.Parent().Parent() != nil {
		topLevel = topLevel.Parent()
	}
	switch topLevel.Name() {
	case "review", "scan", "delegate":
		return true
	default:
		return false
	}
}

func versionString() string {
	s := fmt.Sprintf("open-code-review %s", Version)
	if GitCommit != "" {
		s += fmt.Sprintf(" (%s)", GitCommit)
	}
	s += fmt.Sprintf(" %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if BuildDate != "" {
		s += fmt.Sprintf("built at: %s\n", BuildDate)
	}
	s += "https://github.com/alibaba/open-code-review\n"
	return s
}
