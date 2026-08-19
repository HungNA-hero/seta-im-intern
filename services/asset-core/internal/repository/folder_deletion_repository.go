package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

type folderDeletionRepository struct {
	db *gorm.DB
}

// folderDeletionCheckpoint records the completed bounded batches. The
// tombstoned source rows are the authoritative resume position: each later
// query selects only deleted_at IS NULL rows. The counters make that implicit,
// durable checkpoint observable without storing a cursor that can go stale.
type folderDeletionCheckpoint struct {
	RootVisibilityClosed bool  `json:"root_visibility_closed"`
	MetadataBatches      int64 `json:"metadata_batches"`
	MetadataRows         int64 `json:"metadata_rows"`
	FolderBatches        int64 `json:"folder_batches"`
	FolderRows           int64 `json:"folder_rows"`
}

func decodeFolderDeletionCheckpoint(value json.RawMessage) (folderDeletionCheckpoint, error) {
	checkpoint := folderDeletionCheckpoint{}
	if len(value) == 0 {
		return checkpoint, fmt.Errorf("lifecycle delete job has an empty checkpoint")
	}
	if err := json.Unmarshal(value, &checkpoint); err != nil {
		return checkpoint, fmt.Errorf("decode lifecycle delete checkpoint: %w", err)
	}
	return checkpoint, nil
}

func encodeFolderDeletionCheckpoint(checkpoint folderDeletionCheckpoint) (json.RawMessage, error) {
	value, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("encode lifecycle delete checkpoint: %w", err)
	}
	return json.RawMessage(value), nil
}

func NewFolderDeletionRepository(db *gorm.DB) domain.FolderDeletionRepository {
	return &folderDeletionRepository{db: db}
}

// NewLifecycleJobWorkerRepository exposes the V8 worker engine without
// widening the V5 public folder-deletion contract.
func NewLifecycleJobWorkerRepository(db *gorm.DB) domain.LifecycleJobWorkerRepository {
	return &folderDeletionRepository{db: db}
}

func newConfirmationToken() (string, []byte, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(bytes)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func countFolderDeletionRows(tx *gorm.DB, orgID, rootPath string) (activeFolders, activeMetadata, tombstoneFolders, tombstoneMetadata int64, err error) {
	folderRows := func() *gorm.DB {
		return tx.Unscoped().Model(&domain.Folder{}).Where("org_id = ? AND path <@ ?::ltree", orgID, rootPath)
	}
	if err = folderRows().Where("deleted_at IS NULL").Count(&activeFolders).Error; err != nil {
		return
	}
	if err = folderRows().Where("deleted_at IS NOT NULL").Count(&tombstoneFolders).Error; err != nil {
		return
	}
	metadataRows := func() *gorm.DB {
		return tx.Unscoped().Table("metadata_items").
			Joins("JOIN folders ON folders.id = metadata_items.folder_id").
			Where("folders.org_id = ? AND folders.path <@ ?::ltree", orgID, rootPath)
	}
	if err = metadataRows().Where("metadata_items.deleted_at IS NULL").Count(&activeMetadata).Error; err != nil {
		return
	}
	err = metadataRows().Where("metadata_items.deleted_at IS NOT NULL").Count(&tombstoneMetadata).Error
	return
}

func previewFromJob(job domain.FolderDeletionJob, token string) domain.FolderDeletionPreview {
	expiresAt := time.Time{}
	if job.PreviewExpiresAt != nil {
		expiresAt = *job.PreviewExpiresAt
	}
	return domain.FolderDeletionPreview{
		ID:                     job.ID,
		RootFolderID:           job.RootFolderID,
		ActiveFolderCount:      job.ActiveFolderCount,
		ActiveMetadataCount:    job.ActiveMetadataCount,
		TombstoneFolderCount:   job.TombstoneFolderCount,
		TombstoneMetadataCount: job.TombstoneMetadataCount,
		TotalRows:              job.TotalRows(),
		ConfirmationToken:      token,
		ExpiresAt:              expiresAt,
	}
}

func (r *folderDeletionRepository) PreviewFolderDeletion(ctx context.Context, orgID, userID, folderID string) (domain.FolderDeletionPreview, error) {
	var preview domain.FolderDeletionPreview
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationWrite(tx, orgID); err != nil {
			return err
		}

		var folder domain.Folder
		if err := tx.Where("id = ? AND org_id = ?", folderID, orgID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrFolderNotFound
			}
			return err
		}
		if err := ensureNoActiveDeletionForPaths(tx, orgID, folder.Path); err != nil {
			return err
		}

		activeFolders, activeMetadata, tombstoneFolders, tombstoneMetadata, err := countFolderDeletionRows(tx, orgID, folder.Path)
		if err != nil {
			return err
		}
		token, tokenHash, err := newConfirmationToken()
		if err != nil {
			return err
		}
		expiresAt := time.Now().UTC().Add(domain.FolderDeletionPreviewTTL)
		job := domain.FolderDeletionJob{
			ID:                     uuid.NewString(),
			OrgID:                  orgID,
			RootFolderID:           folder.ID,
			RootPath:               folder.Path,
			RequestedBy:            userID,
			Status:                 domain.FolderDeletionPreviewed,
			ConfirmationTokenHash:  tokenHash,
			PreviewExpiresAt:       &expiresAt,
			ActiveFolderCount:      activeFolders,
			ActiveMetadataCount:    activeMetadata,
			TombstoneFolderCount:   tombstoneFolders,
			TombstoneMetadataCount: tombstoneMetadata,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		preview = previewFromJob(job, token)
		return nil
	})
	return preview, err
}

