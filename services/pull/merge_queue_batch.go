// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	issues_model "gitea.dev/models/issues"
	repo_model "gitea.dev/models/repo"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/git"
	"gitea.dev/modules/git/gitcmd"
	"gitea.dev/modules/git/gitrepo"
	repo_module "gitea.dev/modules/repository"
)

// mergeQueueTempRefPrefix is the ref namespace holding merge queue synthetic combined commits, kept
// outside refs/heads/* so batches are never listed as branches or touched by branch-protection/
// branch-deletion logic.
const mergeQueueTempRefPrefix = "refs/for-merge-queue/"

// MergeQueueTempRefName returns the ref name used to hold the synthetic combined commit built for a
// merge queue batch while it is being tested.
func MergeQueueTempRefName(baseBranch string, batchID int64) string {
	return fmt.Sprintf("%s%s/%d", mergeQueueTempRefPrefix, baseBranch, batchID)
}

// ParseMergeQueueTempRef reports whether ref is a merge queue synthetic ref, and if so extracts the
// base branch name and batch id encoded in it. Used by the Actions `merge_group` trigger to identify
// pushes that should fire that event instead of (not in addition to) a normal `push`.
func ParseMergeQueueTempRef(ref string) (baseBranch string, batchID int64, ok bool) {
	rest, found := strings.CutPrefix(ref, mergeQueueTempRefPrefix)
	if !found {
		return "", 0, false
	}
	idx := strings.LastIndex(rest, "/")
	if idx < 0 {
		return "", 0, false
	}
	id, err := strconv.ParseInt(rest[idx+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return rest[:idx], id, true
}

// DeleteMergeQueueTempRef deletes the synthetic preview ref for a batch once it has reached a
// terminal state.
func DeleteMergeQueueTempRef(ctx context.Context, repo *repo_model.Repository, baseBranch string, batchID int64) error {
	gitRepo, err := git.OpenRepository(ctx, repo)
	if err != nil {
		return fmt.Errorf("OpenRepository: %w", err)
	}
	defer gitRepo.Close()

	tempRef := MergeQueueTempRefName(baseBranch, batchID)
	if err := git.RemoveRef(ctx, gitRepo, tempRef); err != nil {
		return fmt.Errorf("failed to delete merge queue preview ref %s: %w", tempRef, err)
	}
	return nil
}

type mergeQueueBatchContext struct {
	context.Context
	tmpBasePath string
	tmpRepo     git.RepositoryFacade
	outbuf      *bytes.Buffer
	env         []string
}

func (c *mergeQueueBatchContext) run(cmd *gitcmd.Command) error {
	c.outbuf.Reset()
	err := cmd.WithEnv(c.env).WithRepo(c.tmpRepo).WithStdoutBuffer(c.outbuf).RunWithStderr(c.Context)
	if err != nil {
		return fmt.Errorf("%w\n%s\n%s", err, c.outbuf.String(), err.Stderr())
	}
	return nil
}

// BuildMergeQueueBatchPreviewCommit builds, on top of the base branch's *current* tip, the commit that
// sequentially merging/rebasing every pr in prs (in order) with mergeStyle would actually produce, and
// pushes it to a synthetic ref (MergeQueueTempRefName) in the base repository so required status
// checks can run against it before any real merge happens. It never touches the real base branch.
//
// For rebase/rebase-merge styles each PR's own commits are replayed on top of the *previous PR's
// replayed result* (not merged pairwise), so the tested tree is exactly what sequentially rebase-
// merging the PRs for real would produce.
//
// Every commit created here is unsigned (--no-gpg-sign): this is only a test artifact used to trigger
// CI, never the commit that actually lands - the real, correctly signed commits are produced later by
// Merge() for each PR in order once the batch's checks pass.
func BuildMergeQueueBatchPreviewCommit(ctx context.Context, prs []*issues_model.PullRequest, doer *user_model.User, mergeStyle repo_model.MergeStyle, batchID int64) (commitID string, err error) {
	if len(prs) == 0 {
		return "", fmt.Errorf("merge queue batch has no pull requests")
	}
	basePR := prs[0]
	if err := basePR.LoadBaseRepo(ctx); err != nil {
		return "", fmt.Errorf("LoadBaseRepo: %w", err)
	}

	tmpBasePath, tmpRepo, cancel, err := repo_module.CreateTemporaryGitRepo("mergequeue")
	if err != nil {
		return "", fmt.Errorf("CreateTemporaryGitRepo: %w", err)
	}
	defer cancel()

	sig := doer.NewGitSig()
	commitTimeStr := time.Now().Format(time.RFC3339)
	bctx := &mergeQueueBatchContext{
		Context:     ctx,
		tmpBasePath: tmpBasePath,
		tmpRepo:     tmpRepo,
		outbuf:      &bytes.Buffer{},
		env: append(os.Environ(),
			"GIT_AUTHOR_NAME="+sig.Name,
			"GIT_AUTHOR_EMAIL="+sig.Email,
			"GIT_AUTHOR_DATE="+commitTimeStr,
			"GIT_COMMITTER_NAME="+sig.Name,
			"GIT_COMMITTER_EMAIL="+sig.Email,
			"GIT_COMMITTER_DATE="+commitTimeStr,
		),
	}

	if err := git.InitRepositoryLocal(ctx, tmpBasePath, false, basePR.BaseRepo.ObjectFormatName); err != nil {
		return "", fmt.Errorf("InitRepositoryLocal: %w", err)
	}

	addAlternates := func(cacheRepoPath string) error {
		altFile := filepath.Join(tmpBasePath, ".git", "objects", "info", "alternates")
		f, err := os.OpenFile(altFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open alternates file: %w", err)
		}
		defer f.Close()
		if _, err := fmt.Fprintln(f, filepath.Join(cacheRepoPath, "objects")); err != nil {
			return fmt.Errorf("write alternates file: %w", err)
		}
		return nil
	}

	baseRepoPath := gitrepo.RepoLocalPath(basePR.BaseRepo.CodeStorageRepo())
	if err := addAlternates(baseRepoPath); err != nil {
		return "", err
	}
	if err := bctx.run(gitcmd.NewCommand("remote", "add", "origin").AddDynamicArguments(baseRepoPath)); err != nil {
		return "", fmt.Errorf("remote add origin: %w", err)
	}
	if err := bctx.run(gitcmd.NewCommand("fetch", "origin", "--no-tags").
		AddDynamicArguments(git.BranchPrefix + basePR.BaseBranch + ":refs/heads/cursor")); err != nil {
		return "", fmt.Errorf("fetch base branch: %w", err)
	}
	if err := bctx.run(gitcmd.NewCommand("checkout", "cursor")); err != nil {
		return "", fmt.Errorf("checkout cursor: %w", err)
	}

	// Disable LFS filters, same as the single-PR merge path, to avoid touching LFS storage during
	// test merges.
	for _, kv := range [][2]string{
		{"filter.lfs.process", ""},
		{"filter.lfs.required", "false"},
		{"filter.lfs.clean", ""},
		{"filter.lfs.smudge", ""},
	} {
		if err := bctx.run(gitcmd.NewCommand("config", "--local").AddDynamicArguments(kv[0], kv[1])); err != nil {
			return "", fmt.Errorf("git config %s: %w", kv[0], err)
		}
	}

	headRemotes := make(map[int64]bool, len(prs))
	objectFormat := git.ObjectFormatFromName(basePR.BaseRepo.ObjectFormatName)

	for i, pr := range prs {
		if err := pr.LoadHeadRepo(ctx); err != nil {
			return "", fmt.Errorf("LoadHeadRepo[PR:%d]: %w", pr.ID, err)
		}

		remoteName := fmt.Sprintf("head_%d", pr.HeadRepoID)
		if !headRemotes[pr.HeadRepoID] {
			headRepoPath := gitrepo.RepoLocalPath(pr.HeadRepo.CodeStorageRepo())
			if err := addAlternates(headRepoPath); err != nil {
				return "", err
			}
			if err := bctx.run(gitcmd.NewCommand("remote", "add").AddDynamicArguments(remoteName, headRepoPath)); err != nil {
				return "", fmt.Errorf("remote add %s: %w", remoteName, err)
			}
			headRemotes[pr.HeadRepoID] = true
		}

		var headRef string
		switch {
		case pr.Flow == issues_model.PullRequestFlowGithub:
			headRef = git.BranchPrefix + pr.HeadBranch
		case len(pr.HeadCommitID) == objectFormat.FullLength():
			headRef = pr.HeadCommitID
		default:
			headRef = pr.GetGitHeadRefName()
		}

		trackingBranch := fmt.Sprintf("tracking%d", i)
		if err := bctx.run(gitcmd.NewCommand("fetch", "--no-tags").
			AddDynamicArguments(remoteName, headRef+":"+trackingBranch)); err != nil {
			return "", fmt.Errorf("fetch head for PR[%d]: %w", pr.ID, err)
		}

		if err := applyPreviewStep(bctx, i, trackingBranch, mergeStyle, pr); err != nil {
			return "", fmt.Errorf("PR[%d]: %w", pr.ID, err)
		}
	}

	finalCommitID, err := git.GetFullCommitID(ctx, bctx.tmpRepo, "cursor")
	if err != nil {
		return "", fmt.Errorf("failed to get full commit id for merge queue preview: %w", err)
	}

	tempRef := MergeQueueTempRefName(basePR.BaseBranch, batchID)
	if err := bctx.run(gitcmd.NewCommand("push", "--force", "origin").
		AddDynamicArguments("cursor:" + tempRef)); err != nil {
		return "", fmt.Errorf("failed to push merge queue preview ref %s: %w", tempRef, err)
	}

	return finalCommitID, nil
}

// applyPreviewStep advances the "cursor" branch by merging/rebasing trackingBranch onto it, following
// the same logic real per-PR merge would (see merge_merge.go/merge_squash.go/merge_rebase.go), except
// unsigned and against the batch's moving cursor instead of the real base branch.
func applyPreviewStep(bctx *mergeQueueBatchContext, index int, trackingBranch string, mergeStyle repo_model.MergeStyle, pr *issues_model.PullRequest) error {
	message := fmt.Sprintf("[merge queue] pull request #%d", pr.Index)
	if pr.Issue != nil {
		message = fmt.Sprintf("[merge queue] %s", pr.Issue.Title)
	}

	switch mergeStyle {
	case repo_model.MergeStyleMerge:
		return bctx.run(gitcmd.NewCommand("merge", "--no-ff", "--no-gpg-sign").
			AddOptionFormat("--message=%s", message).
			AddDynamicArguments(trackingBranch))

	case repo_model.MergeStyleSquash:
		if err := bctx.run(gitcmd.NewCommand("merge", "--squash", "--no-gpg-sign").AddDynamicArguments(trackingBranch)); err != nil {
			return err
		}
		return bctx.run(gitcmd.NewCommand("commit", "--no-gpg-sign").AddOptionFormat("--message=%s", message))

	case repo_model.MergeStyleFastForwardOnly:
		return bctx.run(gitcmd.NewCommand("merge", "--ff-only").AddDynamicArguments(trackingBranch))

	case repo_model.MergeStyleRebase, repo_model.MergeStyleRebaseMerge:
		staging := fmt.Sprintf("staging%d", index)
		if err := bctx.run(gitcmd.NewCommand("checkout", "-b").AddDynamicArguments(staging, trackingBranch)); err != nil {
			return err
		}
		if err := bctx.run(gitcmd.NewCommand("rebase", "--no-gpg-sign").AddDynamicArguments("cursor")); err != nil {
			return fmt.Errorf("rebase onto cursor: %w", err)
		}
		if err := bctx.run(gitcmd.NewCommand("checkout", "cursor")); err != nil {
			return err
		}
		if mergeStyle == repo_model.MergeStyleRebase {
			return bctx.run(gitcmd.NewCommand("merge", "--ff-only").AddDynamicArguments(staging))
		}
		if err := bctx.run(gitcmd.NewCommand("merge", "--no-ff", "--no-commit").AddDynamicArguments(staging)); err != nil {
			return err
		}
		return bctx.run(gitcmd.NewCommand("commit", "--no-gpg-sign").AddOptionFormat("--message=%s", message))

	default:
		return ErrInvalidMergeStyle{ID: pr.BaseRepoID, Style: mergeStyle}
	}
}
