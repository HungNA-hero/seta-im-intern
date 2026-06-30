package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

func (r *assetRepository) CreateFolder(ctx context.Context, orgID, userID string, input domain.CreateFolderInput) (domain.Folder, error) {
	var newFolder domain.Folder

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Ensure refs
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		// 2. Load and lock parent if child
		var parentPath string
		if input.ParentPath != nil && *input.ParentPath != "" {
			parentPath = *input.ParentPath
			var parent domain.Folder
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("org_id = ? AND path = ?", orgID, parentPath).
				First(&parent).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.ErrFolderNotFound
				}
				return err
			}
		}

		// 3. Pre-check sibling uniqueness
		var siblingCount int64
		var countErr error
		if parentPath != "" {
			countErr = tx.Model(&domain.Folder{}).
				Where("org_id = ? AND path <@ ?::ltree AND nlevel(path) = nlevel(?::ltree) + 1 AND name = ?", orgID, parentPath, parentPath, input.Name).
				Count(&siblingCount).Error
		} else {
			countErr = tx.Model(&domain.Folder{}).
				Where("org_id = ? AND nlevel(path) = 1 AND name = ?", orgID, input.Name).
				Count(&siblingCount).Error
		}
		if countErr != nil {
			return countErr
		}
		if siblingCount > 0 {
			return domain.ErrFolderConflict
		}

		// 4. Generate UUID and Path
		folderID := uuid.New().String()
		segment := strings.ReplaceAll(folderID, "-", "")

		var path string
		if parentPath != "" {
			path = parentPath + "." + segment
		} else {
			path = segment
		}

		// 5. Insert
		newFolder = domain.Folder{
			ID:        folderID,
			OrgID:     orgID,
			Path:      path,
			Name:      input.Name,
			CreatedBy: userID,
		}
		if input.Description != nil {
			newFolder.Description = input.Description
		}

		if err := tx.Create(&newFolder).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.ErrFolderConflict
			}
			return err
		}

		return nil
	})

	return newFolder, err
}

func (r *assetRepository) UpdateFolder(ctx context.Context, orgID, userID, folderID string, input domain.UpdateFolderInput) (domain.Folder, error) {
	var folder domain.Folder

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Ensure refs
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		// 2. Load active folder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", folderID, orgID).
			First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}

		// 3. Sibling uniqueness if name changed
		if input.NameSet && *input.Name != folder.Name {
			var siblingCount int64
			var countErr error
			if !strings.Contains(folder.Path, ".") {
				countErr = tx.Model(&domain.Folder{}).
					Where("org_id = ? AND nlevel(path) = 1 AND name = ? AND id != ?", orgID, *input.Name, folder.ID).
					Count(&siblingCount).Error
			} else {
				parentPath := folder.Path[:strings.LastIndex(folder.Path, ".")]
				countErr = tx.Model(&domain.Folder{}).
					Where("org_id = ? AND path <@ ?::ltree AND nlevel(path) = nlevel(?::ltree) + 1 AND name = ? AND id != ?",
						orgID, parentPath, parentPath, *input.Name, folder.ID).
					Count(&siblingCount).Error
			}
			if countErr != nil {
				return countErr
			}
			if siblingCount > 0 {
				return domain.ErrFolderConflict
			}
			folder.Name = *input.Name
		}

		// 4. Update fields
		if input.DescriptionSet {
			folder.Description = input.Description
		}
		folder.UpdatedBy = &userID

		// 5. Save (updates all fields, trigger handles updated_at)
		if err := tx.Save(&folder).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.ErrFolderConflict
			}
			return err
		}

		return nil
	})

	return folder, err
}
