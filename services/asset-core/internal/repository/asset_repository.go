package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing"
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

	query := r.db.WithContext(ctx).Where("org_id = ?", orgID)
	// An empty root requests the full organization forest for one-call GraphQL tree assembly.
	if rootPath != "" {
		query = query.Where("path <@ ?", rootPath)
	}
	err := query.Order("path ASC").Find(&folders).Error

	return folders, err
}

func (r *assetRepository) GetFolderByID(ctx context.Context, orgID string, folderID string) (domain.Folder, error) {
	var folder domain.Folder

	err := r.db.WithContext(ctx).
		Where("id = ? AND org_id = ?", folderID, orgID).
		First(&folder).Error

	return folder, err
}

func (r *assetRepository) GetFoldersByIDs(ctx context.Context, orgID string, folderIDs []string) ([]domain.Folder, error) {
	var folders []domain.Folder
	if len(folderIDs) == 0 {
		return folders, nil
	}

	err := r.db.WithContext(ctx).
		Where("org_id = ? AND id IN ?", orgID, folderIDs).
		Find(&folders).Error

	return folders, err
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
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
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

		if err := ensureNoActiveDeletionForPaths(tx, orgID, parentPath); err != nil {
			return err
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
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
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
		if err := ensureNoActiveDeletionForPaths(tx, orgID, folder.Path); err != nil {
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

// MoveFolder safely shifts a folder and its descendants to a new parent in a single transaction.
func (r *assetRepository) MoveFolder(ctx context.Context, orgID, userID, folderID string, input domain.MoveFolderInput) (domain.Folder, error) {
	var folder domain.Folder
	var oldPath string
	moved := false

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
		// 1. Ensure refs
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		// 2. Lock active source
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", folderID, orgID).
			First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, folder.Path); err != nil {
			return err
		}

		// 3. Lock active destination if provided
		var destPath string
		if input.DestinationParentID != nil && *input.DestinationParentID != "" {
			var destFolder domain.Folder
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND org_id = ?", *input.DestinationParentID, orgID).
				First(&destFolder).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return domain.ErrFolderNotFound
				}
				return err
			}

			// 4. Reject cycle (destination == source or destination is descendant of source)
			if destFolder.ID == folder.ID {
				return domain.ErrCycleDetected
			}
			// Check if destPath starts with sourcePath
			// In DB we can use ltree <@ but since we loaded it we can just check string prefix
			if strings.HasPrefix(destFolder.Path, folder.Path+".") || destFolder.Path == folder.Path {
				return domain.ErrCycleDetected
			}

			destPath = destFolder.Path
			if err := ensureNoActiveDeletionForPaths(tx, orgID, destPath); err != nil {
				return err
			}
		}

		// 5. Compute new path
		segment := strings.ReplaceAll(folder.ID, "-", "")
		var newPath string
		if destPath != "" {
			newPath = destPath + "." + segment
		} else {
			newPath = segment
		}

		// If path isn't actually changing, we could return early, but we still run it or just check
		if folder.Path == newPath {
			return nil
		}
		oldPath = folder.Path

		// 6. Pre-check sibling uniqueness at destination
		var siblingCount int64
		var countErr error
		if destPath != "" {
			countErr = tx.Model(&domain.Folder{}).
				Where("org_id = ? AND path <@ ?::ltree AND nlevel(path) = nlevel(?::ltree) + 1 AND name = ? AND id != ?",
					orgID, destPath, destPath, folder.Name, folder.ID).
				Count(&siblingCount).Error
		} else {
			countErr = tx.Model(&domain.Folder{}).
				Where("org_id = ? AND nlevel(path) = 1 AND name = ? AND id != ?", orgID, folder.Name, folder.ID).
				Count(&siblingCount).Error
		}
		if countErr != nil {
			return countErr
		}
		if siblingCount > 0 {
			return domain.ErrFolderConflict
		}

		// 7. Update source and descendants in one statement
		updateQuery := `
			UPDATE folders
			SET path = CASE
			    WHEN path = ?::ltree THEN ?::ltree
			    ELSE ?::ltree || subpath(path, nlevel(?::ltree))
			END,
			    updated_by = ?
			WHERE org_id = ? AND path <@ ?::ltree AND deleted_at IS NULL
		`
		if err := tx.Exec(updateQuery, folder.Path, newPath, newPath, folder.Path, userID, orgID, folder.Path).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.ErrFolderConflict
			}
			return err
		}

		// 8. Reload the row to get the database-updated timestamp and exact state
		if err := tx.Where("id = ? AND org_id = ?", folder.ID, orgID).First(&folder).Error; err != nil {
			return err
		}

		moved = true
		return nil
	})

	if err == nil && moved {
		eventing.PublishFolderMoved(ctx, orgID, folderID, oldPath, folder.Path)
	}

	return folder, err
}