func (r *folderDeletionRepository) ConfirmFolderDeletion(ctx context.Context, orgID, userID, folderID, previewID, token string) (domain.FolderDeletionJob, error) {
	var confirmed domain.FolderDeletionJob
	var visibilityChanged bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}

		var job domain.FolderDeletionJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ? AND requested_by = ? AND status = ?", previewID, orgID, userID, domain.FolderDeletionPreviewed).
			First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrDeletionPreviewStale
			}
			return err
		}
		now := time.Now().UTC()
		tokenHash := sha256.Sum256([]byte(token))
		if job.PreviewExpiresAt == nil || !now.Before(*job.PreviewExpiresAt) ||
			subtle.ConstantTimeCompare(job.ConfirmationTokenHash, tokenHash[:]) != 1 ||
			job.RootFolderID != folderID {
			if err := tx.Delete(&job).Error; err != nil {
				return err
			}
			return domain.ErrDeletionPreviewStale
		}

		var folder domain.Folder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", folderID, orgID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return domain.ErrDeletionPreviewStale
			}
			return err
		}
		if folder.Path != job.RootPath {
			return domain.ErrDeletionPreviewStale
		}

		var overlap int64
		if err := tx.Model(&domain.FolderDeletionJob{}).
			Where("id != ? AND org_id = ? AND status IN ? AND (root_path @> ?::ltree OR root_path <@ ?::ltree)",
				job.ID,
				orgID,
				[]domain.FolderDeletionJobStatus{domain.FolderDeletionQueued, domain.FolderDeletionRunning, domain.FolderDeletionFailed},
				folder.Path,
				folder.Path,
			).
			Count(&overlap).Error; err != nil {
			return err
		}
		if overlap > 0 {
			return domain.ErrFolderDeletionInProgress
		}

		activeFolders, activeMetadata, tombstoneFolders, tombstoneMetadata, err := countFolderDeletionRows(tx, orgID, folder.Path)
		if err != nil {
			return err
		}
		if job.ActiveFolderCount != activeFolders || job.ActiveMetadataCount != activeMetadata ||
			job.TombstoneFolderCount != tombstoneFolders || job.TombstoneMetadataCount != tombstoneMetadata {
			if err := tx.Delete(&job).Error; err != nil {
				return err
			}
			return domain.ErrDeletionPreviewStale
		}

		job.Status = domain.FolderDeletionQueued
		job.ConfirmationTokenHash = nil
		job.PreviewExpiresAt = nil
		job.QueuedAt = &now
		job.NextRunAt = &now

		lifecycleUnitID, gateClosed, err := closeFolderDeletionVisibilityGate(tx, job, now)
		if err != nil {
			return err
		}
		visibilityChanged = gateClosed
		if gateClosed {
			job.DeletedFolderCount++
		}

		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		if _, err := createFolderDeletionLifecycleJob(tx, job, lifecycleUnitID, now); err != nil {
			return err
		}
		confirmed = job
		return nil
	})
	if err == nil && visibilityChanged {
		eventing.PublishFolderDeleted(ctx, orgID, folderID, confirmed.RootPath, confirmed.ID)
	}
	return confirmed, err
}

