package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing"
)

const (
	lifecycleRestoreFolderBatchSize   = 100
	lifecycleRestoreMetadataBatchSize = 1000
)

// lifecycleRestoreCheckpoint records completed, bounded restore work. The
// authoritative resume position is membership plus deleted_at: completed rows
// have their lifecycle_unit_id cleared, so a retry cannot restore them twice.
type lifecycleRestoreCheckpoint struct {
	FolderBatches   int64 `json:"folder_batches"`
	FolderRows      int64 `json:"folder_rows"`
	MetadataBatches int64 `json:"metadata_batches"`
	MetadataRows    int64 `json:"metadata_rows"`
}

func encodeLifecycleRestoreCheckpoint(checkpoint lifecycleRestoreCheckpoint) (json.RawMessage, error) {
	value, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("encode lifecycle restore checkpoint: %w", err)
	}
	return json.RawMessage(value), nil
}

func decodeLifecycleRestoreCheckpoint(value json.RawMessage) (lifecycleRestoreCheckpoint, error) {
	checkpoint := lifecycleRestoreCheckpoint{}
	if len(value) == 0 {
		return checkpoint, fmt.Errorf("lifecycle restore job has an empty checkpoint")
	}
	if err := json.Unmarshal(value, &checkpoint); err != nil {
		return checkpoint, fmt.Errorf("decode lifecycle restore checkpoint: %w", err)
	}
	return checkpoint, nil
}

// QueueLifecycleRestore turns one completed Recycle Bin unit into durable
// worker work. The source root remains tombstoned, so normal reads stay hidden
// while a worker prepares its members in bounded transactions.
func (r *assetRepository) QueueLifecycleRestore(ctx context.Context, orgID, userID, unitID string) (domain.LifecycleJob, error) {
	var queued domain.LifecycleJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}

		var unit domain.LifecycleUnit
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", unitID, orgID).
			First(&unit).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrLifecycleUnitNotFound
			}
			return err
		}

		// A network retry of the same restore request returns the already queued
		// job instead of creating duplicate workers for one lifecycle unit.
		if unit.State == domain.LifecycleRestoreQueued || unit.State == domain.LifecycleRestoring {
			err := tx.Where("unit_id = ? AND operation = ? AND status IN ?", unit.ID, domain.LifecycleJobRestore, []domain.LifecycleJobStatus{domain.LifecycleJobQueued, domain.LifecycleJobRunning}).
				Order("created_at DESC").
				First(&queued).Error
			if err == nil {
				return nil
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return fmt.Errorf("lifecycle unit %s is restoring without an active restore job", unit.ID)
		}
		if unit.State != domain.LifecycleDeleted {
			return domain.ErrLifecycleUnitNotRestorable
		}

		if err := assertLifecycleRestoreRootAndParent(tx, unit); err != nil {
			return err
		}

		checkpoint, err := encodeLifecycleRestoreCheckpoint(lifecycleRestoreCheckpoint{})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		unitID := unit.ID
		rootFolderID := unit.RootResourceID
		if unit.RootResourceType == domain.LifecycleResourceMetadata {
			if unit.OriginalFolderID == nil {
				return domain.ErrLifecycleUnitNotRestorable
			}
			rootFolderID = *unit.OriginalFolderID
		}
		queued = domain.LifecycleJob{
			ID:               uuid.NewString(),
			OrgID:            orgID,
			UnitID:           &unitID,
			RootResourceType: unit.RootResourceType,
			RootResourceID:   unit.RootResourceID,
			RootFolderID:     rootFolderID,
			RootFolderPath:   unit.RootFolderPath,
			RequestedBy:      &userID,
			Operation:        domain.LifecycleJobRestore,
			Status:           domain.LifecycleJobQueued,
			Checkpoint:       checkpoint,
			NextRunAt:        &now,
			QueuedAt:         &now,
		}
		if err := tx.Create(&queued).Error; err != nil {
			return err
		}
		return tx.Model(&unit).Update("state", domain.LifecycleRestoreQueued).Error
	})
	return queued, err
}