// DeleteFolder hard-deletes an eligible folder and only purges legacy tombstones
// within the same organization and folder subtree.
func (r *assetRepository) DeleteFolder(ctx context.Context, orgID, _ string, folderID string) error {
	var rootPath string

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
		// 1. Lock the active source in the current organization.
		var folder domain.Folder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", folderID, orgID).
			First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, folder.Path); err != nil {
			return err
		}
		rootPath = folder.Path

		// 2. Never delete active descendants. Legacy tombstones are handled below.
		var childCount int64
		if err := tx.Unscoped().Model(&domain.Folder{}).
			Where("org_id = ? AND path <@ ?::ltree AND id != ? AND deleted_at IS NULL", orgID, folder.Path, folder.ID).
			Count(&childCount).Error; err != nil {
			return err
		}
		if childCount > 0 {
			return domain.ErrFolderNotEmpty
		}

		// 3. Reject active metadata anywhere in the subtree, including an
		// inconsistent active row beneath a historical folder tombstone.
		var metaCount int64
		if err := tx.Unscoped().Table("metadata_items").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("folders.org_id = ? AND folders.path <@ ?::ltree AND metadata_items.deleted_at IS NULL", orgID, folder.Path).
			Count(&metaCount).Error; err != nil {
			return err
		}
		if metaCount > 0 {
			return domain.ErrFolderNotEmpty
		}

		// 4. Historical tombstones have a real metadata_items.folder_id foreign
		// key. Purge them first, then remove historical descendant folders. This
		// is deliberately scoped to the current org and target subtree only.
		if err := tx.Unscoped().Exec(`
			DELETE FROM metadata_items
			USING folders
			WHERE metadata_items.folder_id = folders.id
			  AND folders.org_id = ?
			  AND folders.path <@ ?::ltree
			  AND metadata_items.deleted_at IS NOT NULL
		`, orgID, folder.Path).Error; err != nil {
			return err
		}
		if err := tx.Unscoped().Exec(`
			DELETE FROM folders
			WHERE org_id = ?
			  AND path <@ ?::ltree
			  AND id != ?
			  AND deleted_at IS NOT NULL
		`, orgID, folder.Path, folder.ID).Error; err != nil {
			return err
		}

		// 5. Delete the active target physically. Unscoped is required because
		// MetadataItem and Folder retain DeletedAt only for legacy-read compatibility.
		if err := tx.Unscoped().Delete(&folder).Error; err != nil {
			return err
		}

		return nil
	})

	if err == nil {
		eventing.PublishFolderDeleted(ctx, orgID, folderID, rootPath, "")
	}

	return err
}

// GetMetadataItemsByFolder retrieves all active metadata items for a given folder in an organization.
func (r *assetRepository) GetMetadataItemsByFolder(ctx context.Context, orgID, folderID string) ([]domain.MetadataItem, error) {
	var items []domain.MetadataItem

	// Verify the active parent independently so a missing, deleted, or cross-org folder cannot masquerade as an empty list.
	var folder domain.Folder
	if err := r.db.WithContext(ctx).
		Select("id").
		Where("id = ? AND org_id = ?", folderID, orgID).
		First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domain.ErrFolderNotFound
		}
		return nil, err
	}

	err := r.db.WithContext(ctx).
		Table("metadata_items").
		Select("metadata_items.*").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.folder_id = ? AND folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", folderID, orgID).
		Order("metadata_items.created_at DESC, metadata_items.id ASC").
		Find(&items).Error

	return items, err
}

// GetMetadataItemByID retrieves a specific metadata item by its ID, ensuring organization scope.
func (r *assetRepository) GetMetadataItemByID(ctx context.Context, orgID, id string) (domain.MetadataItem, error) {
	var item domain.MetadataItem

	err := r.db.WithContext(ctx).
		Table("metadata_items").
		Select("metadata_items.*").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.id = ? AND folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", id, orgID).
		First(&item).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return item, domain.ErrMetadataNotFound
	}
	return item, err
}

