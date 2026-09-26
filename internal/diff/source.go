// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"

	"github.com/alibaba/open-code-review/internal/model"
)

// Source is the review-input surface the agent needs from a diff provider.
// Provider (git) and SVNProvider (Subversion) both implement it, so the
// review pipeline is agnostic to the version control system underneath.
type Source interface {
	// GetDiff returns the changes available to a review.
	GetDiff(ctx context.Context) ([]model.Diff, error)
	// GetDiffSet additionally reports the diffs excluded by provider-level
	// directory rules, for callers that account for the whole changeset.
	GetDiffSet(ctx context.Context) (DiffSet, error)
	// ResolveInput freezes this run's immutable revision endpoints.
	ResolveInput(ctx context.Context) InputResolution
	// RemoteIdentity returns a stable, credential-free repository identity,
	// or "" when none is available.
	RemoteIdentity(ctx context.Context) string
}

var (
	_ Source = (*Provider)(nil)
	_ Source = (*SVNProvider)(nil)
)
