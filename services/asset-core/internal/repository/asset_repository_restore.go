package repository

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing"
)

func nextAvailableDisplayValue(base string, used map[string]struct{}) string {
	if _, exists := used[base]; !exists {
		return base
	}
	for suffix := 1; ; suffix += 1 {
		candidate := base + " (" + strconv.Itoa(suffix) + ")"
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

// completeLifecycleRestore closes the live Recycle Bin entry for one root. A
// missing unit is tolerated for tombstones that predate KAN-82; a KAN-82
// delete always has one and will transition it from DELETED to RESTORED in the
// same transaction as the source-row restore.
func completeLifecycleRestore(
	tx *gorm.DB,
	orgID string,
	resourceType domain.LifecycleResourceType,
	resourceID string,
) error {
	return tx.Model(&domain.LifecycleUnit{}).
		Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ? AND state = ?", orgID, resourceType, resourceID, domain.LifecycleDeleted).
		Update("state", domain.LifecycleRestored).Error
}

// GetFolderRestoreAuthorizationFact intentionally uses an unscoped lookup. It
// is only exposed through a trusted internal endpoint and never through normal
// public reads, which continue to hide tombstones.
func (r *assetRepository) GetFolderRestoreAuthorizationFact(ctx context.Context, orgID, folderID string) (domain.FolderRestoreAuthorizationFact, error) {
	var folder domain.Folder
	err := r.db.WithContext(ctx).Unscoped().
		Select("folders.id", "folders.path").
		Where("folders.id = ? AND folders.org_id = ?", folderID, orgID).
		First(&folder).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.FolderRestoreAuthorizationFact{}, domain.ErrFolderNotFound
	}
	if err != nil {
		return domain.FolderRestoreAuthorizationFact{}, err
	}
	return domain.FolderRestoreAuthorizationFact{ID: folder.ID, Path: folder.Path}, nil
}

// GetMetadataRestoreAuthorizationFact returns only the metadata-to-folder
// relationship required for current object-permission evaluation.
func (r *assetRepository) GetMetadataRestoreAuthorizationFact(ctx context.Context, orgID, id string) (domain.MetadataRestoreAuthorizationFact, error) {
	var fact domain.MetadataRestoreAuthorizationFact
	err := r.db.WithContext(ctx).Unscoped().
		Table("metadata_items").
		Select("metadata_items.id", "metadata_items.folder_id", "folders.path AS folder_path").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.id = ? AND folders.org_id = ?", id, orgID).
		Scan(&fact).Error
	if err != nil {
		return domain.MetadataRestoreAuthorizationFact{}, err
	}
	if fact.ID == "" {
		return domain.MetadataRestoreAuthorizationFact{}, domain.ErrMetadataNotFound
	}
	return fact, nil
}

// GetLifecycleRestoreAuthorizationFact resolves a Recycle Bin unit back to
// the original protected resource. This is intentionally a private fact: it
// lets Access Core evaluate current write permission before it can queue a
// restore, without trusting a client-supplied resource identity.
func (r *assetRepository) GetLifecycleRestoreAuthorizationFact(ctx context.Context, orgID, unitID string) (domain.LifecycleRestoreAuthorizationFact, error) {
	var unit domain.LifecycleUnit
	err := r.db.WithContext(ctx).
		Where("id = ? AND org_id = ? AND state IN ?", unitID, orgID, []domain.LifecycleUnitState{
			domain.LifecycleDeleted,
			domain.LifecycleRestoreQueued,
			domain.LifecycleRestoring,
		}).
		First(&unit).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LifecycleRestoreAuthorizationFact{}, domain.ErrLifecycleUnitNotFound
	}
	if err != nil {
		return domain.LifecycleRestoreAuthorizationFact{}, err
	}

	fact := domain.LifecycleRestoreAuthorizationFact{
		UnitID:           unit.ID,
		RootResourceType: unit.RootResourceType,
		RootResourceID:   unit.RootResourceID,
	}
	switch unit.RootResourceType {
	case domain.LifecycleResourceFolder:
		var root domain.Folder
		err := r.db.WithContext(ctx).Unscoped().
			Select("folders.id", "folders.path").
			Where("folders.id = ? AND folders.org_id = ? AND folders.lifecycle_unit_id = ? AND folders.deleted_at IS NOT NULL", unit.RootResourceID, orgID, unit.ID).
			First(&root).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.LifecycleRestoreAuthorizationFact{}, domain.ErrLifecycleUnitNotFound
		}
		if err != nil {
			return domain.LifecycleRestoreAuthorizationFact{}, err
		}
		fact.RootFolderID = root.ID
		fact.RootFolderPath = root.Path
		return fact, nil
	case domain.LifecycleResourceMetadata:
		var row struct {
			FolderID   string `gorm:"column:folder_id"`
			FolderPath string `gorm:"column:folder_path"`
		}
		err := r.db.WithContext(ctx).Unscoped().
			Table("metadata_items").
			Select("metadata_items.folder_id", "folders.path AS folder_path").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND folders.org_id = ? AND metadata_items.lifecycle_unit_id = ? AND metadata_items.deleted_at IS NOT NULL", unit.RootResourceID, orgID, unit.ID).
			Scan(&row).Error
		if err != nil {
			return domain.LifecycleRestoreAuthorizationFact{}, err
		}
		if row.FolderID == "" || row.FolderPath == "" {
			return domain.LifecycleRestoreAuthorizationFact{}, domain.ErrLifecycleUnitNotFound
		}
		fact.RootFolderID = row.FolderID
		fact.RootFolderPath = row.FolderPath
		return fact, nil
	default:
		return domain.LifecycleRestoreAuthorizationFact{}, domain.ErrLifecycleUnitNotFound
	}
}

