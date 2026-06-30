package usecase

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"seta-im-intern/go-asset-core/internal/domain"
)

var ltreePathPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*$`)

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

func (u *assetUsecase) CreateFolder(ctx context.Context, orgID, userID string, input domain.CreateFolderInput) (domain.Folder, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	if utf8.RuneCountInString(input.Name) > 255 {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	if input.ParentPath != nil {
		trimmed := strings.TrimSpace(*input.ParentPath)
		if trimmed != "" && !ltreePathPattern.MatchString(trimmed) {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		input.ParentPath = &trimmed
	}
	return u.repo.CreateFolder(ctx, orgID, userID, input)
}

func (u *assetUsecase) UpdateFolder(ctx context.Context, orgID, userID, folderID string, input domain.UpdateFolderInput) (domain.Folder, error) {
	if input.NameSet {
		if input.Name == nil {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		trimmed := strings.TrimSpace(*input.Name)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > 255 {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		input.Name = &trimmed
	}
	if !input.NameSet && !input.DescriptionSet {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	return u.repo.UpdateFolder(ctx, orgID, userID, folderID, input)
}
