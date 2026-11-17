// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"fmt"
	"strings"

	git_model "gitea.dev/models/git"
	issues_model "gitea.dev/models/issues"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/commitstatus"
	"gitea.dev/modules/git"
	"gitea.dev/modules/git/gitcmd"
	"gitea.dev/modules/git/gitrepo"
	"gitea.dev/modules/log"
	repo_module "gitea.dev/modules/repository"
	"gitea.dev/modules/setting"
)

// CreateMergeQueueCommit merges the pull request onto baseCommitID and stores the result at targetRef in the base repo.
// The ref is written without running hooks, like GitHub's queue branches it is not a user branch.
func CreateMergeQueueCommit(ctx context.Context, pr *issues_model.PullRequest, doer *user_model.User, mergeStyle repo_model.MergeStyle, baseCommitID, expectedHeadCommitID, targetRef string) (string, error) {
	if err := pr.LoadBaseRepo(ctx); err != nil {
		return "", err
	}
	if err := pr.LoadIssue(ctx); err != nil {
		return "", err
	}
	baseGitRepo, closer, err := git.RepositoryFromContextOrOpen(ctx, pr.BaseRepo)
	if err != nil {
		return "", err
	}
	defer closer.Close()
	message, body, err := GetDefaultMergeMessage(ctx, baseGitRepo, pr, mergeStyle)
	if err != nil {
		return "", err
	}
	if body != "" {
		message += "\n\n" + body
	}

	mergeCtx, cancel, err := createTemporaryRepoForMerge(ctx, pr, doer, expectedHeadCommitID, baseCommitID)
	if err != nil {
		return "", err
	}
	defer cancel()

	if err := doMergeStyle(mergeCtx, mergeStyle, message); err != nil {
		return "", err
	}
	commitID, err := git.GetFullCommitID(ctx, mergeCtx.tmpRepo, tmpRepoBaseBranch)
	if err != nil {
		return "", fmt.Errorf("CreateMergeQueueCommit: %w", err)
	}
	if setting.LFS.StartServer {
		if err := LFSPush(ctx, mergeCtx.tmpBasePath, mergeCtx.tmpRepo, commitID, baseCommitID, pr); err != nil {
			return "", err
		}
	}

	if err := gitcmd.NewCommand("fetch", "--no-tags").
		AddDynamicArguments(mergeCtx.tmpBasePath, "+"+git.BranchPrefix+tmpRepoBaseBranch+":"+targetRef).
		WithRepo(pr.BaseRepo).RunWithStderr(ctx); err != nil {
		return "", fmt.Errorf("CreateMergeQueueCommit: fetch into %s: %w\n%s", targetRef, err, err.Stderr())
	}
	return commitID, nil
}

// MergeQueueRequiredContexts adapts required contexts to queue commits: Actions name their statuses after the event,
// so a required "CI / test (pull_request)" is satisfied by "CI / test (merge_group)" like GitHub's event-less check names
func MergeQueueRequiredContexts(required []string) []string {
	adapted := make([]string, 0, len(required))
	for _, ctx := range required {
		for _, suffix := range []string{" (pull_request)", " (pull_request_target)"} {
			if base, ok := strings.CutSuffix(ctx, suffix); ok {
				ctx = base + " (merge_group)"
				break
			}
		}
		adapted = append(adapted, ctx)
	}
	return adapted
}

// GetMergeQueueCommitStatusState evaluates the required checks of the branch on a queue commit
func GetMergeQueueCommitStatusState(ctx context.Context, repo *repo_model.Repository, pb *git_model.ProtectedBranch, sha string) (commitstatus.CommitStatusState, error) {
	required, err := EffectiveRequiredContexts(ctx, repo, pb)
	if err != nil {
		return "", err
	}
	if len(required) == 0 {
		return commitstatus.CommitStatusSuccess, nil
	}
	statuses, err := GetLatestPullCommitStatuses(ctx, repo.ID, sha)
	if err != nil {
		return "", err
	}
	return MergeRequiredContextsCommitStatus(statuses, MergeQueueRequiredContexts(required)), nil
}

// MergeQueuePullRequests fast-forwards the base branch to groupHeadSHA and marks the pull requests merged,
// each with the queue commit that added it. The push runs the hooks so the usual push events are sent.
func MergeQueuePullRequests(ctx context.Context, doer *user_model.User, repo *repo_model.Repository, baseBranch string, prs []*issues_model.PullRequest, mergedCommitIDs []string) error {
	last := prs[len(prs)-1]
	for i, pr := range prs {
		if err := recordMergeIntent(ctx, pr, doer, mergedCommitIDs[i], false); err != nil {
			return err
		}
	}
	env := repo_module.FullPushingEnvironment(doer, doer, repo, repo.Name, last.ID, last.Index)
	err := gitcmd.NewCommand("push").
		AddDynamicArguments(gitrepo.RepoLocalPath(repo.CodeStorageRepo()), mergedCommitIDs[len(prs)-1]+":"+git.BranchPrefix+baseBranch).
		WithRepo(repo).WithEnv(env).RunWithStderr(ctx)
	if err != nil {
		for _, pr := range prs {
			if clearErr := clearMergeIntent(ctx, pr); clearErr != nil {
				log.Error("Unable to clear merge intent of %-v: %v", pr, clearErr)
			}
		}
		return fmt.Errorf("MergeQueuePullRequests: push to %s: %w\n%s", baseBranch, err, err.Stderr())
	}

	for _, pr := range prs {
		if _, err := MarkAsMerged(ctx, pr, pr.MergedCommitID, pr.MergedUnix, doer, pr.Status); err != nil {
			log.Error("MarkAsMerged %-v: %v", pr, err)
		}
	}
	addTestPullRequestTaskAfterWebOperation(last, doer)
	return nil
}
