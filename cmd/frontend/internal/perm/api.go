package perm

import (
	"context"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/types"
	"github.com/sourcegraph/sourcegraph/pkg/actor"
	"github.com/sourcegraph/sourcegraph/pkg/api"
)

var MockFilter func(ctx context.Context, repos []*types.Repo, p P) ([]*types.Repo, error)

// Filter is the enforcement mechanism for repository permissions. It accepts a list of repositories
// and a permission type `p` and returns a subset of those repositories (no guarantee on order) for
// which the currently authenticated user has the specified permission.
func Filter(ctx context.Context, repos []*types.Repo, p P) ([]*types.Repo, error) {
	if MockFilter != nil {
		return MockFilter(ctx, repos, p)
	}

	if len(repos) == 0 {
		return repos, nil
	}
	if isInternalActor(ctx) {
		return repos, nil
	}

	filteredURIs, acceptAll, err := getFilteredRepoURIs(ctx, ToRepos(repos), p)
	if err != nil {
		return nil, err
	}
	if acceptAll {
		return repos, nil
	}

	filteredRepos := make([]*types.Repo, 0, len(filteredURIs))
	for _, repo := range repos {
		if _, ok := filteredURIs[repo.URI]; ok {
			filteredRepos = append(filteredRepos, repo)
		}
	}
	return filteredRepos, nil
}

// isInternalActor returns true if the actor represents an internal agent (i.e., non-user-bound
// request that originates from within Sourcegraph itself).
//
// 🚨 SECURITY: internal requests bypass authz provider permissions checks, so correctness is
// important here.
func isInternalActor(ctx context.Context) bool {
	return actor.FromContext(ctx).Internal
}

// getFilteredRepoURIs returns a subset of repos (`accepted`) filtered by whether the current user
// has the specified permission on those repos. If the return value `acceptAll` is true, all repos
// in the input set should be regarded as a member of the filtered set and the value of `accepted`
// should be disregarded.
func getFilteredRepoURIs(ctx context.Context, repos map[Repo]struct{}, p P) (
	accepted map[api.RepoURI]struct{}, acceptAll bool, err error,
) {
	accepted = make(map[api.RepoURI]struct{})
	unverified := make(map[Repo]struct{})
	for repo := range repos {
		unverified[repo] = struct{}{}
	}

	providersMu.RLock()
	defer providersMu.RUnlock()
Outer:
	for _, authnProvider := range authnProviders {
		id, isAdmin, err := authnProvider.CurrentIdentity(ctx)
		if err != nil {
			return nil, false, err
		}
		if isAdmin {
			return nil, true, nil
		}
		for _, identityToAuthzIDMapper := range identityToAuthzIDMappers {
			for _, authzProvider := range authzProviders {
				if len(unverified) == 0 {
					break Outer
				}

				authzID := identityToAuthzIDMapper.AuthzID(id, authzProvider)
				perms, err := authzProvider.RepoPerms(ctx, authzID, unverified)
				if err != nil {
					return nil, false, err
				}

				newUnverified := make(map[Repo]struct{})
				for unverifiedRepo := range unverified {
					repoPerms, ok := perms[unverifiedRepo.URI]
					if !ok {
						newUnverified[unverifiedRepo] = struct{}{}
						continue
					}
					if repoPerms[p] {
						accepted[unverifiedRepo.URI] = struct{}{}
					}
				}
				unverified = newUnverified
			}
		}
	}

	if permissionsAllowByDefault {
		for r := range unverified {
			accepted[r.URI] = struct{}{}
		}
	}

	return accepted, false, nil
}
