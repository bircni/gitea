// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	actions_model "code.gitea.io/gitea/models/actions"
	"code.gitea.io/gitea/modules/json"
	"code.gitea.io/gitea/modules/log"
	"code.gitea.io/gitea/modules/setting"

	"github.com/golang-jwt/jwt/v5"
)

type actionsClaims struct {
	jwt.RegisteredClaims
	Scp    string `json:"scp"`
	TaskID int64
	RunID  int64
	JobID  int64
	Ac     string `json:"ac"`
}

type taskTokenClaims struct {
	jwt.RegisteredClaims
	TaskToken actions_model.TaskTokenMetadata `json:"task_token"`
}

type actionsCacheScope struct {
	Scope      string
	Permission actionsCachePermission
}

type actionsCachePermission int

const (
	actionsCachePermissionRead = 1 << iota
	actionsCachePermissionWrite
)

func CreateAuthorizationToken(taskID, runID, jobID int64) (string, error) {
	now := time.Now()

	ac, err := json.Marshal(&[]actionsCacheScope{
		{
			Scope:      "",
			Permission: actionsCachePermissionWrite,
		},
	})
	if err != nil {
		return "", err
	}

	claims := actionsClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(1*time.Hour + setting.Actions.EndlessTaskTimeout)),
			NotBefore: jwt.NewNumericDate(now),
		},
		Scp:    fmt.Sprintf("Actions.Results:%d:%d", runID, jobID),
		Ac:     string(ac),
		TaskID: taskID,
		RunID:  runID,
		JobID:  jobID,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString(setting.GetGeneralTokenSigningSecret())
	if err != nil {
		return "", err
	}

	return tokenString, nil
}

func CreateTaskAuthorizationToken(task *actions_model.ActionTask) (string, error) {
	meta := actions_model.NewTaskTokenMetadata(task)
	if meta == nil {
		return "", errors.New("missing task metadata")
	}

	now := time.Now()
	claims := taskTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(1*time.Hour + setting.Actions.EndlessTaskTimeout)),
			NotBefore: jwt.NewNumericDate(now),
		},
		TaskToken: *meta,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	tokenString, err := token.SignedString(setting.GetGeneralTokenSigningSecret())
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func ParseAuthorizationToken(req *http.Request) (int64, error) {
	h := req.Header.Get("Authorization")
	if h == "" {
		return 0, nil
	}

	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 {
		log.Error("split token failed: %s", h)
		return 0, errors.New("split token failed")
	}

	return TokenToTaskID(parts[1])
}

// TokenToTaskID returns the TaskID associated with the provided JWT token
func TokenToTaskID(token string) (int64, error) {
	parsedToken, err := jwt.ParseWithClaims(token, &actionsClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return setting.GetGeneralTokenSigningSecret(), nil
	})
	if err != nil {
		return 0, err
	}

	c, ok := parsedToken.Claims.(*actionsClaims)
	if !parsedToken.Valid || !ok {
		return 0, errors.New("invalid token claim")
	}

	return c.TaskID, nil
}

func ParseTaskAuthorizationToken(token string) (*actions_model.TaskTokenMetadata, error) {
	parsedToken, err := jwt.ParseWithClaims(token, &taskTokenClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return setting.GetGeneralTokenSigningSecret(), nil
	})
	if err != nil {
		return nil, err
	}

	c, ok := parsedToken.Claims.(*taskTokenClaims)
	if !parsedToken.Valid || !ok {
		return nil, errors.New("invalid token claim")
	}
	if c.TaskToken.TaskID == 0 || c.TaskToken.RepoID == 0 || c.TaskToken.OwnerID == 0 {
		return nil, errors.New("invalid task token claim")
	}

	return &c.TaskToken, nil
}
