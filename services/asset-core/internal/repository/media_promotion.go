package repository

import (
	"context"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"

	"gorm.io/gorm"
)

var (
	ErrIncompleteOutputSet = errors.New("a media version needs exactly one thumbnail and one web output")
	ErrVersionNotPending   = errors.New("media version is not the asset's pending candidate")
)

type MediaCompletion struct {
	JobID               string
	VersionID           string
	OrgID               string
	AssetID             string
	DetectedContentType domain.MediaContentType
	SourceWidth         int
	SourceHeight        int
	SourceSHA256        []byte
	Outputs             []domain.MediaOutput
}

type MediaFailure struct {
	JobID     string
	VersionID string
	OrgID     string
	AssetID   string
	ErrorCode string
}

func (store *mediaJobStore) CompleteAndPromote(ctx context.Context, completion MediaCompletion, held domain.JobLease) (bool, error) {
	if err := requireCompleteOutputSet(completion.Outputs); err != nil {
		return false, err
	}

	applied := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		leased, err := lockLeasedJob(tx, completion.JobID, held)
		if err != nil || !leased {
			return err
		}

		for _, output := range completion.Outputs {
			if err := insertOutput(tx, output); err != nil {
				return err
			}
		}
		if err := requireTwoStoredOutputs(tx, completion.VersionID); err != nil {
			return err
		}

		if err := tx.Exec(`
			UPDATE asset_media_versions
			SET status = 'completed',
			    detected_content_type = ?,
			    source_width = ?,
			    source_height = ?,
			    sha256 = ?,
			    completed_at = statement_timestamp(),
			    activated_at = statement_timestamp()
			WHERE id = ? AND org_id = ? AND status IN ('pending', 'processing')`,
			completion.DetectedContentType, completion.SourceWidth, completion.SourceHeight,
			completion.SourceSHA256, completion.VersionID, completion.OrgID,
		).Error; err != nil {
			return err
		}

		if err := promoteActiveVersion(tx, completion); err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE media_processing_jobs
			SET status = 'completed', stage = NULL, completed_at = statement_timestamp(),
			    lease_owner = NULL, lease_expires_at = NULL
			WHERE id = ?`, completion.JobID).Error; err != nil {
			return err
		}

		applied = true
		return nil
	})
	return applied, err
}

func (store *mediaJobStore) FailVersion(ctx context.Context, failure MediaFailure, held domain.JobLease) (bool, error) {
	applied := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		leased, err := lockLeasedJob(tx, failure.JobID, held)
		if err != nil || !leased {
			return err
		}

		if err := failMediaVersion(tx, failure); err != nil {
			return err
		}

		applied = true
		return nil
	})
	return applied, err
}

func failMediaVersion(tx *gorm.DB, failure MediaFailure) error {
	if err := tx.Exec(`
		UPDATE asset_media_versions
		SET status = 'failed', failure_code = ?, failed_at = statement_timestamp()
		WHERE id = ? AND org_id = ?`,
		failure.ErrorCode, failure.VersionID, failure.OrgID,
	).Error; err != nil {
		return err
	}

	if err := clearPendingMediaVersion(tx, failure.AssetID, failure.VersionID); err != nil {
		return err
	}
	return tx.Exec(`
		UPDATE media_processing_jobs
		SET status = 'failed', stage = NULL, failed_at = statement_timestamp(), last_error_code = ?,
		    lease_owner = NULL, lease_expires_at = NULL
		WHERE id = ?`, failure.ErrorCode, failure.JobID).Error
}

func lockLeasedJob(tx *gorm.DB, jobID string, held domain.JobLease) (bool, error) {
	var leasedID string
	result := tx.Raw(`
		SELECT id FROM media_processing_jobs
		WHERE id = ?
		  AND status = 'processing'
		  AND lease_owner = ?
		  AND lease_expires_at = ?
		  AND lease_expires_at > statement_timestamp()
		FOR UPDATE`,
		jobID, held.Owner, held.ExpiresAt.UTC(),
	).Scan(&leasedID)
	if result.Error != nil {
		return false, fmt.Errorf("locking media job %s under its lease: %w", jobID, result.Error)
	}
	return result.RowsAffected == 1, nil
}

func promoteActiveVersion(tx *gorm.DB, completion MediaCompletion) error {
	result := tx.Exec(`
		UPDATE metadata_items
		SET active_media_version_id = ?, pending_media_version_id = NULL
		WHERE id = ? AND pending_media_version_id = ?`,
		completion.VersionID, completion.AssetID, completion.VersionID,
	)
	if result.Error != nil {
		return fmt.Errorf("promoting media version %s: %w", completion.VersionID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: %s", ErrVersionNotPending, completion.VersionID)
	}
	return nil
}

func insertOutput(tx *gorm.DB, output domain.MediaOutput) error {
	return tx.Exec(`
		INSERT INTO media_outputs (version_id, kind, object_key, content_type, width, height, size_bytes, sha256)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (version_id, kind) DO NOTHING`,
		output.VersionID, output.Kind, output.ObjectKey, output.ContentType,
		output.Width, output.Height, output.SizeBytes, output.SHA256,
	).Error
}

func requireTwoStoredOutputs(tx *gorm.DB, versionID string) error {
	var stored int64
	if err := tx.Raw(
		"SELECT count(DISTINCT kind) FROM media_outputs WHERE version_id = ?", versionID,
	).Scan(&stored).Error; err != nil {
		return err
	}
	if stored != int64(len(domain.MediaOutputManifest)) {
		return fmt.Errorf("%w: version %s has %d", ErrIncompleteOutputSet, versionID, stored)
	}
	return nil
}

func requireCompleteOutputSet(outputs []domain.MediaOutput) error {
	seen := make(map[domain.MediaOutputKind]bool, len(outputs))
	for _, output := range outputs {
		if seen[output.Kind] {
			return fmt.Errorf("%w: %s produced twice", ErrIncompleteOutputSet, output.Kind)
		}
		seen[output.Kind] = true
	}

	for _, policy := range domain.MediaOutputManifest {
		if !seen[policy.Kind] {
			return fmt.Errorf("%w: %s missing", ErrIncompleteOutputSet, policy.Kind)
		}
	}
	return nil
}