// assertLifecycleRestoreRootAndParent confirms both the unit/source ownership
// and the parent-first rule. It intentionally maps a missing or hidden parent
// to one lifecycle-specific error without changing any source row.
func assertLifecycleRestoreRootAndParent(tx *gorm.DB, unit domain.LifecycleUnit) error {
	switch unit.RootResourceType {
	case domain.LifecycleResourceFolder:
		var root domain.Folder
		if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ? AND lifecycle_unit_id = ?", unit.RootResourceID, unit.OrgID, unit.ID).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrLifecycleUnitNotRestorable
			}
			return err
		}
		if !root.DeletedAt.Valid {
			return domain.ErrLifecycleUnitNotRestorable
		}
		if unit.OriginalParentPath == nil {
			return nil
		}
		if _, err := lockedVisibleParent(tx, unit.OrgID, *unit.OriginalParentPath); err != nil {
			if errors.Is(err, domain.ErrFolderParentDeleted) {
				return domain.ErrRestoreParentDeleted
			}
			return err
		}
		return nil
	case domain.LifecycleResourceMetadata:
		var root domain.MetadataItem
		if err := tx.Unscoped().Table("metadata_items").
			Select("metadata_items.*").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "metadata_items"}}).
			Where("metadata_items.id = ? AND folders.org_id = ? AND metadata_items.lifecycle_unit_id = ?", unit.RootResourceID, unit.OrgID, unit.ID).
			First(&root).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrLifecycleUnitNotRestorable
			}
			return err
		}
		if !root.DeletedAt.Valid || unit.OriginalFolderID == nil {
			return domain.ErrLifecycleUnitNotRestorable
		}
		if _, err := lockedVisibleParentByID(tx, unit.OrgID, *unit.OriginalFolderID); err != nil {
			if errors.Is(err, domain.ErrFolderParentDeleted) {
				return domain.ErrRestoreParentDeleted
			}
			return err
		}
		return nil
	default:
		return domain.ErrLifecycleUnitNotRestorable
	}
}

// ClaimNextLifecycleJob is the V8 worker claim path. It keeps V5 folder
// deletion rows as a compatibility projection, but ownership belongs to the
// V8 lifecycle job so RESTORE can use the same lease discipline.
func (r *folderDeletionRepository) ClaimNextLifecycleJob(ctx context.Context, workerID string) (*domain.LifecycleJob, error) {
	var claimed *domain.LifecycleJob
	var adoptionVisibilityEvent *domain.FolderDeletionJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		adopted, visibilityChanged, err := adoptNextLegacyFolderDeletionJob(tx, now)
		if err != nil {
			return err
		}
		if visibilityChanged {
			adoptionVisibilityEvent = adopted
		}

		var job domain.LifecycleJob
		err = tx.Raw(`
			SELECT *
			FROM asset_lifecycle_jobs
			WHERE operation IN (?, ?, ?)
			  AND (
				(status = ? AND (next_run_at IS NULL OR next_run_at <= ?))
				OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?)
			  )
			ORDER BY queued_at ASC NULLS LAST, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`, domain.LifecycleJobDelete, domain.LifecycleJobRestore, domain.LifecycleJobPurge, domain.LifecycleJobQueued, now, domain.LifecycleJobRunning, now).Scan(&job).Error
		if err != nil {
			return err
		}
		if job.ID == "" {
			return nil
		}
		if job.UnitID == nil {
			return fmt.Errorf("lifecycle job %s has no unit", job.ID)
		}

		var legacyJob *domain.FolderDeletionJob
		if job.Operation == domain.LifecycleJobDelete {
			if job.LegacyFolderDeletionJobID == nil {
				return fmt.Errorf("delete lifecycle job %s has no legacy compatibility row", job.ID)
			}
			legacy := domain.FolderDeletionJob{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND org_id = ?", *job.LegacyFolderDeletionJobID, job.OrgID).
				First(&legacy).Error; err != nil {
				return err
			}
			legacyJob = &legacy
		}

		if job.Attempts >= domain.FolderDeletionMaxAttempts {
			return failClaimedLifecycleJob(tx, &job, legacyJob, now)
		}

		if job.Operation == domain.LifecycleJobRestore {
			var unit domain.LifecycleUnit
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND org_id = ? AND state IN ?", *job.UnitID, job.OrgID, []domain.LifecycleUnitState{domain.LifecycleRestoreQueued, domain.LifecycleRestoring}).
				First(&unit).Error; err != nil {
				return err
			}
			if err := assertLifecycleRestoreRootAndParent(tx, unit); err != nil {
				return err
			}
			if err := tx.Model(&unit).Update("state", domain.LifecycleRestoring).Error; err != nil {
				return err
			}
		}
		if job.Operation == domain.LifecycleJobPurge {
			result := tx.Model(&domain.LifecycleUnit{}).
				Where("id = ? AND org_id = ? AND state IN ?", *job.UnitID, job.OrgID, []domain.LifecycleUnitState{domain.LifecyclePurgeQueued, domain.LifecyclePurging}).
				Update("state", domain.LifecyclePurging)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("purge lifecycle job %s does not own a purgeable unit", job.ID)
			}
		}

		leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
		job.Status = domain.LifecycleJobRunning
		job.Attempts++
		job.LeaseOwner = &workerID
		job.LeaseExpiresAt = &leaseExpiresAt
		job.NextRunAt = nil
		if job.StartedAt == nil {
			job.StartedAt = &now
		}
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		if legacyJob != nil {
			legacyJob.Status = domain.FolderDeletionRunning
			legacyJob.Attempts = job.Attempts
			legacyJob.LeaseOwner = &workerID
			legacyJob.LeaseExpiresAt = &leaseExpiresAt
			legacyJob.NextRunAt = nil
			if legacyJob.StartedAt == nil {
				legacyJob.StartedAt = &now
			}
			if err := tx.Save(legacyJob).Error; err != nil {
				return err
			}
		}
		claimed = &job
		return nil
	})
	if err == nil && adoptionVisibilityEvent != nil {
		eventing.PublishFolderDeleted(ctx, adoptionVisibilityEvent.OrgID, adoptionVisibilityEvent.RootFolderID, adoptionVisibilityEvent.RootPath, adoptionVisibilityEvent.ID)
	}
	return claimed, err
}