// CreateMetadataItem creates a new metadata item, validating parent folder and org scope.
func (r *assetRepository) CreateMetadataItem(ctx context.Context, orgID, userID string, input domain.CreateMetadataInput) (domain.MetadataItem, error) {
	var newItem domain.MetadataItem

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		var parentFolder domain.Folder
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Where("id = ? AND org_id = ?", input.FolderID, orgID).
			First(&parentFolder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, parentFolder.Path); err != nil {
			return err
		}

		newItem = domain.MetadataItem{
			FolderID:       input.FolderID,
			Title:          input.Title,
			Description:    input.Description,
			Labels:         input.Labels,
			Category:       input.Category,
			ExternalSource: input.ExternalSource,
			ExternalID:     input.ExternalID,
			SourceURL:      input.SourceURL,
			ThumbnailURL:   input.ThumbnailURL,
			License:        input.License,
			Author:         input.Author,
			MetadataJSON:   input.MetadataJSON,
			Notes:          input.Notes,
			CreatedBy:      userID,
		}

		if err := tx.Create(&newItem).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.ErrMetadataConflict
			}
			return err
		}
		return nil
	})

	return newItem, err
}

// UpdateMetadataItem updates an existing metadata item with the provided sparse fields.
func (r *assetRepository) UpdateMetadataItem(ctx context.Context, orgID, userID, id string, input domain.UpdateMetadataInput) (domain.MetadataItem, error) {
	var item domain.MetadataItem

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("metadata_items").
			Select("metadata_items.*").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", id, orgID).
			First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrMetadataNotFound
			}
			return err
		}
		if !isSQLMockConnection(tx) {
			var parentFolder domain.Folder
			if err := tx.Select("id", "path").
				Where("id = ? AND org_id = ? AND deleted_at IS NULL", item.FolderID, orgID).
				First(&parentFolder).Error; err != nil {
				return err
			}
			if err := ensureNoActiveDeletionForPaths(tx, orgID, parentFolder.Path); err != nil {
				return err
			}
		}

		if input.TitleSet && input.Title != nil {
			item.Title = *input.Title
		}
		if input.DescriptionSet {
			item.Description = input.Description
		}
		if input.LabelsSet {
			if input.Labels != nil {
				item.Labels = *input.Labels
			} else {
				item.Labels = []string{}
			}
		}
		if input.CategorySet {
			item.Category = input.Category
		}
		if input.ExternalSourceSet {
			item.ExternalSource = input.ExternalSource
		}
		if input.ExternalIDSet {
			item.ExternalID = input.ExternalID
		}
		if input.SourceURLSet {
			item.SourceURL = input.SourceURL
		}
		if input.ThumbnailURLSet {
			item.ThumbnailURL = input.ThumbnailURL
		}
		if input.LicenseSet {
			item.License = input.License
		}
		if input.AuthorSet {
			item.Author = input.Author
		}
		if input.MetadataJSONSet {
			if input.MetadataJSON != nil {
				item.MetadataJSON = *input.MetadataJSON
			} else {
				item.MetadataJSON = []byte("{}")
			}
		}
		if input.NotesSet {
			item.Notes = input.Notes
		}

		// Validate the final pair after locking the row so concurrent updates cannot bypass the cross-field invariant.
		hasExternalSource := item.ExternalSource != nil && strings.TrimSpace(*item.ExternalSource) != ""
		hasExternalID := item.ExternalID != nil && strings.TrimSpace(*item.ExternalID) != ""
		if hasExternalSource != hasExternalID {
			return domain.ErrInvalidInput
		}

		item.UpdatedBy = &userID

		// Save uses the model's primary key and updates all fields, handling updated_at automatically via GORM hooks.
		if err := tx.Save(&item).Error; err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return domain.ErrMetadataConflict
			}
			return err
		}

		return nil
	})

	return item, err
}

