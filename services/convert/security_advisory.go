// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package convert

import (
	"context"

	"gitea.dev/models/db"
	repo_model "gitea.dev/models/repo"
	security_model "gitea.dev/models/security"
	user_model "gitea.dev/models/user"
	api "gitea.dev/modules/structs"
)

// ToSecurityAdvisory converts a security_model.Advisory to its API representation,
// loading the advisory's vulnerabilities and credits and resolving the
// author/publisher/repository references. A nil doer is treated as anonymous.
func ToSecurityAdvisory(ctx context.Context, doer *user_model.User, advisory *security_model.Advisory) (*api.SecurityAdvisory, error) {
	vulns, err := loadAdvisoryVulnerabilities(ctx, advisory.ID)
	if err != nil {
		return nil, err
	}
	credits, err := loadAdvisoryCredits(ctx, doer, advisory.ID)
	if err != nil {
		return nil, err
	}

	author, err := user_model.GetUserByID(ctx, advisory.AuthorID)
	if err != nil && !user_model.IsErrUserNotExist(err) {
		return nil, err
	}

	var publisher *api.User
	if advisory.PublisherID > 0 {
		pub, err := user_model.GetUserByID(ctx, advisory.PublisherID)
		if err != nil && !user_model.IsErrUserNotExist(err) {
			return nil, err
		} else if err == nil {
			publisher = ToUser(ctx, pub, doer)
		}
	}

	var repoMeta *api.RepositoryMeta
	repo, err := repo_model.GetRepositoryByID(ctx, advisory.RepoID)
	if err != nil && !repo_model.IsErrRepoNotExist(err) {
		return nil, err
	} else if err == nil {
		if err := repo.LoadOwner(ctx); err != nil {
			return nil, err
		}
		repoMeta = &api.RepositoryMeta{
			ID:       repo.ID,
			Name:     repo.Name,
			Owner:    repo.OwnerName,
			FullName: repo.FullName(),
		}
	}

	result := &api.SecurityAdvisory{
		GTSAID:  advisory.GTSAID,
		CVEID:   advisory.CVEID,
		URL:     advisoryAPIURL(ctx, repo, advisory),
		HTMLURL: advisoryHTMLURL(ctx, repo, advisory),

		Summary:     advisory.Summary,
		Description: advisory.Description,
		Severity:    api.SecurityAdvisorySeverity(advisory.Severity),
		State:       api.SecurityAdvisoryState(advisory.State),

		CVSSSeverities: &api.SecurityAdvisoryCVSSSeverities{
			CVSSv3Vector: advisory.CVSSv3Vector,
			CVSSv3Score:  advisory.CVSSv3Score,
			CVSSv4Vector: advisory.CVSSv4Vector,
		},
		CWEIDs:          advisory.CWEIDs,
		Credits:         credits,
		Vulnerabilities: vulns,

		Publisher:  publisher,
		Repository: repoMeta,

		CreatedAt: advisory.CreatedUnix.AsTime(),
		UpdatedAt: advisory.UpdatedUnix.AsTime(),
	}
	if author != nil {
		result.Author = ToUser(ctx, author, doer)
	}
	if advisory.PublishedUnix != 0 {
		result.PublishedAt = advisory.PublishedUnix.AsTimePtr()
	}
	if advisory.ClosedUnix != 0 {
		result.ClosedAt = advisory.ClosedUnix.AsTimePtr()
	}
	if advisory.WithdrawnUnix != 0 {
		result.WithdrawnAt = advisory.WithdrawnUnix.AsTimePtr()
	}

	return result, nil
}

// ToSecurityAdvisoryList converts a slice of advisories, in the same way as ToSecurityAdvisory.
func ToSecurityAdvisoryList(ctx context.Context, doer *user_model.User, advisories []*security_model.Advisory) ([]*api.SecurityAdvisory, error) {
	result := make([]*api.SecurityAdvisory, 0, len(advisories))
	for _, advisory := range advisories {
		converted, err := ToSecurityAdvisory(ctx, doer, advisory)
		if err != nil {
			return nil, err
		}
		result = append(result, converted)
	}
	return result, nil
}

func advisoryAPIURL(ctx context.Context, repo *repo_model.Repository, advisory *security_model.Advisory) string {
	if repo == nil {
		return ""
	}
	return repo.APIURL(ctx) + "/security-advisories/" + advisory.GTSAID
}

func advisoryHTMLURL(ctx context.Context, repo *repo_model.Repository, advisory *security_model.Advisory) string {
	if repo == nil {
		return ""
	}
	return repo.HTMLURL(ctx) + "/security/advisories/" + advisory.GTSAID
}

func loadAdvisoryVulnerabilities(ctx context.Context, advisoryID int64) ([]*api.SecurityAdvisoryVulnerability, error) {
	var vulns []*security_model.AdvisoryVulnerability
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisoryID).OrderBy("id").Find(&vulns); err != nil {
		return nil, err
	}

	result := make([]*api.SecurityAdvisoryVulnerability, 0, len(vulns))
	for _, v := range vulns {
		result = append(result, &api.SecurityAdvisoryVulnerability{
			Package: &api.SecurityAdvisoryPackage{
				Ecosystem: v.Ecosystem,
				Name:      v.PackageName,
			},
			VulnerableVersionRange: v.VulnerableVersionRange,
			PatchedVersions:        v.PatchedVersions,
			VulnerableFunctions:    v.VulnerableFunctions,
		})
	}
	return result, nil
}

// loadAdvisoryCredits resolves each credit's local user reference via
// user_model.GetUserByID and ToUser, which honours KeepEmailPrivate through
// user.GetEmail() rather than exposing user.Email directly.
func loadAdvisoryCredits(ctx context.Context, doer *user_model.User, advisoryID int64) ([]*api.SecurityAdvisoryCredit, error) {
	var credits []*security_model.AdvisoryCredit
	if err := db.GetEngine(ctx).Where("advisory_id = ?", advisoryID).OrderBy("id").Find(&credits); err != nil {
		return nil, err
	}

	result := make([]*api.SecurityAdvisoryCredit, 0, len(credits))
	for _, c := range credits {
		credit := &api.SecurityAdvisoryCredit{
			OriginalAuthor: c.OriginalAuthor,
			Type:           api.SecurityAdvisoryCreditType(c.Type),
			State:          api.SecurityAdvisoryCreditState(c.State),
		}
		if c.UserID > 0 {
			u, err := user_model.GetUserByID(ctx, c.UserID)
			if err != nil {
				if user_model.IsErrUserNotExist(err) {
					continue
				}
				return nil, err
			}
			credit.User = ToUser(ctx, u, doer)
			credit.OriginalAuthor = ""
		}
		result = append(result, credit)
	}
	return result, nil
}
