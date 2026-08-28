// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v28

import (
	"context"

	"gitea.dev/modelmigration/base"

	"xorm.io/xorm"
)

// AddMergeQueue adds the merge queue tables and the associated branch protection settings.
func AddMergeQueue(_ context.Context, x base.EngineMigration) error {
	type ProtectedBranch struct {
		EnableMergeQueue       bool   `xorm:"NOT NULL DEFAULT false"`
		MergeQueueMinBatchSize int    `xorm:"NOT NULL DEFAULT 1"`
		MergeQueueMaxBatchSize int    `xorm:"NOT NULL DEFAULT 1"`
		MergeQueueWaitMinutes  int    `xorm:"NOT NULL DEFAULT 5"`
		MergeQueueMergeStyle   string `xorm:"VARCHAR(30)"`
	}
	if _, err := x.SyncWithOptions(xorm.SyncOptions{
		IgnoreConstrains:  true,
		IgnoreDropIndices: true,
	}, new(ProtectedBranch)); err != nil {
		return err
	}

	if err := x.Sync(new(mergeQueueEntry)); err != nil {
		return err
	}
	return x.Sync(new(mergeQueueBatch))
}

type mergeQueueEntry struct {
	ID                     int64  `xorm:"pk autoincr"`
	RepoID                 int64  `xorm:"INDEX NOT NULL"`
	BaseBranch             string `xorm:"VARCHAR(255) INDEX NOT NULL"`
	PullID                 int64  `xorm:"INDEX NOT NULL"`
	Position               int64  `xorm:"INDEX NOT NULL"`
	BatchID                int64  `xorm:"INDEX"`
	Status                 int    `xorm:"INDEX NOT NULL"`
	HeadCommitID           string `xorm:"VARCHAR(64) NOT NULL"`
	EnqueuedByID           int64  `xorm:"NOT NULL"`
	MergeStyle             string `xorm:"VARCHAR(30) NOT NULL"`
	Message                string `xorm:"LONGTEXT"`
	DeleteBranchAfterMerge bool
	RemovalReason          string `xorm:"TEXT"`
	CreatedUnix            int64  `xorm:"created"`
	UpdatedUnix            int64  `xorm:"updated"`
}

func (mergeQueueEntry) TableName() string {
	return "pull_merge_queue_entry"
}

type mergeQueueBatch struct {
	ID           int64  `xorm:"pk autoincr"`
	RepoID       int64  `xorm:"INDEX NOT NULL"`
	BaseBranch   string `xorm:"VARCHAR(255) NOT NULL"`
	BaseCommitID string `xorm:"VARCHAR(64) NOT NULL"`
	TempCommitID string `xorm:"VARCHAR(64) INDEX"`
	TempRefName  string `xorm:"VARCHAR(255)"`
	MergeStyle   string `xorm:"VARCHAR(30) NOT NULL"`
	Status       int    `xorm:"INDEX NOT NULL"`
	CreatedUnix  int64  `xorm:"created"`
	UpdatedUnix  int64  `xorm:"updated"`
}

func (mergeQueueBatch) TableName() string {
	return "pull_merge_queue_batch"
}
