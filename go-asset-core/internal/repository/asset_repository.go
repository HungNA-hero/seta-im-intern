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