func authorizeFolderDeletionJob(job domain.FolderDeletionJob, actorID string, actorIsOrgAdmin bool) error {
	if job.RequestedBy != actorID && !actorIsOrgAdmin {
		return domain.ErrDeletionJobNotFound
	}
	return nil
}

func (r *folderDeletionRepository) getAuthorizedJob(tx *gorm.DB, orgID, actorID, jobID string, actorIsOrgAdmin bool, lock bool) (domain.FolderDeletionJob, error) {
	var job domain.FolderDeletionJob
	query := tx.Where("id = ? AND org_id = ? AND status != ?", jobID, orgID, domain.FolderDeletionPreviewed)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return job, domain.ErrDeletionJobNotFound
		}
		return job, err
	}
	if err := authorizeFolderDeletionJob(job, actorID, actorIsOrgAdmin); err != nil {
		return job, err
	}
	return job, nil
}

func (r *folderDeletionRepository) GetFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	return r.getAuthorizedJob(r.db.WithContext(ctx), orgID, actorID, jobID, actorIsOrgAdmin, false)
}

func (r *folderDeletionRepository) CancelFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	var cancelled domain.FolderDeletionJob
	var visibilityRestored bool
	var restoredRootPath string
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		// Read first for authorization, then lock the worker-owned V8 row before
		// taking the V5 compatibility row. Worker, retry and cancel all follow
		// that order so a claim cannot race a cancellation into two outcomes.
		preview, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, false)
		if err != nil {
			return err
		}
		if preview.Status != domain.FolderDeletionQueued {
			return domain.ErrDeletionJobNotCancellable
		}
		lifecycleJob, hasLifecycleJob, err := findLifecycleJobForFolderDeletion(tx, preview.ID, true)
		if err != nil {
			return err
		}
		job, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, true)
		if err != nil {
			return err
		}
		if job.Status != domain.FolderDeletionQueued || (hasLifecycleJob && lifecycleJob.Status != domain.LifecycleJobQueued) {
			return domain.ErrDeletionJobNotCancellable
		}

		// A queued job created by this version has already hidden its root. A
		// worker has not claimed it yet, so no descendant was changed and it is
		// safe to reopen just the root. The restored unit and suppressed V8 job
		// retain the lightweight lifecycle audit trail.
		var unit domain.LifecycleUnit
		unitErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ? AND state = ?", job.OrgID, domain.LifecycleResourceFolder, job.RootFolderID, domain.LifecycleDeleting).
			First(&unit).Error
		if unitErr != nil && !errors.Is(unitErr, gorm.ErrRecordNotFound) {
			return unitErr
		}
		if unitErr == nil {
			rootResult := tx.Exec(`
				UPDATE folders
				SET deleted_at = NULL, lifecycle_unit_id = NULL
				WHERE id = ? AND org_id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL
			`, job.RootFolderID, job.OrgID, unit.ID)
			if rootResult.Error != nil {
				return rootResult.Error
			}
			if rootResult.RowsAffected != 1 {
				return fmt.Errorf("lifecycle deletion root %s is not safely cancellable", job.RootFolderID)
			}
			if err := tx.Model(&unit).Update("state", domain.LifecycleRestored).Error; err != nil {
				return err
			}
			visibilityRestored = true
			restoredRootPath = job.RootPath
		}

		now := time.Now().UTC()
		if hasLifecycleJob {
			lifecycleJob.Status = domain.LifecycleJobSuppressed
			lifecycleJob.NextRunAt = nil
			lifecycleJob.CompletedAt = &now
			if err := tx.Save(&lifecycleJob).Error; err != nil {
				return err
			}
		}
		job.Status = domain.FolderDeletionCancelled
		job.CancelledAt = &now
		job.NextRunAt = nil
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		cancelled = job
		return nil
	})
	if err == nil && visibilityRestored {
		eventing.PublishFolderRestored(ctx, orgID, cancelled.RootFolderID, restoredRootPath)
	}
	return cancelled, err
}