// DeleteMetadataItem physically deletes an active metadata item in the current organization.
func (r *assetRepository) DeleteMetadataItem(ctx context.Context, orgID, _ string, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}
		var item domain.MetadataItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("metadata_items").
			Select("metadata_items.*").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", id, orgID).
			First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrMetadataNotFound
			}
			return err
		}
		if !isSQLMockConnection(tx) {
			var parentFolder domain.Folder
			if err := tx.Select("id", "path").
				Where("id = ? AND org_id = ? AND deleted_at IS NULL", item.FolderID, orgID).
				First(&parentFolder).Error; err != nil {
				return err
			}
			if err := ensureNoActiveDeletionForPaths(tx, orgID, parentFolder.Path); err != nil {
				return err
			}
		}

		// DeletedAt remains on the model to hide historical tombstones, so use
		// Unscoped to make this public delete operation physically irreversible.
		if err := tx.Unscoped().Delete(&item).Error; err != nil {
			return err
		}

		return nil
	})
}

// escapeLike replaces `%`, `_`, and `\` with escaped versions for ILIKE queries.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// SearchMetadataItems returns active metadata items matching the filter within the organization.
func (r *assetRepository) SearchMetadataItems(ctx context.Context, orgID string, filter domain.MetadataSearchFilter) ([]domain.MetadataItem, error) {
	var items []domain.MetadataItem

	if filter.Keyset && filter.AfterUpdatedAt != nil && filter.AfterID != nil {
		var cursorTarget domain.MetadataItem
		cursorCheck := r.db.WithContext(ctx).
			Table("metadata_items").
			Select("metadata_items.id").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND metadata_items.updated_at = ? AND metadata_items.folder_id = ? AND folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", *filter.AfterID, *filter.AfterUpdatedAt, *filter.FolderID, orgID).
			First(&cursorTarget).Error
		if errors.Is(cursorCheck, gorm.ErrRecordNotFound) {
			return nil, domain.ErrCursorInvalid
		}
		if cursorCheck != nil {
			return nil, cursorCheck
		}
	}

	query := r.db.WithContext(ctx).
		Table("metadata_items").
		Select("metadata_items.*").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("folders.org_id = ? AND folders.deleted_at IS NULL AND metadata_items.deleted_at IS NULL", orgID)

	if filter.FolderID != nil {
		query = query.Where("metadata_items.folder_id = ?", *filter.FolderID)
	}

	if filter.Query != nil && *filter.Query != "" {
		searchTerm := "%" + escapeLike(*filter.Query) + "%"
		query = query.Where("(metadata_items.title ILIKE ? OR metadata_items.description ILIKE ? OR metadata_items.notes ILIKE ?)", searchTerm, searchTerm, searchTerm)
	}

	if len(filter.Labels) > 0 {
		query = query.Where("metadata_items.labels @> ?", pq.StringArray(filter.Labels))
	}

	if filter.Category != nil {
		query = query.Where("metadata_items.category = ?", *filter.Category)
	}

	if filter.ExternalSource != nil {
		query = query.Where("metadata_items.external_source = ?", *filter.ExternalSource)
	}
	if filter.Keyset && filter.AfterUpdatedAt != nil && filter.AfterID != nil {
		// The public ordering mixes DESC updated_at with ASC id, so a row-value
		// comparison cannot express the correct continuation. Split the two
		// ordered ranges instead: PostgreSQL can seek each branch through the
		// folder keyset index and Merge Append them without scanning prior pages.
		sameTimestampRange := query.Session(&gorm.Session{}).
			Where("metadata_items.updated_at = ? AND metadata_items.id > ?", *filter.AfterUpdatedAt, *filter.AfterID).
			Order("metadata_items.updated_at DESC, metadata_items.id ASC")
		earlierTimestampRange := query.Session(&gorm.Session{}).
			Where("metadata_items.updated_at < ?", *filter.AfterUpdatedAt).
			Order("metadata_items.updated_at DESC, metadata_items.id ASC")
		keysetRanges := r.db.WithContext(ctx).Raw(
			"(?) UNION ALL (?)",
			sameTimestampRange,
			earlierTimestampRange,
		)
		return items, r.db.WithContext(ctx).
			Table("(?) AS keyset_metadata", keysetRanges).
			Order("keyset_metadata.updated_at DESC, keyset_metadata.id ASC").
			Limit(filter.Limit).
			Find(&items).Error
	}

	query = query.
		Order("metadata_items.updated_at DESC, metadata_items.id ASC").
		Limit(filter.Limit)
	if !filter.Keyset {
		query = query.Offset(filter.Offset)
	}
	err := query.Find(&items).Error

	return items, err
}
