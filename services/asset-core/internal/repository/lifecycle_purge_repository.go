package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
)

// NewLifecyclePurgeRepository exposes the database side of physical lifecycle
// purge. Storage deletes deliberately remain in the usecase: PostgreSQL cannot
// atomically commit a MinIO delete.
func NewLifecyclePurgeRepository(db *gorm.DB) domain.LifecyclePurgeRepository {
	return &folderDeletionRepository{db: db}
}

func (r *folderDeletionRepository) NextLifecyclePurgeAsset(ctx context.Context, jobID, workerID string) (*domain.LifecyclePurgeAsset, error) {
	var work *domain.LifecyclePurgeAsset
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, unit, err := lockRunningPurgeJob(tx, jobID, workerID)
		if err != nil {
			return err
		}

		assetID, err := nextPurgeAssetID(tx, job, unit)
		if err != nil || assetID == "" {
			return err
		}
		if err := createPurgeObjectManifest(tx, job.ID, job.OrgID, assetID); err != nil {
			return err
		}

		var keys []string
		if err := tx.Raw(`
			SELECT object_key
			FROM asset_lifecycle_purge_objects
			WHERE lifecycle_job_id = ? AND asset_id = ? AND deleted_at IS NULL
			ORDER BY object_key ASC`, job.ID, assetID).Scan(&keys).Error; err != nil {
			return fmt.Errorf("list purge objects for asset %s: %w", assetID, err)
		}
		work = &domain.LifecyclePurgeAsset{
			JobID: job.ID, OrgID: job.OrgID, UnitID: unit.ID, AssetID: assetID, ObjectKeys: keys,
		}
		return nil
	})
	return work, err
}

func lockRunningPurgeJob(tx *gorm.DB, jobID, workerID string) (domain.LifecycleJob, domain.LifecycleUnit, error) {
	var job domain.LifecycleJob
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND operation = ? AND status = ? AND lease_owner = ? AND lease_expires_at > statement_timestamp()", jobID, domain.LifecycleJobPurge, domain.LifecycleJobRunning, workerID).
		First(&job).Error; err != nil {
		return domain.LifecycleJob{}, domain.LifecycleUnit{}, fmt.Errorf("lock claimed purge job: %w", err)
	}
	if job.UnitID == nil {
		return domain.LifecycleJob{}, domain.LifecycleUnit{}, fmt.Errorf("purge job %s has no lifecycle unit", job.ID)
	}
	if err := lockOrganizationDeletion(tx, job.OrgID); err != nil {
		return domain.LifecycleJob{}, domain.LifecycleUnit{}, err
	}
	var unit domain.LifecycleUnit
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND org_id = ? AND state = ?", *job.UnitID, job.OrgID, domain.LifecyclePurging).
		First(&unit).Error; err != nil {
		return domain.LifecycleJob{}, domain.LifecycleUnit{}, fmt.Errorf("lock purging lifecycle unit: %w", err)
	}
	return job, unit, nil
}

func nextPurgeAssetID(tx *gorm.DB, job domain.LifecycleJob, unit domain.LifecycleUnit) (string, error) {
	var assetID string
	query := `
		SELECT metadata.id
		FROM metadata_items AS metadata
		JOIN folders AS folder ON folder.id = metadata.folder_id
		WHERE folder.org_id = ?
		  AND metadata.lifecycle_unit_id = ?
		  AND metadata.deleted_at IS NOT NULL`
	args := []any{job.OrgID, unit.ID}
	if unit.RootResourceType == domain.LifecycleResourceMetadata {
		query += " AND metadata.id = ?"
		args = append(args, unit.RootResourceID)
	} else {
		query += " AND folder.path <@ ?::ltree"
		args = append(args, unit.RootFolderPath)
	}
	query += " ORDER BY metadata.id ASC FOR UPDATE OF metadata SKIP LOCKED LIMIT 1"
	result := tx.Raw(query, args...).Scan(&assetID)
	if result.Error != nil {
		return "", fmt.Errorf("select next purge asset: %w", result.Error)
	}
	return assetID, nil
}

