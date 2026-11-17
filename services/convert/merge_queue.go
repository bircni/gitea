// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"context"

	git_model "gitea.dev/models/git"
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

// ToBranchProtectionMergeQueue converts the merge queue settings of a branch protection, filling in defaults
func ToBranchProtectionMergeQueue(cfg git_model.MergeQueueConfig) *api.BranchProtectionMergeQueue {
	cfg = cfg.Normalized()
	return &api.BranchProtectionMergeQueue{
		MergeMethod:                  string(cfg.MergeMethod),
		MaxEntriesToBuild:            cfg.MaxEntriesToBuild,
		MinEntriesToMerge:            cfg.MinEntriesToMerge,
		MaxEntriesToMerge:            cfg.MaxEntriesToMerge,
		MinEntriesToMergeWaitMinutes: cfg.MinEntriesToMergeWaitMinutes,
		GroupingStrategy:             string(cfg.GroupingStrategy),
		CheckResponseTimeoutMinutes:  cfg.CheckResponseTimeoutMinutes,
	}
}

// ToMergeQueueEntry converts a merge queue entry
func ToMergeQueueEntry(ctx context.Context, entry *pull_model.MergeQueueEntry, position int, prIndex int64, doer *user_model.User) *api.MergeQueueEntry {
	_, enqueuer, _ := user_model.GetPossibleUserByID(ctx, entry.EnqueuerID)
	return &api.MergeQueueEntry{
		Position:          position,
		State:             string(entry.State),
		PullRequestIndex:  prIndex,
		BaseBranch:        entry.BaseBranch,
		HeadSHA:           entry.HeadSHA,
		MergeGroupHeadSHA: entry.GroupHeadSHA,
		MergeGroupHeadRef: entry.GroupRef,
		Enqueuer:          ToUser(ctx, enqueuer, doer),
		EnqueuedAt:        entry.CreatedUnix.AsTime(),
	}
}
