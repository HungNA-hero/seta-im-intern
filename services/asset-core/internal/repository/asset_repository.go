package repository

import (
	"context"

	"gorm.io/gorm"
	"seta-im-intern/go-asset-core/internal/domain"
)

type assetRepository struct {
	db *gorm.DB
}

// NewAssetRepository creates a new instance of AssetRepository.
func NewAssetRepository(db *gorm.DB) domain.AssetRepository {
	return &assetRepository{db: db}
}

func (r *assetRepository) GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]domain.Folder, error) {
	var folders []domain.Folder

	// Using PostgreSQL ltree <@ operator to find all descendants of rootPath.
	// We MUST filter by org_id as requested by the Mentor Feedback.
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND path <@ ?", orgID, rootPath).
		Order("path ASC").
		Find(&folders).Error

	return folders, err
}

func (r *assetRepository) GetFolderByID(ctx context.Context, orgID string, folderID string) (domain.Folder, error) {
	var folder domain.Folder

	err := r.db.WithContext(ctx).
		Where("id = ? AND org_id = ?", folderID, orgID).
		First(&folder).Error

	return folder, err
}

func (r *assetRepository) GetFolderChildren(ctx context.Context, orgID string, parentPath string) ([]domain.Folder, error) {
	var folders []domain.Folder

	// Direct children are nodes whose path descends from parentPath and whose
	// ltree level is exactly one more than parentPath's level.
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND path <@ ? AND nlevel(path) = nlevel(?::ltree) + 1",
			orgID, parentPath, parentPath).
		Order("name ASC").
		Find(&folders).Error

	return folders, err
}

func (r *assetRepository) GetRootFolders(ctx context.Context, orgID string) ([]domain.Folder, error) {
	var folders []domain.Folder

	// Root-level folders have an ltree level of 1
	err := r.db.WithContext(ctx).
		Where("org_id = ? AND nlevel(path) = 1", orgID).
		Order("name ASC").
		Find(&folders).Error

	return folders, err
}

func (r *assetRepository) EnsureRefs(ctx context.Context, userID, orgID string) error {
	// Upsert UserRef
	if err := r.db.WithContext(ctx).Exec(
		"INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID,
	).Error; err != nil {
		return err
	}

	// Upsert OrganizationRef
	if err := r.db.WithContext(ctx).Exec(
		"INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID,
	).Error; err != nil {
		return err
	}

	return nil
}