func (r *folderDeletionRepository) RetryFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	var retried domain.FolderDeletionJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		preview, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, false)
		if err != nil {
			return err
		}
		if preview.Status != domain.FolderDeletionFailed {
			return domain.ErrDeletionJobNotCancellable
		}
		lifecycleJob, hasLifecycleJob, err := findLifecycleJobForFolderDeletion(tx, preview.ID, true)
		if err != nil {
			return err
		}
		job, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, true)
		if err != nil {
			return err
		}
		if job.Status != domain.FolderDeletionFailed || (hasLifecycleJob && lifecycleJob.Status != domain.LifecycleJobFailed) {
			return domain.ErrDeletionJobNotCancellable
		}
		now := time.Now().UTC()
		if hasLifecycleJob {
			lifecycleJob.Status = domain.LifecycleJobQueued
			lifecycleJob.Attempts = 0
			lifecycleJob.NextRunAt = &now
			lifecycleJob.LeaseOwner = nil
			lifecycleJob.LeaseExpiresAt = nil
			lifecycleJob.FailureCode = nil
			lifecycleJob.CompletedAt = nil
			if err := tx.Save(&lifecycleJob).Error; err != nil {
				return err
			}
		}
		job.Status = domain.FolderDeletionQueued
		job.ManualRetries++
		job.Attempts = 0
		job.NextRunAt = &now
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		job.LastErrorCode = nil
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		retried = job
		return nil
	})
	return retried, err
}

func (r *folderDeletionRepository) ClaimNextFolderDeletionJob(ctx context.Context, workerID string) (*domain.FolderDeletionJob, error) {
	var claimed *domain.FolderDeletionJob
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

		var lifecycleJob domain.LifecycleJob
		err = tx.Raw(`
			SELECT *
			FROM asset_lifecycle_jobs
			WHERE operation = ?
			  AND legacy_folder_deletion_job_id IS NOT NULL
			  AND (
				(status = ? AND (next_run_at IS NULL OR next_run_at <= ?))
				OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?)
			  )
			ORDER BY queued_at ASC NULLS LAST, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`, domain.LifecycleJobDelete, domain.LifecycleJobQueued, now, domain.LifecycleJobRunning, now).Scan(&lifecycleJob).Error
		if err != nil {
			return err
		}
		if lifecycleJob.ID == "" {
			return nil
		}
		if lifecycleJob.LegacyFolderDeletionJobID == nil {
			return fmt.Errorf("delete lifecycle job %s has no legacy compatibility row", lifecycleJob.ID)
		}

		var job domain.FolderDeletionJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND org_id = ?", *lifecycleJob.LegacyFolderDeletionJobID, lifecycleJob.OrgID).
			First(&job).Error; err != nil {
			return err
		}
		if lifecycleJob.Attempts >= domain.FolderDeletionMaxAttempts {
			lifecycleJob.Status = domain.LifecycleJobFailed
			lifecycleJob.NextRunAt = nil
			lifecycleJob.LeaseOwner = nil
			lifecycleJob.LeaseExpiresAt = nil
			code := "INTERNAL_ERROR"
			lifecycleJob.FailureCode = &code
			lifecycleJob.CompletedAt = &now
			if err := tx.Save(&lifecycleJob).Error; err != nil {
				return err
			}
			job.Status = domain.FolderDeletionFailed
			job.Attempts = lifecycleJob.Attempts
			job.NextRunAt = nil
			job.LeaseOwner = nil
			job.LeaseExpiresAt = nil
			job.LastErrorCode = &code
			return tx.Save(&job).Error
		}
		leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
		lifecycleJob.Status = domain.LifecycleJobRunning
		lifecycleJob.Attempts++
		lifecycleJob.LeaseOwner = &workerID
		lifecycleJob.LeaseExpiresAt = &leaseExpiresAt
		lifecycleJob.NextRunAt = nil
		if lifecycleJob.StartedAt == nil {
			lifecycleJob.StartedAt = &now
		}
		if err := tx.Save(&lifecycleJob).Error; err != nil {
			return err
		}
		job.Status = domain.FolderDeletionRunning
		job.Attempts = lifecycleJob.Attempts
		job.LeaseOwner = &workerID
		job.LeaseExpiresAt = &leaseExpiresAt
		job.NextRunAt = nil
		if job.StartedAt == nil {
			job.StartedAt = &now
		}
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		claimed = &job
		return nil
	})
	if err == nil && adoptionVisibilityEvent != nil {
		eventing.PublishFolderDeleted(ctx, adoptionVisibilityEvent.OrgID, adoptionVisibilityEvent.RootFolderID, adoptionVisibilityEvent.RootPath, adoptionVisibilityEvent.ID)
	}
	return claimed, err
}