// RenewLifecyclePurgeLease extends a claimed PURGE job only while the exact
// lease handed to this worker is still current. Matching the previous expiry
// prevents a stale worker from extending a lease after ownership changed.
func (r *folderDeletionRepository) RenewLifecyclePurgeLease(
	ctx context.Context,
	jobID, workerID string,
	expectedExpiry time.Time,
) (time.Time, error) {
	var renewed struct{ LeaseExpiresAt time.Time }
	result := r.db.WithContext(ctx).Raw(`
		UPDATE asset_lifecycle_jobs
		SET lease_expires_at = statement_timestamp() + make_interval(secs => ?)
		WHERE id = ?
		  AND operation = ?
		  AND status = ?
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()
		RETURNING lease_expires_at`,
		domain.FolderDeletionLeaseDuration.Seconds(),
		jobID,
		domain.LifecycleJobPurge,
		domain.LifecycleJobRunning,
		workerID,
		expectedExpiry.UTC(),
	).Scan(&renewed)
	if result.Error != nil {
		return time.Time{}, fmt.Errorf("renew lifecycle purge lease for job %s: %w", jobID, result.Error)
	}
	if result.RowsAffected != 1 {
		return time.Time{}, fmt.Errorf("lifecycle purge lease was not held for job %s", jobID)
	}
	return renewed.LeaseExpiresAt, nil
}

