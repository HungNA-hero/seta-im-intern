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
		Where("org_id = ? AND path <@ ? AND deleted_at IS NULL", orgID, rootPath).
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
