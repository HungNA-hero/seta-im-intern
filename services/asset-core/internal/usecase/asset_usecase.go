package usecase

import (
	"context"

	"seta-im-intern/go-asset-core/internal/domain"
)

type assetUsecase struct {
	repo domain.AssetRepository
}

// NewAssetUsecase creates a new instance of AssetUsecase.
func NewAssetUsecase(repo domain.AssetRepository) domain.AssetUsecase {
	return &assetUsecase{repo: repo}
}

func (u *assetUsecase) GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]domain.Folder, error) {
	// Here we could add business logic, permission checks, etc.
	// For now, it simply delegates to the repository.
	return u.repo.GetFolderTree(ctx, orgID, rootPath)
}

func (u *assetUsecase) GetFolderByID(ctx context.Context, orgID string, folderID string) (domain.Folder, error) {
	return u.repo.GetFolderByID(ctx, orgID, folderID)
}

func (u *assetUsecase) GetFolderChildren(ctx context.Context, orgID string, parentPath string) ([]domain.Folder, error) {
	return u.repo.GetFolderChildren(ctx, orgID, parentPath)
}

func (u *assetUsecase) GetRootFolders(ctx context.Context, orgID string) ([]domain.Folder, error) {
	return u.repo.GetRootFolders(ctx, orgID)
}

func (u *assetUsecase) EnsureRefs(ctx context.Context, userID, orgID string) error {
	return u.repo.EnsureRefs(ctx, userID, orgID)
}