func failClaimedLifecycleJob(tx *gorm.DB, job *domain.LifecycleJob, legacyJob *domain.FolderDeletionJob, now time.Time) error {
	code := "INTERNAL_ERROR"
	job.Status = domain.LifecycleJobFailed
	job.NextRunAt = nil
	job.LeaseOwner = nil
	job.LeaseExpiresAt = nil
	job.FailureCode = &code
	job.CompletedAt = &now
	if err := tx.Save(job).Error; err != nil {
		return err
	}
	if job.UnitID != nil {
		states := []domain.LifecycleUnitState(nil)
		switch job.Operation {
		case domain.LifecycleJobRestore:
			states = []domain.LifecycleUnitState{domain.LifecycleRestoreQueued, domain.LifecycleRestoring}
		case domain.LifecycleJobPurge:
			states = []domain.LifecycleUnitState{domain.LifecyclePurgeQueued, domain.LifecyclePurging}
		}
		if len(states) > 0 {
			if err := tx.Model(&domain.LifecycleUnit{}).
				Where("id = ? AND org_id = ? AND state IN ?", *job.UnitID, job.OrgID, states).
				Update("state", domain.LifecycleFailed).Error; err != nil {
				return err
			}
		}
	}
	if legacyJob == nil {
		return nil
	}
	legacyJob.Status = domain.FolderDeletionFailed
	legacyJob.Attempts = job.Attempts
	legacyJob.NextRunAt = nil
	legacyJob.LeaseOwner = nil
	legacyJob.LeaseExpiresAt = nil
	legacyJob.LastErrorCode = &code
	legacyJob.CompletedAt = &now
	return tx.Save(legacyJob).Error
}

// ProcessLifecycleJob dispatches the worker-owned operation. The V5 folder
// deletion implementation remains the compatibility adapter for DELETE while
// RESTORE is native to the V8 lifecycle job model.
func (r *folderDeletionRepository) ProcessLifecycleJob(ctx context.Context, jobID, workerID string) error {
	var job domain.LifecycleJob
	if err := r.db.WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		return err
	}
	switch job.Operation {
	case domain.LifecycleJobDelete:
		if job.LegacyFolderDeletionJobID == nil {
			return fmt.Errorf("delete lifecycle job %s has no legacy compatibility row", job.ID)
		}
		return r.ProcessFolderDeletionJob(ctx, *job.LegacyFolderDeletionJobID, workerID)
	case domain.LifecycleJobRestore:
		return r.processLifecycleRestoreJob(ctx, jobID, workerID)
	default:
		return fmt.Errorf("unsupported lifecycle operation %s", job.Operation)
	}
}

// FailLifecycleJob applies bounded retry to native RESTORE and PURGE jobs. A
// failed operation keeps the root tombstone in place; no partial member becomes visible.
func (r *folderDeletionRepository) FailLifecycleJob(ctx context.Context, jobID, workerID string) error {
	var job domain.LifecycleJob
	if err := r.db.WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
		return err
	}
	if job.Operation == domain.LifecycleJobDelete {
		if job.LegacyFolderDeletionJobID == nil {
			return fmt.Errorf("delete lifecycle job %s has no legacy compatibility row", job.ID)
		}
		return r.FailFolderDeletionJob(ctx, *job.LegacyFolderDeletionJobID, workerID)
	}
	if job.Operation != domain.LifecycleJobRestore && job.Operation != domain.LifecycleJobPurge {
		return fmt.Errorf("unsupported lifecycle operation %s", job.Operation)
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked domain.LifecycleJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND operation = ? AND status = ? AND lease_owner = ?", jobID, job.Operation, domain.LifecycleJobRunning, workerID).
			First(&locked).Error; err != nil {
			return err
		}
		if locked.UnitID == nil {
			return fmt.Errorf("lifecycle %s job %s has no unit", locked.Operation, locked.ID)
		}
		now := time.Now().UTC()
		code := "INTERNAL_ERROR"
		locked.FailureCode = &code
		locked.LeaseOwner = nil
		locked.LeaseExpiresAt = nil
		if locked.Attempts >= domain.FolderDeletionMaxAttempts {
			locked.Status = domain.LifecycleJobFailed
			locked.NextRunAt = nil
			locked.CompletedAt = &now
			if err := tx.Save(&locked).Error; err != nil {
				return err
			}
			state := domain.LifecycleRestoring
			if locked.Operation == domain.LifecycleJobPurge {
				state = domain.LifecyclePurging
			}
			return tx.Model(&domain.LifecycleUnit{}).
				Where("id = ? AND org_id = ? AND state = ?", *locked.UnitID, locked.OrgID, state).
				Update("state", domain.LifecycleFailed).Error
		}
		nextRunAt := now.Add(automaticRetryDelay(locked.Attempts))
		locked.Status = domain.LifecycleJobQueued
		locked.NextRunAt = &nextRunAt
		if err := tx.Save(&locked).Error; err != nil {
			return err
		}
		state := domain.LifecycleRestoring
		queuedState := domain.LifecycleRestoreQueued
		if locked.Operation == domain.LifecycleJobPurge {
			state = domain.LifecyclePurging
			queuedState = domain.LifecyclePurgeQueued
		}
		return tx.Model(&domain.LifecycleUnit{}).
			Where("id = ? AND org_id = ? AND state = ?", *locked.UnitID, locked.OrgID, state).
			Update("state", queuedState).Error
	})
}

