package repository

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
)

type folderDeletionRepository struct {
	db *gorm.DB
}

func NewFolderDeletionRepository(db *gorm.DB) domain.FolderDeletionRepository {
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
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		confirmed = job
		return nil
	})
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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, true)
		if err != nil {
			return err
		}
		if job.Status != domain.FolderDeletionQueued {
			return domain.ErrDeletionJobNotCancellable
		}
		now := time.Now().UTC()
		job.Status = domain.FolderDeletionCancelled
		job.CancelledAt = &now
		job.NextRunAt = nil
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		cancelled = job
		return nil
	})
	return cancelled, err
}

func (r *folderDeletionRepository) RetryFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	var retried domain.FolderDeletionJob
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}
		job, err := r.getAuthorizedJob(tx, orgID, actorID, jobID, actorIsOrgAdmin, true)
		if err != nil {
			return err
		}
		if job.Status != domain.FolderDeletionFailed {
			return domain.ErrDeletionJobNotCancellable
		}
		now := time.Now().UTC()
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
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job domain.FolderDeletionJob
		now := time.Now().UTC()
		err := tx.Raw(`
			SELECT *
			FROM folder_deletion_jobs
			WHERE (status = ? AND (next_run_at IS NULL OR next_run_at <= ?))
			   OR (status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?)
			ORDER BY queued_at ASC NULLS LAST, created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		`, domain.FolderDeletionQueued, now, domain.FolderDeletionRunning, now).Scan(&job).Error
		if err != nil {
			return err
		}
		if job.ID == "" {
			return nil
		}
		if job.Attempts >= domain.FolderDeletionMaxAttempts {
			job.Status = domain.FolderDeletionFailed
			job.LeaseOwner = nil
			job.LeaseExpiresAt = nil
			code := "INTERNAL_ERROR"
			job.LastErrorCode = &code
			return tx.Save(&job).Error
		}
		leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
		job.Status = domain.FolderDeletionRunning
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
		claimed = &job
		return nil
	})
	return claimed, err
}

func (r *folderDeletionRepository) ProcessFolderDeletionJob(ctx context.Context, jobID, workerID string) error {
	for {
		done, err := r.processFolderDeletionBatch(ctx, jobID, workerID)
		if err != nil || done {
			return err
		}
	}
}

func (r *folderDeletionRepository) processFolderDeletionBatch(ctx context.Context, jobID, workerID string) (bool, error) {
	var done bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job domain.FolderDeletionJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_owner = ?", jobID, domain.FolderDeletionRunning, workerID).
			First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("folder deletion job claim was lost")
			}
			return err
		}

		metadataResult := tx.Exec(`
			DELETE FROM metadata_items
			WHERE id IN (
				SELECT metadata_items.id
				FROM metadata_items
				JOIN folders ON folders.id = metadata_items.folder_id
				WHERE folders.org_id = ? AND folders.path <@ ?::ltree
				ORDER BY metadata_items.id
				LIMIT ?
				FOR UPDATE OF metadata_items SKIP LOCKED
			)
		`, job.OrgID, job.RootPath, domain.FolderDeletionMetadataBatchSize)
		if metadataResult.Error != nil {
			return metadataResult.Error
		}
		now := time.Now().UTC()
		leaseExpiresAt := now.Add(domain.FolderDeletionLeaseDuration)
		job.LeaseExpiresAt = &leaseExpiresAt
		if metadataResult.RowsAffected > 0 {
			job.DeletedMetadataCount += metadataResult.RowsAffected
			return tx.Save(&job).Error
		}

		folderResult := tx.Exec(`
			DELETE FROM folders
			WHERE id IN (
				SELECT id
				FROM folders
				WHERE org_id = ? AND path <@ ?::ltree
				ORDER BY nlevel(path) DESC, path DESC
				LIMIT ?
				FOR UPDATE SKIP LOCKED
			)
		`, job.OrgID, job.RootPath, domain.FolderDeletionFolderBatchSize)
		if folderResult.Error != nil {
			return folderResult.Error
		}
		if folderResult.RowsAffected > 0 {
			job.DeletedFolderCount += folderResult.RowsAffected
			return tx.Save(&job).Error
		}

		job.Status = domain.FolderDeletionSucceeded
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		job.CompletedAt = &now
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		done = true
		return nil
	})
	return done, err
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
		var job domain.FolderDeletionJob
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND status = ? AND lease_owner = ?", jobID, domain.FolderDeletionRunning, workerID).
			First(&job).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		code := "INTERNAL_ERROR"
		job.LastErrorCode = &code
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		if job.Attempts >= domain.FolderDeletionMaxAttempts {
			job.Status = domain.FolderDeletionFailed
			job.NextRunAt = nil
			return tx.Save(&job).Error
		}
		nextRunAt := now.Add(automaticRetryDelay(job.Attempts))
		job.Status = domain.FolderDeletionQueued
		job.NextRunAt = &nextRunAt
		return tx.Save(&job).Error
	})
}