// GetLifecycleJob is only used through the trusted Asset-to-Access boundary.
// Access Core authorizes against the root information carried by the returned
// job before exposing its status to a client.
func (r *assetRepository) GetLifecycleJob(ctx context.Context, orgID, jobID string) (domain.LifecycleJob, error) {
	var job domain.LifecycleJob
	err := r.db.WithContext(ctx).Where("id = ? AND org_id = ?", jobID, orgID).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LifecycleJob{}, domain.ErrLifecycleJobNotFound
	}
	if err != nil {
		return domain.LifecycleJob{}, err
	}
	return job, nil
}

func lockedVisibleParent(
	tx *gorm.DB,
	orgID string,
	parentPath string,
) (domain.Folder, error) {
	var parent domain.Folder
	err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("folders.org_id = ? AND folders.path = ?", orgID, parentPath).
		Where(visibleFolderPredicate("folders")).
		First(&parent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Folder{}, domain.ErrFolderParentDeleted
	}
	return parent, err
}

func activeSiblingFolderNames(
	tx *gorm.DB,
	orgID string,
	parentPath string,
	excludeID string,
) (map[string]struct{}, error) {
	var names []string
	query := tx.Unscoped().Table("folders").
		Select("folders.name").
		Where("folders.org_id = ? AND folders.id != ?", orgID, excludeID).
		Where(visibleFolderPredicate("folders"))
	if parentPath == "" {
		query = query.Where("nlevel(folders.path) = 1")
	} else {
		query = query.Where(
			"folders.path <@ ?::ltree AND nlevel(folders.path) = nlevel(?::ltree) + 1",
			parentPath,
			parentPath,
		)
	}
	if err := query.Find(&names).Error; err != nil {
		return nil, err
	}
	used := make(map[string]struct{}, len(names))
	for _, name := range names {
		used[name] = struct{}{}
	}
	return used, nil
}

// RestoreFolder restores one tombstoned folder. It never cascades into
// descendants: the immediate parent must already be visible and active.
func (r *assetRepository) RestoreFolder(ctx context.Context, orgID, userID, folderID string) (domain.Folder, error) {
	var restored domain.Folder
	var restoredPath string

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Exclusive organization coordination serializes restore-name selection
		// with creates, moves, and other restores in the same organization.
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}

		var folder domain.Folder
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("folders.id = ? AND folders.org_id = ?", folderID, orgID).
			First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}
		if !folder.DeletedAt.Valid {
			return domain.ErrFolderNotDeleted
		}

		parentPath := ""
		if separator := strings.LastIndex(folder.Path, "."); separator >= 0 {
			parentPath = folder.Path[:separator]
			if _, err := lockedVisibleParent(tx, orgID, parentPath); err != nil {
				return err
			}
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, folder.Path); err != nil {
			return err
		}

		usedNames, err := activeSiblingFolderNames(tx, orgID, parentPath, folder.ID)
		if err != nil {
			return err
		}
		name := nextAvailableDisplayValue(folder.Name, usedNames)
		now := time.Now().UTC()
		if err := tx.Unscoped().Model(&domain.Folder{}).
			Where("id = ? AND org_id = ? AND deleted_at IS NOT NULL", folder.ID, orgID).
			Updates(map[string]any{"deleted_at": nil, "name": name, "updated_by": userID, "updated_at": now, "lifecycle_unit_id": nil}).Error; err != nil {
			return err
		}
		if err := completeLifecycleRestore(tx, orgID, domain.LifecycleResourceFolder, folder.ID); err != nil {
			return err
		}
		if err := tx.Unscoped().
			Where("folders.id = ? AND folders.org_id = ?", folder.ID, orgID).
			Where(visibleFolderPredicate("folders")).
			First(&restored).Error; err != nil {
			return err
		}
		restoredPath = restored.Path
		return nil
	})
	if err == nil {
		eventing.PublishFolderRestored(ctx, orgID, restored.ID, restoredPath)
	}
	return restored, err
}