// adoptNextLegacyFolderDeletionJob prevents a queued V5 row from being
// stranded when the worker changes to V8 ownership. It is deliberately bounded
// to one row per poll and only adopts a queued job or an expired legacy lease.
func adoptNextLegacyFolderDeletionJob(tx *gorm.DB, now time.Time) (*domain.FolderDeletionJob, bool, error) {
	var legacyJob domain.FolderDeletionJob
	err := tx.Raw(`
		SELECT legacy_jobs.*
		FROM folder_deletion_jobs AS legacy_jobs
		WHERE (
				(legacy_jobs.status = ? AND (legacy_jobs.next_run_at IS NULL OR legacy_jobs.next_run_at <= ?))
				OR (legacy_jobs.status = ? AND legacy_jobs.lease_expires_at IS NOT NULL AND legacy_jobs.lease_expires_at < ?)
			)
			AND NOT EXISTS (
				SELECT 1
				FROM asset_lifecycle_jobs AS lifecycle_jobs
				WHERE lifecycle_jobs.legacy_folder_deletion_job_id = legacy_jobs.id
			)
		ORDER BY legacy_jobs.queued_at ASC NULLS LAST, legacy_jobs.created_at ASC
		FOR UPDATE OF legacy_jobs SKIP LOCKED
		LIMIT 1
	`, domain.FolderDeletionQueued, now, domain.FolderDeletionRunning, now).Scan(&legacyJob).Error
	if err != nil {
		return nil, false, err
	}
	if legacyJob.ID == "" {
		return nil, false, nil
	}

	unitID, visibilityChanged, err := closeFolderDeletionVisibilityGate(tx, legacyJob, now)
	if err != nil {
		return nil, false, err
	}
	if visibilityChanged {
		legacyJob.DeletedFolderCount++
	}
	legacyJob.Status = domain.FolderDeletionQueued
	legacyJob.NextRunAt = &now
	legacyJob.LeaseOwner = nil
	legacyJob.LeaseExpiresAt = nil
	if err := tx.Save(&legacyJob).Error; err != nil {
		return nil, false, err
	}
	if _, err := createFolderDeletionLifecycleJob(tx, legacyJob, unitID, now); err != nil {
		return nil, false, err
	}
	return &legacyJob, visibilityChanged, nil
}