type lifecycleRestoreCompletion struct {
	resourceType domain.LifecycleResourceType
	resourceID   string
	rootPath     string
	orgID        string
}

func (r *folderDeletionRepository) processLifecycleRestoreJob(ctx context.Context, jobID, workerID string) error {
	for {
		done, completion, err := r.processLifecycleRestoreBatch(ctx, jobID, workerID)
		if err != nil {
			return err
		}
		if !done {
			continue
		}
		if completion.resourceType == domain.LifecycleResourceFolder {
			eventing.PublishFolderRestored(ctx, completion.orgID, completion.resourceID, completion.rootPath)
		} else {
			eventing.PublishMetadataRestored(ctx, completion.orgID, completion.resourceID)
		}
		return nil
	}
}

func (r *folderDeletionRepository) processLifecycleRestoreBatch(ctx context.Context, jobID, workerID string) (bool, lifecycleRestoreCompletion, error) {
	completion := lifecycleRestoreCompletion{}
	var done bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job domain.LifecycleJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND operation = ? AND status = ? AND lease_owner = ?", jobID, domain.LifecycleJobRestore, domain.LifecycleJobRunning, workerID).
			First(&job).Error; err != nil {
			return err
		}
		if job.UnitID == nil {
			return fmt.Errorf("restore lifecycle job %s has no unit", job.ID)
		}
		if err := lockOrganizationDeletion(tx, job.OrgID); err != nil {
			return err
		}
		var unit domain.LifecycleUnit
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ? AND state = ?", *job.UnitID, job.OrgID, domain.LifecycleRestoring).
			First(&unit).Error; err != nil {
			return err
		}
		if err := assertLifecycleRestoreRootAndParent(tx, unit); err != nil {
			return err
		}
		checkpoint, err := decodeLifecycleRestoreCheckpoint(job.Checkpoint)
		if err != nil {
			return err
		}
		if unit.RootResourceType == domain.LifecycleResourceMetadata {
			if err := restoreMetadataRoot(tx, &job, unit, &checkpoint); err != nil {
				return err
			}
			completion = lifecycleRestoreCompletion{resourceType: unit.RootResourceType, resourceID: unit.RootResourceID, rootPath: unit.RootFolderPath, orgID: unit.OrgID}
			done = true
			return nil
		}

		changed, err := restoreFolderMemberBatch(tx, &job, unit, &checkpoint)
		if err != nil {
			return err
		}
		if changed {
			return nil
		}
		changed, err = restoreMetadataMemberBatch(tx, &job, unit, &checkpoint)
		if err != nil {
			return err
		}
		if changed {
			return nil
		}
		if err := restoreFolderRoot(tx, &job, unit, &checkpoint); err != nil {
			return err
		}
		completion = lifecycleRestoreCompletion{resourceType: unit.RootResourceType, resourceID: unit.RootResourceID, rootPath: unit.RootFolderPath, orgID: unit.OrgID}
		done = true
		return nil
	})
	return done, completion, err
}

