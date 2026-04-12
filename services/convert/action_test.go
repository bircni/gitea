// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	repo_model "code.gitea.io/gitea/models/repo"
	"code.gitea.io/gitea/models/unit"
	"code.gitea.io/gitea/modules/git"
	"code.gitea.io/gitea/modules/git/gitcmd"
	"code.gitea.io/gitea/modules/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildWorkflowTestRepo creates a temporary git repository for testing GetActionWorkflow.
// The default branch "main" has no workflow files; "feature" and "release-v1" each add their own workflow file.
func buildWorkflowTestRepo(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	tmpDir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		_, _, err := gitcmd.NewCommand(gitcmd.ToTrustedCmdArgs(args)...).WithDir(tmpDir).RunStdString(ctx)
		require.NoError(t, err)
	}

	run("init")
	run("symbolic-ref", "HEAD", "refs/heads/main")
	run("config", "user.email", "test@gitea.com")
	run("config", "user.name", "Test")

	err := os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("readme"), 0o644)
	require.NoError(t, err)
	run("add", ".")
	run("commit", "-m", "initial commit")

	run("checkout", "-b", "feature")
	wfDir := filepath.Join(tmpDir, ".gitea", "workflows")
	err = os.MkdirAll(wfDir, 0o755)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(wfDir, "my-workflow.yml"), []byte("on: [push]\njobs:\n  test:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo test\n"), 0o644)
	require.NoError(t, err)
	run("add", ".")
	run("commit", "-m", "add workflow")

	featureCommit, _, err := gitcmd.NewCommand("rev-parse", "HEAD").WithDir(tmpDir).RunStdString(ctx)
	require.NoError(t, err)
	run("update-ref", "refs/pull/42/merge", strings.TrimSpace(featureCommit))

	run("checkout", "main")
	wfPath := filepath.Join(tmpDir, ".gitea", "workflows", "my-workflow.yml")
	err = os.MkdirAll(filepath.Dir(wfPath), 0o755)
	require.NoError(t, err)
	err = os.WriteFile(wfPath, []byte("on: [push]\njobs:\n  release:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo release\n"), 0o644)
	require.NoError(t, err)
	run("add", ".")
	run("commit", "-m", "release workflow")
	run("tag", "release-v1")

	run("checkout", "main")
	run("reset", "--hard", "HEAD~1")

	return tmpDir
}

func TestGetActionWorkflow_FallbackRef(t *testing.T) {
	ctx := t.Context()

	repoDir := buildWorkflowTestRepo(t)

	gitRepo, err := git.OpenRepository(ctx, repoDir)
	require.NoError(t, err)
	defer gitRepo.Close()

	repo := &repo_model.Repository{
		DefaultBranch: "main",
		OwnerName:     "test-owner",
		Name:          "test-repo",
		Units: []*repo_model.RepoUnit{
			{
				Type:   unit.TypeActions,
				Config: &repo_model.ActionsConfig{},
			},
		},
	}

	t.Run("returns error when workflow only on non-default branch", func(t *testing.T) {
		_, err := GetActionWorkflow(ctx, gitRepo, repo, "my-workflow.yml")
		require.Error(t, err)
		assert.ErrorIs(t, err, util.ErrNotExist)
	})

	t.Run("returns workflow when found via fallback ref", func(t *testing.T) {
		wf, err := GetActionWorkflow(ctx, gitRepo, repo, "my-workflow.yml", "refs/heads/feature")
		require.NoError(t, err)
		assert.Equal(t, "my-workflow.yml", wf.ID)
	})

	t.Run("returns workflow when found via pull ref fallback", func(t *testing.T) {
		wf, err := GetActionWorkflow(ctx, gitRepo, repo, "my-workflow.yml", "refs/pull/42/merge")
		require.NoError(t, err)
		assert.Equal(t, "my-workflow.yml", wf.ID)
		assert.Contains(t, wf.HTMLURL, "/src/commit/")
	})

	t.Run("returns workflow with tag link when found via tag fallback", func(t *testing.T) {
		wf, err := GetActionWorkflow(ctx, gitRepo, repo, "my-workflow.yml", "refs/tags/release-v1")
		require.NoError(t, err)
		assert.Equal(t, "my-workflow.yml", wf.ID)
		assert.Contains(t, wf.HTMLURL, "/src/tag/release-v1/")
	})

	t.Run("returns error when workflow missing from both branches", func(t *testing.T) {
		_, err := GetActionWorkflow(ctx, gitRepo, repo, "nonexistent.yml", "refs/heads/feature")
		require.Error(t, err)
		assert.ErrorIs(t, err, util.ErrNotExist)
	})
}