func (r *folderDeletionRepository) ProcessFolderDeletionJob(ctx context.Context, jobID, workerID string) error {
	for {
		done, visibilityChanged, err := r.processFolderDeletionBatch(ctx, jobID, workerID)
		if err != nil {
			return err
		}
		if visibilityChanged {
			// A single root event invalidates derived Access decisions for the whole
			// subtree without placing a potentially huge descendant list on Redis.
			var job domain.FolderDeletionJob
			if err := r.db.WithContext(ctx).Where("id = ?", jobID).First(&job).Error; err != nil {
				return err
			}
			eventing.PublishFolderDeleted(ctx, job.OrgID, job.RootFolderID, job.RootPath, jobID)
		}
		if done {
			return nil
		}
	}
}

// ensureFolderDeletionLifecycleUnit gives a recursive deletion one durable
// root. It also repairs a V5 job from before the visibility gate or lifecycle
// worker existed: the root may already be tombstoned, and the transaction can
// safely create its DELETING unit before bounded processing continues.
func ensureFolderDeletionLifecycleUnit(tx *gorm.DB, job domain.FolderDeletionJob) (string, error) {
	var existing domain.LifecycleUnit
	err := tx.
		Where("org_id = ? AND root_resource_type = ? AND root_resource_id = ? AND state = ?", job.OrgID, domain.LifecycleResourceFolder, job.RootFolderID, domain.LifecycleDeleting).
		Order("created_at DESC").
		First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		lifecycleUnitID, createErr := createDeletingLifecycleUnit(
			tx,
			job.OrgID,
			job.RequestedBy,
			job.RootFolderID,
			job.RootPath,
			lifecycleParentPath(job.RootPath),
		)
		if createErr != nil {
			return "", createErr
		}
		existing.ID = lifecycleUnitID
	}
	result := tx.Unscoped().Model(&domain.Folder{}).
		Where("id = ? AND org_id = ? AND deleted_at IS NOT NULL", job.RootFolderID, job.OrgID).
		Update("lifecycle_unit_id", existing.ID)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected != 1 {
		return "", fmt.Errorf("lifecycle deletion root %s is missing or active", job.RootFolderID)
	}
	return existing.ID, nil
}

// closeFolderDeletionVisibilityGate tombstones only the root. The visibility
// predicate hides every descendant through its deleted ancestor while later
// worker batches tombstone the physical member rows.
func closeFolderDeletionVisibilityGate(tx *gorm.DB, job domain.FolderDeletionJob, now time.Time) (string, bool, error) {
	rootResult := tx.Exec(`
		UPDATE folders
		SET deleted_at = ?, updated_by = ?
		WHERE id = ? AND org_id = ? AND deleted_at IS NULL
	`, now, job.RequestedBy, job.RootFolderID, job.OrgID)
	if rootResult.Error != nil {
		return "", false, rootResult.Error
	}
	unitID, err := ensureFolderDeletionLifecycleUnit(tx, job)
	if err != nil {
		return "", false, err
	}
	return unitID, rootResult.RowsAffected == 1, nil
}

func createFolderDeletionLifecycleJob(tx *gorm.DB, legacyJob domain.FolderDeletionJob, unitID string, now time.Time) (domain.LifecycleJob, error) {
	checkpoint, err := encodeFolderDeletionCheckpoint(folderDeletionCheckpoint{
		RootVisibilityClosed: true,
		FolderRows:           legacyJob.DeletedFolderCount,
	})
	if err != nil {
		return domain.LifecycleJob{}, err
	}
	legacyJobID := legacyJob.ID
	job := domain.LifecycleJob{
		ID:                        uuid.NewString(),
		OrgID:                     legacyJob.OrgID,
		UnitID:                    &unitID,
		LegacyFolderDeletionJobID: &legacyJobID,
		RootResourceType:          domain.LifecycleResourceFolder,
		RootResourceID:            legacyJob.RootFolderID,
		RootFolderID:              legacyJob.RootFolderID,
		RootFolderPath:            legacyJob.RootPath,
		RequestedBy:               &legacyJob.RequestedBy,
		Operation:                 domain.LifecycleJobDelete,
		Status:                    domain.LifecycleJobQueued,
		Checkpoint:                checkpoint,
		Attempts:                  legacyJob.Attempts,
		NextRunAt:                 &now,
		QueuedAt:                  &now,
	}
	if err := tx.Create(&job).Error; err != nil {
		return domain.LifecycleJob{}, err
	}
	return job, nil
}

