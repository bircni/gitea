// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package pull

import (
	"context"
	"fmt"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/util"

	"xorm.io/builder"
)

// MergeQueueEntryState uses the state names of GitHub's merge queue
type MergeQueueEntryState string

const (
	MergeQueueStateQueued         MergeQueueEntryState = "queued"          // waiting to be built
	MergeQueueStateAwaitingChecks MergeQueueEntryState = "awaiting_checks" // queue commit built, checks running
	MergeQueueStateMergeable      MergeQueueEntryState = "mergeable"       // checks passed, waiting for the entries before it
	MergeQueueStateUnmergeable    MergeQueueEntryState = "unmergeable"     // checks failed, kept only for HEADGREEN
)

// MergeQueueRefPrefix is the branch prefix GitHub uses, so existing workflows filtering it keep working
const MergeQueueRefPrefix = "gh-readonly-queue/"

// MergeQueueEntry is a pull request waiting in the merge queue of its base branch
type MergeQueueEntry struct {
	ID                int64                `xorm:"pk autoincr"`
	RepoID            int64                `xorm:"INDEX(branch) NOT NULL"`
	BaseBranch        string               `xorm:"INDEX(branch) VARCHAR(255) NOT NULL"`
	PullID            int64                `xorm:"UNIQUE NOT NULL"`
	EnqueuerID        int64                `xorm:"NOT NULL"`
	Jump              bool                 `xorm:"NOT NULL DEFAULT false"`
	State             MergeQueueEntryState `xorm:"VARCHAR(20) NOT NULL"`
	HeadSHA           string               `xorm:"VARCHAR(64) NOT NULL"`
	BaseSHA           string               `xorm:"VARCHAR(64)"`
	GroupHeadSHA      string               `xorm:"VARCHAR(64) INDEX"`
	GroupRef          string               `xorm:"VARCHAR(255)"`
	ChecksStartedUnix timeutil.TimeStamp
	CreatedUnix       timeutil.TimeStamp `xorm:"created"`
	UpdatedUnix       timeutil.TimeStamp `xorm:"updated"`
}

func (MergeQueueEntry) TableName() string {
	return "pull_merge_queue_entry"
}

func init() {
	db.RegisterModel(new(MergeQueueEntry))
}

// IsBuilt reports whether a queue commit exists for the entry
func (e *MergeQueueEntry) IsBuilt() bool {
	return e.GroupHeadSHA != ""
}

// MergeQueueGroupRef returns the branch ref of a queue commit like GitHub's gh-readonly-queue/main/pr-12-<sha>
func MergeQueueGroupRef(baseBranch string, prIndex int64, headSHA string) string {
	return fmt.Sprintf("refs/heads/%s%s/pr-%d-%s", MergeQueueRefPrefix, baseBranch, prIndex, headSHA)
}

var ErrAlreadyInMergeQueue = util.NewAlreadyExistErrorf("pull request is already in the merge queue")

// AddToMergeQueue appends the pull request to the queue of its base branch
func AddToMergeQueue(ctx context.Context, entry *MergeQueueEntry) error {
	exist, err := db.GetEngine(ctx).Where("pull_id = ?", entry.PullID).Exist(&MergeQueueEntry{})
	if err != nil {
		return err
	} else if exist {
		return ErrAlreadyInMergeQueue
	}
	entry.State = MergeQueueStateQueued
	_, err = db.GetEngine(ctx).Insert(entry)
	return err
}

// GetMergeQueueEntryByPullID returns nil when the pull request is not queued
func GetMergeQueueEntryByPullID(ctx context.Context, pullID int64) (*MergeQueueEntry, error) {
	entry := &MergeQueueEntry{}
	has, err := db.GetEngine(ctx).Where("pull_id = ?", pullID).Get(entry)
	if err != nil || !has {
		return nil, err
	}
	return entry, nil
}

// GetMergeQueueEntriesByGroupHeadSHA finds the entries whose queue commit is the given commit
func GetMergeQueueEntriesByGroupHeadSHA(ctx context.Context, repoID int64, sha string) ([]*MergeQueueEntry, error) {
	entries := make([]*MergeQueueEntry, 0, 1)
	return entries, db.GetEngine(ctx).Where("repo_id = ? AND group_head_sha = ?", repoID, sha).Find(&entries)
}

// GetMergeQueueEntries returns the queue of a branch in merge order, jumped entries first
func GetMergeQueueEntries(ctx context.Context, repoID int64, baseBranch string) ([]*MergeQueueEntry, error) {
	entries := make([]*MergeQueueEntry, 0, 10)
	return entries, db.GetEngine(ctx).Where("repo_id = ? AND base_branch = ?", repoID, baseBranch).
		OrderBy("jump DESC, id ASC").Find(&entries)
}

// GetMergeQueueBranches returns the branches of all repositories that have queued pull requests
func GetMergeQueueBranches(ctx context.Context) ([]*MergeQueueEntry, error) {
	entries := make([]*MergeQueueEntry, 0, 10)
	return entries, db.GetEngine(ctx).Select("repo_id, base_branch").GroupBy("repo_id, base_branch").Find(&entries)
}

// UpdateMergeQueueEntry stores the build state of an entry
func UpdateMergeQueueEntry(ctx context.Context, entry *MergeQueueEntry) error {
	_, err := db.GetEngine(ctx).ID(entry.ID).Cols("state", "base_sha", "group_head_sha", "group_ref", "checks_started_unix").Update(entry)
	return err
}

// DeleteMergeQueueEntry removes the pull request from the queue, returns whether it was queued
func DeleteMergeQueueEntry(ctx context.Context, pullID int64) (bool, error) {
	n, err := db.GetEngine(ctx).Where(builder.Eq{"pull_id": pullID}).Delete(&MergeQueueEntry{})
	return n > 0, err
}

// HasMergeQueueEntries reports whether the branch has a non-empty merge queue
func HasMergeQueueEntries(ctx context.Context, repoID int64, baseBranch string) (bool, error) {
	return db.GetEngine(ctx).Where("repo_id = ? AND base_branch = ?", repoID, baseBranch).Exist(&MergeQueueEntry{})
}