func activeMetadataTitles(
	tx *gorm.DB,
	orgID string,
	folderID string,
	excludeID string,
) (map[string]struct{}, error) {
	var titles []string
	err := tx.Unscoped().Table("metadata_items").
		Select("metadata_items.title").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.folder_id = ? AND metadata_items.id != ? AND folders.org_id = ?", folderID, excludeID, orgID).
		Where(visibleMetadataPredicate("metadata_items", "folders")).
		Find(&titles).Error
	if err != nil {
		return nil, err
	}
	used := make(map[string]struct{}, len(titles))
	for _, title := range titles {
		used[title] = struct{}{}
	}
	return used, nil
}

// RestoreMetadataItem restores one tombstoned metadata item after its
// containing folder is visible. Its external identity remains immutable; only
// the user-visible title may be auto-renamed to avoid a collision.
func (r *assetRepository) RestoreMetadataItem(ctx context.Context, orgID, userID, id string) (domain.MetadataItem, error) {
	var restored domain.MetadataItem

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}

		var item domain.MetadataItem
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
			Table("metadata_items").
			Select("metadata_items.*").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND folders.org_id = ?", id, orgID).
			First(&item).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrMetadataNotFound
			}
			return err
		}
		if !item.DeletedAt.Valid {
			return domain.ErrMetadataNotDeleted
		}

		parent, err := lockedVisibleParentByID(tx, orgID, item.FolderID)
		if err != nil {
			if errors.Is(err, domain.ErrFolderParentDeleted) {
				return domain.ErrMetadataFolderDeleted
			}
			return err
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, parent.Path); err != nil {
			return err
		}

		if item.ExternalSource != nil && item.ExternalID != nil {
			var conflictCount int64
			if err := tx.Unscoped().Table("metadata_items").
				Joins("JOIN folders ON folders.id = metadata_items.folder_id").
				Where("metadata_items.id != ? AND metadata_items.external_source = ? AND metadata_items.external_id = ? AND folders.org_id = ?", item.ID, *item.ExternalSource, *item.ExternalID, orgID).
				Where(visibleMetadataPredicate("metadata_items", "folders")).
				Count(&conflictCount).Error; err != nil {
				return err
			}
			if conflictCount > 0 {
				return domain.ErrMetadataConflict
			}
		}

		usedTitles, err := activeMetadataTitles(tx, orgID, item.FolderID, item.ID)
		if err != nil {
			return err
		}
		title := nextAvailableDisplayValue(item.Title, usedTitles)
		now := time.Now().UTC()
		if err := tx.Unscoped().Model(&domain.MetadataItem{}).
			Where("id = ? AND deleted_at IS NOT NULL", item.ID).
			Updates(map[string]any{"deleted_at": nil, "title": title, "updated_by": userID, "updated_at": now, "lifecycle_unit_id": nil}).Error; err != nil {
			return err
		}
		if err := completeLifecycleRestore(tx, orgID, domain.LifecycleResourceMetadata, item.ID); err != nil {
			return err
		}
		if err := tx.Unscoped().Table("metadata_items").
			Select("metadata_items.*").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("metadata_items.id = ? AND folders.org_id = ?", item.ID, orgID).
			Where(visibleMetadataPredicate("metadata_items", "folders")).
			First(&restored).Error; err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		eventing.PublishMetadataRestored(ctx, orgID, restored.ID)
	}
	return restored, err
}

func lockedVisibleParentByID(tx *gorm.DB, orgID, folderID string) (domain.Folder, error) {
	var folder domain.Folder
	err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("folders.id = ? AND folders.org_id = ?", folderID, orgID).
		Where(visibleFolderPredicate("folders")).
		First(&folder).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.Folder{}, domain.ErrFolderParentDeleted
	}
	return folder, err
}