func findLifecycleJobForFolderDeletion(tx *gorm.DB, legacyJobID string, lock bool) (domain.LifecycleJob, bool, error) {
	var lifecycleJob domain.LifecycleJob
	query := tx.Where("legacy_folder_deletion_job_id = ?", legacyJobID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.First(&lifecycleJob).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domain.LifecycleJob{}, false, nil
		}
		return domain.LifecycleJob{}, false, err
	}
	return lifecycleJob, true, nil
}

func (r *folderDeletionRepository) processFolderDeletionBatch(ctx context.Context, jobID, workerID string) (bool, bool, error) {
	var done bool
	var visibilityChanged bool
	var job domain.FolderDeletionJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lifecycleJob, foundLifecycleJob, err := findLifecycleJobForFolderDeletion(tx, jobID, true)
		if err != nil {
			return err
		}
		if !foundLifecycleJob || lifecycleJob.Status != domain.LifecycleJobRunning || lifecycleJob.LeaseOwner == nil || *lifecycleJob.LeaseOwner != workerID {
			return fmt.Errorf("lifecycle deletion job claim was lost")
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_owner = ?", jobID, domain.FolderDeletionRunning, workerID).
			First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("folder deletion compatibility job claim was lost")
			}
			return err
		}
		if lifecycleJob.UnitID == nil {
			return fmt.Errorf("lifecycle deletion job %s has no unit", lifecycleJob.ID)
		}

		unitID, rootVisibilityChanged, err := closeFolderDeletionVisibilityGate(tx, job, time.Now().UTC())
		if err != nil {
			return err
		}
		if unitID != *lifecycleJob.UnitID {
			return fmt.Errorf("lifecycle deletion job %s is linked to the wrong unit", lifecycleJob.ID)
		}
		visibilityChanged = rootVisibilityChanged
		checkpoint, err := decodeFolderDeletionCheckpoint(lifecycleJob.Checkpoint)
		if err != nil {
			return err
		}
		checkpoint.RootVisibilityClosed = true
		if rootVisibilityChanged {
			job.DeletedFolderCount++
			checkpoint.FolderRows++
		}

		metadataResult := tx.Exec(`
			UPDATE metadata_items
			SET deleted_at = ?, updated_by = ?, lifecycle_unit_id = ?
			WHERE id IN (
				SELECT metadata_items.id
				FROM metadata_items
				JOIN folders ON folders.id = metadata_items.folder_id
				WHERE folders.org_id = ?
				  AND folders.path <@ ?::ltree
				  AND metadata_items.deleted_at IS NULL
				ORDER BY metadata_items.id
				LIMIT ?
				FOR UPDATE OF metadata_items SKIP LOCKED
			)
		`, time.Now().UTC(), job.RequestedBy, *lifecycleJob.UnitID, job.OrgID, job.RootPath, domain.FolderDeletionMetadataBatchSize)
		if metadataResult.Error != nil {
			return metadataResult.Error
		}
		now := time.Now().UTC()
		if metadataResult.RowsAffected > 0 {
			job.DeletedMetadataCount += metadataResult.RowsAffected
			checkpoint.MetadataBatches++
			checkpoint.MetadataRows += metadataResult.RowsAffected
			return saveRunningFolderDeletionProgress(tx, &lifecycleJob, &job, checkpoint, now)
		}

		folderResult := tx.Exec(`
			UPDATE folders
			SET deleted_at = ?, updated_by = ?, lifecycle_unit_id = ?
			WHERE id IN (
				SELECT id
				FROM folders
				WHERE org_id = ?
				  AND path <@ ?::ltree
				  AND deleted_at IS NULL
				ORDER BY nlevel(path) DESC, path DESC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
		`, time.Now().UTC(), job.RequestedBy, *lifecycleJob.UnitID, job.OrgID, job.RootPath, domain.FolderDeletionFolderBatchSize)
		if folderResult.Error != nil {
			return folderResult.Error
		}
		if folderResult.RowsAffected > 0 {
			job.DeletedFolderCount += folderResult.RowsAffected
			checkpoint.FolderBatches++
			checkpoint.FolderRows += folderResult.RowsAffected
			return saveRunningFolderDeletionProgress(tx, &lifecycleJob, &job, checkpoint, now)
		}

		now = time.Now().UTC()
		if err := completeDeletingLifecycleUnit(tx, job.OrgID, job.RootFolderID, now); err != nil {
			return err
		}
		job.Status = domain.FolderDeletionSucceeded
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		job.CompletedAt = &now
		checkpointValue, err := encodeFolderDeletionCheckpoint(checkpoint)
		if err != nil {
			return err
		}
		lifecycleJob.Status = domain.LifecycleJobSucceeded
		lifecycleJob.Checkpoint = checkpointValue
		lifecycleJob.LeaseOwner = nil
		lifecycleJob.LeaseExpiresAt = nil
		lifecycleJob.CompletedAt = &now
		if err := tx.Save(&lifecycleJob).Error; err != nil {
			return err
		}
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, visibilityChanged, err
}

