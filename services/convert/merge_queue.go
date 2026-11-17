// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"context"

	"gitea.dev/models/perm"
	access_model "gitea.dev/models/perm/access"
	pull_model "gitea.dev/models/pull"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	api "gitea.dev/modules/structs"
)

// ToMergeGroupPayload builds the payload of a merge_group event, reason is only set for destroyed
func ToMergeGroupPayload(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, entry *pull_model.MergeQueueEntry, action api.HookMergeGroupAction, reason string) (*api.MergeGroupPayload, error) {
	gitRepo, closer, err := git.RepositoryFromContextOrOpen(ctx, repo)
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	group := &api.MergeGroup{
		HeadSHA: entry.GroupHeadSHA,
		HeadRef: entry.GroupRef,
		BaseSHA: entry.BaseSHA,
		BaseRef: git.RefNameFromBranch(entry.BaseBranch).String(),
	}
	// the queue commit is already gone when the group is destroyed
	if commit, err := gitRepo.GetCommit(ctx, entry.GroupHeadSHA); err == nil {
		group.HeadCommit = ToPayloadCommit(ctx, repo, commit)
	}
	return &api.MergeGroupPayload{
		Action:     action,
		Reason:     reason,
		MergeGroup: group,
		Repo:       ToRepo(ctx, repo, access_model.Permission{AccessMode: perm.AccessModeOwner}),
		Sender:     ToUser(ctx, doer, nil),
	}, nil
}