func createPurgeObjectManifest(tx *gorm.DB, jobID, orgID, assetID string) error {
	// Every raw key and output key is recorded before any external delete. The
	// unique constraint makes a crash/retry converge on one manifest per job.
	if err := tx.Exec(`
		INSERT INTO asset_lifecycle_purge_objects (lifecycle_job_id, org_id, asset_id, object_key)
		SELECT ?, ?, versions.asset_id, versions.raw_object_key
		FROM asset_media_versions AS versions
		WHERE versions.asset_id = ?
		ON CONFLICT (lifecycle_job_id, object_key) DO NOTHING`, jobID, orgID, assetID).Error; err != nil {
		return fmt.Errorf("manifest raw objects for asset %s: %w", assetID, err)
	}
	if err := tx.Exec(`
		INSERT INTO asset_lifecycle_purge_objects (lifecycle_job_id, org_id, asset_id, object_key)
		SELECT ?, ?, versions.asset_id, output.object_key
		FROM asset_media_versions AS versions
		JOIN media_outputs AS output ON output.version_id = versions.id
		WHERE versions.asset_id = ?
		ON CONFLICT (lifecycle_job_id, object_key) DO NOTHING`, jobID, orgID, assetID).Error; err != nil {
		return fmt.Errorf("manifest derivative objects for asset %s: %w", assetID, err)
	}
	if err := tx.Exec(`
		INSERT INTO asset_lifecycle_purge_objects (lifecycle_job_id, org_id, asset_id, object_key)
		SELECT ?, ?, sessions.asset_id, sessions.raw_object_key
		FROM media_upload_sessions AS sessions
		WHERE sessions.org_id = ?
		  AND sessions.asset_id = ?
		  AND sessions.raw_object_key <> ''
		ON CONFLICT (lifecycle_job_id, object_key) DO NOTHING`, jobID, orgID, orgID, assetID).Error; err != nil {
		return fmt.Errorf("manifest upload-session raw objects for asset %s: %w", assetID, err)
	}
	return nil
}

func (r *folderDeletionRepository) MarkLifecyclePurgeObjectsDeleted(ctx context.Context, jobID, workerID, assetID string, objectKeys []string) error {
	if len(objectKeys) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, _, err := lockRunningPurgeJob(tx, jobID, workerID); err != nil {
			return err
		}
		result := tx.Exec(`
			UPDATE asset_lifecycle_purge_objects
			SET deleted_at = statement_timestamp()
			WHERE lifecycle_job_id = ? AND asset_id = ? AND object_key IN ? AND deleted_at IS NULL`,
			jobID, assetID, objectKeys,
		)
		if result.Error != nil {
			return fmt.Errorf("mark deleted purge objects for asset %s: %w", assetID, result.Error)
		}
		return nil
	})
}

func (r *folderDeletionRepository) FinalizeLifecyclePurgeAsset(ctx context.Context, jobID, workerID, assetID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, unit, err := lockRunningPurgeJob(tx, jobID, workerID)
		if err != nil {
			return err
		}
		var pending int64
		if err := tx.Raw(`SELECT count(*) FROM asset_lifecycle_purge_objects
			WHERE lifecycle_job_id = ? AND asset_id = ? AND deleted_at IS NULL`, job.ID, assetID).Scan(&pending).Error; err != nil {
			return fmt.Errorf("count pending purge objects for asset %s: %w", assetID, err)
		}
		if pending != 0 {
			return fmt.Errorf("asset %s still has %d purge objects", assetID, pending)
		}

		var usage domain.OrganizationMediaUsage
		usageErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ?", job.OrgID).
			Take(&usage).Error
		if usageErr != nil && !errors.Is(usageErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("lock media quota for asset %s: %w", assetID, usageErr)
		}

		var storedBytes int64
		if err := tx.Raw(`SELECT COALESCE(sum(original_size_bytes) FILTER (WHERE raw_retained), 0)
			FROM asset_media_versions WHERE asset_id = ?`, assetID).Scan(&storedBytes).Error; err != nil {
			return fmt.Errorf("sum stored media bytes for asset %s: %w", assetID, err)
		}
		var reservedBytes int64
		if err := tx.Raw(`SELECT COALESCE(sum(expected_size_bytes), 0)
			FROM media_upload_sessions
			WHERE org_id = ? AND asset_id = ? AND state = ?`, job.OrgID, assetID, domain.UploadSessionCreated).Scan(&reservedBytes).Error; err != nil {
			return fmt.Errorf("sum reserved media bytes for asset %s: %w", assetID, err)
		}
		if (storedBytes > 0 || reservedBytes > 0) && errors.Is(usageErr, gorm.ErrRecordNotFound) {
			return fmt.Errorf("media quota ledger is missing for asset %s", assetID)
		}

		// This order is deliberately the same as media_teardown_order_test. Each
		// statement removes the foreign-key dependent that blocks the next one.
		statements := []struct {
			sql  string
			args []any
		}{
			{`UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL WHERE id = ?`, []any{assetID}},
			{`DELETE FROM media_job_outbox WHERE job_id IN (SELECT id FROM media_processing_jobs WHERE asset_id = ?)`, []any{assetID}},
			{`DELETE FROM media_processing_jobs WHERE asset_id = ?`, []any{assetID}},
			{`DELETE FROM media_outputs WHERE version_id IN (SELECT id FROM asset_media_versions WHERE asset_id = ?)`, []any{assetID}},
			{`DELETE FROM asset_media_versions WHERE asset_id = ?`, []any{assetID}},
			{`DELETE FROM media_upload_sessions WHERE asset_id = ?`, []any{assetID}},
			{`DELETE FROM metadata_items WHERE id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL`, []any{assetID, unit.ID}},
		}
		for _, statement := range statements {
			if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
				return fmt.Errorf("teardown purged asset %s: %w", assetID, err)
			}
		}
		if storedBytes > 0 || reservedBytes > 0 {
			if err := tx.Exec(`UPDATE organization_media_usage
				SET stored_raw_bytes = GREATEST(stored_raw_bytes - ?, 0),
				    reserved_raw_bytes = GREATEST(reserved_raw_bytes - ?, 0)
				WHERE org_id = ?`, storedBytes, reservedBytes, job.OrgID).Error; err != nil {
				return fmt.Errorf("release media quota for asset %s: %w", assetID, err)
			}
		}
		return nil
	})
}