func saveRunningFolderDeletionProgress(
	tx *gorm.DB,
	lifecycleJob *domain.LifecycleJob,
	legacyJob *domain.FolderDeletionJob,
	checkpoint folderDeletionCheckpoint,
	now time.Time,
) error {
	checkpointValue, err := encodeFolderDeletionCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
	lifecycleJob.Checkpoint = checkpointValue
	lifecycleJob.LeaseExpiresAt = &leaseExpiresAt
	legacyJob.LeaseExpiresAt = &leaseExpiresAt
	if err := tx.Save(lifecycleJob).Error; err != nil {
		return err
	}
	return tx.Save(legacyJob).Error
}

func automaticRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 5 * time.Second
	case 2:
		return 30 * time.Second
	default:
		return 2 * time.Minute
	}
}

func (r *folderDeletionRepository) FailFolderDeletionJob(ctx context.Context, jobID, workerID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lifecycleJob, foundLifecycleJob, err := findLifecycleJobForFolderDeletion(tx, jobID, true)
		if err != nil {
			return err
		}
		if !foundLifecycleJob || lifecycleJob.Status != domain.LifecycleJobRunning || lifecycleJob.LeaseOwner == nil || *lifecycleJob.LeaseOwner != workerID {
			return fmt.Errorf("lifecycle deletion job claim was lost")
		}
		var job domain.FolderDeletionJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_owner = ?", jobID, domain.FolderDeletionRunning, workerID).
			First(&job).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		code := "INTERNAL_ERROR"
		lifecycleJob.FailureCode = &code
		lifecycleJob.LeaseOwner = nil
		lifecycleJob.LeaseExpiresAt = nil
		job.LastErrorCode = &code
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		if job.Attempts >= domain.FolderDeletionMaxAttempts {
			lifecycleJob.Status = domain.LifecycleJobFailed
			lifecycleJob.NextRunAt = nil
			lifecycleJob.CompletedAt = &now
			if err := tx.Save(&lifecycleJob).Error; err != nil {
				return err
			}
			job.Status = domain.FolderDeletionFailed
			job.NextRunAt = nil
			return tx.Save(&job).Error
		}
		nextRunAt := now.Add(automaticRetryDelay(job.Attempts))
		lifecycleJob.Status = domain.LifecycleJobQueued
		lifecycleJob.NextRunAt = &nextRunAt
		if err := tx.Save(&lifecycleJob).Error; err != nil {
			return err
		}
		job.Status = domain.FolderDeletionQueued
		job.NextRunAt = &nextRunAt
		return tx.Save(&job).Error
	})
}
