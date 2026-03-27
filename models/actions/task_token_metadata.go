// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"encoding/base64"

	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unit"
	"code.gitea.io/gitea/modules/json"
)

// TaskTokenMetadata carries the task context needed to authorize Actions API/git/LFS
// requests without reloading the task row on every request.
type TaskTokenMetadata struct {
	TaskID            int64                               `json:"task_id"`
	RepoID            int64                               `json:"repo_id"`
	OwnerID           int64                               `json:"owner_id"`
	TaskRepoIsPrivate bool                                `json:"task_repo_is_private"`
	IsForkPullRequest bool                                `json:"is_fork_pull_request"`
	TokenPermissions  *repo_model.ActionsTokenPermissions `json:"token_permissions,omitempty"`
}

func NewTaskTokenMetadata(task *ActionTask) *TaskTokenMetadata {
	if task == nil || task.Job == nil || task.Job.Repo == nil {
		return nil
	}

	return &TaskTokenMetadata{
		TaskID:            task.ID,
		RepoID:            task.RepoID,
		OwnerID:           task.Job.Repo.OwnerID,
		TaskRepoIsPrivate: task.Job.Repo.IsPrivate,
		IsForkPullRequest: task.IsForkPullRequest,
		TokenPermissions:  task.Job.TokenPermissions,
	}
}

func EncodeTaskTokenMetadata(meta *TaskTokenMetadata) (string, error) {
	if meta == nil {
		return "", nil
	}

	bs, err := json.Marshal(meta)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bs), nil
}

func DecodeTaskTokenMetadata(encoded string) (*TaskTokenMetadata, error) {
	if encoded == "" {
		return nil, nil //nolint:nilnil // not applicable
	}

	bs, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}

	meta := new(TaskTokenMetadata)
	if err := json.Unmarshal(bs, meta); err != nil {
		return nil, err
	}
	return meta, nil
}

// ComputeTaskTokenPermissionsFromMetadata computes the effective permissions for a job
// token against the target repository using token metadata instead of reloading the task row.
func ComputeTaskTokenPermissionsFromMetadata(ctx context.Context, taskMeta *TaskTokenMetadata, taskRepo, targetRepo *repo_model.Repository) (ret repo_model.ActionsTokenPermissions, err error) {
	if err := taskRepo.LoadUnits(ctx); err != nil {
		return ret, err
	}

	repoActionsCfg := taskRepo.MustGetUnit(ctx, unit.TypeActions).ActionsConfig()
	ownerActionsCfg, err := GetOwnerActionsConfig(ctx, taskMeta.OwnerID)
	if err != nil {
		return ret, err
	}

	isCrossRepo := taskMeta.RepoID != targetRepo.ID
	return applyTokenPermissions(taskMeta.TokenPermissions, repoActionsCfg, ownerActionsCfg, taskMeta.IsForkPullRequest, isCrossRepo), nil
}