func (r *folderDeletionRepository) FinalizeLifecyclePurgeJob(ctx context.Context, jobID, workerID string) (bool, error) {
	completed := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		job, unit, err := lockRunningPurgeJob(tx, jobID, workerID)
		if err != nil {
			return err
		}

		// A folder hierarchy has no parent foreign key, but leaf-first removal
		// retains the same structural invariant and makes retries bounded.
		var folderID string
		result := tx.Raw(`
			SELECT folder.id
			FROM folders AS folder
			WHERE folder.org_id = ?
			  AND folder.lifecycle_unit_id = ?
			  AND folder.deleted_at IS NOT NULL
			  AND folder.path <@ ?::ltree
			  AND NOT EXISTS (SELECT 1 FROM metadata_items WHERE folder_id = folder.id)
			  AND NOT EXISTS (
				SELECT 1 FROM folders AS child
				WHERE child.org_id = folder.org_id
				  AND child.lifecycle_unit_id = folder.lifecycle_unit_id
				  AND child.id <> folder.id
				  AND child.path <@ folder.path
			  )
			ORDER BY nlevel(folder.path) DESC, folder.path DESC
			FOR UPDATE OF folder SKIP LOCKED
			LIMIT 1`, job.OrgID, unit.ID, unit.RootFolderPath).Scan(&folderID)
		if result.Error != nil {
			return fmt.Errorf("select purgeable folder leaf: %w", result.Error)
		}
		if folderID != "" {
			if err := tx.Exec(`DELETE FROM folders WHERE id = ? AND org_id = ? AND lifecycle_unit_id = ? AND deleted_at IS NOT NULL`, folderID, job.OrgID, unit.ID).Error; err != nil {
				return fmt.Errorf("delete purged folder leaf %s: %w", folderID, err)
			}
			return nil
		}

		var remaining int64
		if err := tx.Raw(`
			SELECT count(*)
			FROM metadata_items AS metadata
			JOIN folders AS folder ON folder.id = metadata.folder_id
			WHERE folder.org_id = ?
			  AND metadata.lifecycle_unit_id = ?
			  AND metadata.deleted_at IS NOT NULL
			  AND folder.path <@ ?::ltree`, job.OrgID, unit.ID, unit.RootFolderPath).Scan(&remaining).Error; err != nil {
			return fmt.Errorf("count remaining purge metadata: %w", err)
		}
		if remaining != 0 {
			return fmt.Errorf("purge job %s has %d metadata rows without a selectable root", job.ID, remaining)
		}
		now := time.Now().UTC()
		job.Status = domain.LifecycleJobSucceeded
		job.LeaseOwner = nil
		job.LeaseExpiresAt = nil
		job.CompletedAt = &now
		if err := tx.Save(&job).Error; err != nil {
			return err
		}
		if err := tx.Model(&unit).Where("state = ?", domain.LifecyclePurging).Update("state", domain.LifecyclePurged).Error; err != nil {
			return err
		}
		completed = true
		return nil
	})
	return completed, err
}