func activeStoredSiblingFolderNames(tx *gorm.DB, orgID, parentPath, excludeID string) (map[string]struct{}, error) {
	var names []string
	query := tx.Unscoped().Table("folders").
		Select("folders.name").
		Where("folders.org_id = ? AND folders.id != ? AND folders.deleted_at IS NULL", orgID, excludeID)
	if parentPath == "" {
		query = query.Where("nlevel(folders.path) = 1")
	} else {
		query = query.Where("folders.path <@ ?::ltree AND nlevel(folders.path) = nlevel(?::ltree) + 1", parentPath, parentPath)
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

func activeStoredMetadataTitles(tx *gorm.DB, orgID, folderID, excludeID string) (map[string]struct{}, error) {
	var titles []string
	err := tx.Unscoped().Table("metadata_items").
		Select("metadata_items.title").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.folder_id = ? AND metadata_items.id != ? AND folders.org_id = ? AND metadata_items.deleted_at IS NULL", folderID, excludeID, orgID).
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

func ensureNoStoredExternalIdentityConflict(tx *gorm.DB, orgID string, item domain.MetadataItem) error {
	if item.ExternalSource == nil || item.ExternalID == nil {
		return nil
	}
	var count int64
	err := tx.Unscoped().Table("metadata_items").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Where("metadata_items.id != ? AND metadata_items.external_source = ? AND metadata_items.external_id = ? AND folders.org_id = ? AND metadata_items.deleted_at IS NULL", item.ID, *item.ExternalSource, *item.ExternalID, orgID).
		Count(&count).Error
	if err != nil {
		return err
	}
	if count > 0 {
		return domain.ErrMetadataConflict
	}
	return nil
}

func saveRestoreProgress(tx *gorm.DB, job *domain.LifecycleJob, checkpoint lifecycleRestoreCheckpoint, now time.Time) error {
	value, err := encodeLifecycleRestoreCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
	job.Checkpoint = value
	job.LeaseExpiresAt = &leaseExpiresAt
	return tx.Save(job).Error
}

func restoreFolderMemberBatch(tx *gorm.DB, job *domain.LifecycleJob, unit domain.LifecycleUnit, checkpoint *lifecycleRestoreCheckpoint) (bool, error) {
	var folders []domain.Folder
	err := tx.Raw(`
		SELECT *
		FROM folders
		WHERE org_id = ?
		  AND lifecycle_unit_id = ?
		  AND id != ?
		  AND deleted_at IS NOT NULL
		ORDER BY nlevel(path) ASC, path ASC
		LIMIT ?
		FOR UPDATE SKIP LOCKED
	`, job.OrgID, unit.ID, unit.RootResourceID, lifecycleRestoreFolderBatchSize).Scan(&folders).Error
	if err != nil {
		return false, err
	}
	if len(folders) == 0 {
		return false, nil
	}
	for _, folder := range folders {
		parentPath := lifecycleParentPath(folder.Path)
		resolvedParentPath := ""
		if parentPath != nil {
			resolvedParentPath = *parentPath
		}
		used, err := activeStoredSiblingFolderNames(tx, job.OrgID, resolvedParentPath, folder.ID)
		if err != nil {
			return false, err
		}
		name := nextAvailableDisplayValue(folder.Name, used)
		result := tx.Unscoped().Model(&domain.Folder{}).
			Where("id = ? AND org_id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL", folder.ID, job.OrgID, unit.ID).
			Updates(map[string]any{"deleted_at": nil, "lifecycle_unit_id": nil, "name": name, "updated_by": job.RequestedBy, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, fmt.Errorf("restore lifecycle folder member %s lost its ownership", folder.ID)
		}
	}
	checkpoint.FolderBatches++
	checkpoint.FolderRows += int64(len(folders))
	return true, saveRestoreProgress(tx, job, *checkpoint, time.Now().UTC())
}

func restoreMetadataMemberBatch(tx *gorm.DB, job *domain.LifecycleJob, unit domain.LifecycleUnit, checkpoint *lifecycleRestoreCheckpoint) (bool, error) {
	var items []domain.MetadataItem
	err := tx.Raw(`
		SELECT metadata_items.*
		FROM metadata_items
		JOIN folders ON folders.id = metadata_items.folder_id
		WHERE folders.org_id = ?
		  AND metadata_items.lifecycle_unit_id = ?
		  AND metadata_items.deleted_at IS NOT NULL
		ORDER BY metadata_items.id ASC
		LIMIT ?
		FOR UPDATE OF metadata_items SKIP LOCKED
	`, job.OrgID, unit.ID, lifecycleRestoreMetadataBatchSize).Scan(&items).Error
	if err != nil {
		return false, err
	}
	if len(items) == 0 {
		return false, nil
	}
	for _, item := range items {
		if err := ensureNoStoredExternalIdentityConflict(tx, job.OrgID, item); err != nil {
			return false, err
		}
		used, err := activeStoredMetadataTitles(tx, job.OrgID, item.FolderID, item.ID)
		if err != nil {
			return false, err
		}
		title := nextAvailableDisplayValue(item.Title, used)
		result := tx.Unscoped().Model(&domain.MetadataItem{}).
			Where("id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL", item.ID, unit.ID).
			Updates(map[string]any{"deleted_at": nil, "lifecycle_unit_id": nil, "title": title, "updated_by": job.RequestedBy, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, fmt.Errorf("restore lifecycle metadata member %s lost its ownership", item.ID)
		}
	}
	checkpoint.MetadataBatches++
	checkpoint.MetadataRows += int64(len(items))
	return true, saveRestoreProgress(tx, job, *checkpoint, time.Now().UTC())
}

func completeRestoreJob(tx *gorm.DB, job *domain.LifecycleJob, unit domain.LifecycleUnit, checkpoint lifecycleRestoreCheckpoint) error {
	now := time.Now().UTC()
	value, err := encodeLifecycleRestoreCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	job.Status = domain.LifecycleJobSucceeded
	job.Checkpoint = value
	job.NextRunAt = nil
	job.LeaseOwner = nil
	job.LeaseExpiresAt = nil
	job.CompletedAt = &now
	if err := tx.Save(job).Error; err != nil {
		return err
	}
	return tx.Model(&domain.LifecycleUnit{}).
		Where("id = ? AND org_id = ? AND state = ?", unit.ID, unit.OrgID, domain.LifecycleRestoring).
		Update("state", domain.LifecycleRestored).Error
}

func restoreFolderRoot(tx *gorm.DB, job *domain.LifecycleJob, unit domain.LifecycleUnit, checkpoint *lifecycleRestoreCheckpoint) error {
	var root domain.Folder
	if err := tx.Unscoped().Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND org_id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL", unit.RootResourceID, unit.OrgID, unit.ID).
		First(&root).Error; err != nil {
		return err
	}
	parentPath := ""
	if unit.OriginalParentPath != nil {
		parentPath = *unit.OriginalParentPath
	}
	used, err := activeStoredSiblingFolderNames(tx, unit.OrgID, parentPath, root.ID)
	if err != nil {
		return err
	}
	name := nextAvailableDisplayValue(root.Name, used)
	result := tx.Unscoped().Model(&domain.Folder{}).
		Where("id = ? AND org_id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL", root.ID, unit.OrgID, unit.ID).
		Updates(map[string]any{"deleted_at": nil, "lifecycle_unit_id": nil, "name": name, "updated_by": job.RequestedBy, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("restore lifecycle root %s lost its ownership", root.ID)
	}
	return completeRestoreJob(tx, job, unit, *checkpoint)
}

func restoreMetadataRoot(tx *gorm.DB, job *domain.LifecycleJob, unit domain.LifecycleUnit, checkpoint *lifecycleRestoreCheckpoint) error {
	var root domain.MetadataItem
	if err := tx.Unscoped().Table("metadata_items").
		Select("metadata_items.*").
		Joins("JOIN folders ON folders.id = metadata_items.folder_id").
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "metadata_items"}}).
		Where("metadata_items.id = ? AND folders.org_id = ? AND metadata_items.lifecycle_unit_id = ? AND metadata_items.deleted_at IS NOT NULL", unit.RootResourceID, unit.OrgID, unit.ID).
		First(&root).Error; err != nil {
		return err
	}
	if err := ensureNoStoredExternalIdentityConflict(tx, unit.OrgID, root); err != nil {
		return err
	}
	used, err := activeStoredMetadataTitles(tx, unit.OrgID, root.FolderID, root.ID)
	if err != nil {
		return err
	}
	title := nextAvailableDisplayValue(root.Title, used)
	result := tx.Unscoped().Model(&domain.MetadataItem{}).
		Where("id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL", root.ID, unit.ID).
		Updates(map[string]any{"deleted_at": nil, "lifecycle_unit_id": nil, "title": title, "updated_by": job.RequestedBy, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("restore lifecycle metadata root %s lost its ownership", root.ID)
	}
	checkpoint.MetadataBatches++
	checkpoint.MetadataRows++
	return completeRestoreJob(tx, job, unit, *checkpoint)
}
