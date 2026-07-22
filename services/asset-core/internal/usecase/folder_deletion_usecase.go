package usecase

import (
	"context"

	"seta-im-intern/go-asset-core/internal/domain"
)

type folderDeletionUsecase struct {
	repo domain.FolderDeletionRepository
}

func NewFolderDeletionUsecase(repo domain.FolderDeletionRepository) domain.FolderDeletionUsecase {
	return &folderDeletionUsecase{repo: repo}
}

func (u *folderDeletionUsecase) PreviewFolderDeletion(ctx context.Context, orgID, userID, folderID string) (domain.FolderDeletionPreview, error) {
	return u.repo.PreviewFolderDeletion(ctx, orgID, userID, folderID)
}

func (u *folderDeletionUsecase) ConfirmFolderDeletion(ctx context.Context, orgID, userID, folderID, previewID, token string) (domain.FolderDeletionJob, error) {
	return u.repo.ConfirmFolderDeletion(ctx, orgID, userID, folderID, previewID, token)
}

func (u *folderDeletionUsecase) GetFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	return u.repo.GetFolderDeletionJob(ctx, orgID, actorID, jobID, actorIsOrgAdmin)
}

func (u *folderDeletionUsecase) CancelFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	return u.repo.CancelFolderDeletionJob(ctx, orgID, actorID, jobID, actorIsOrgAdmin)
}

func (u *folderDeletionUsecase) RetryFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	return u.repo.RetryFolderDeletionJob(ctx, orgID, actorID, jobID, actorIsOrgAdmin)
}
