package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PurgeableMediaObjects is one session's worth of storage that nothing
// references any more: the raw object behind an abandoned or invalid upload,
// and the derivatives of a version that failed.
//
// Objects and rows are separated deliberately. The sweep deletes from storage
// first and records the purge afterwards, so a crash in between leaks nothing
// and only repeats an idempotent delete.
type PurgeableMediaObjects struct {
	UploadID            string
	VersionID           string
	OrgID               string
	RawObjectKey        string
	ProcessedObjectKeys []string
	StoredBytes         int64
}

type expirableUploadSession struct {
	UploadID          string `gorm:"column:upload_id"`
	OrgID             string `gorm:"column:org_id"`
	ExpectedSizeBytes int64  `gorm:"column:expected_size_bytes"`
}

// ExpireUploadSessions transitions at most limit abandoned sessions and
// releases their quota reservations. Candidate selection is intentionally
// read-only; each transition then follows the global quota-before-session lock
// order used by upload admission and cancellation.
func (store *mediaJobStore) ExpireUploadSessions(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}

	var candidates []expirableUploadSession
	if err := store.db.WithContext(ctx).Raw(`
		SELECT id AS upload_id, org_id, expected_size_bytes
		FROM media_upload_sessions
		WHERE state = ?
		  AND session_expires_at <= statement_timestamp()
		ORDER BY updated_at, session_expires_at, id
		LIMIT ?`,
		domain.UploadSessionCreated, limit,
	).Scan(&candidates).Error; err != nil {
		return 0, fmt.Errorf("listing expired upload sessions: %w", err)
	}

	// Failures are moved to the back of the durable queue by touching updated_at.
	// This lets later sessions progress even when every candidate in one batch is
	// persistently broken, without increasing the bounded amount of work.
	expired := 0
	var failures []error
	for _, candidate := range candidates {
		transitioned, err := store.expireUploadSession(ctx, candidate)
		if err != nil {
			failures = append(failures, fmt.Errorf("expiring upload session %s: %w", candidate.UploadID, err))
			if deferErr := store.deferExpiryCandidate(ctx, candidate); deferErr != nil {
				failures = append(failures, fmt.Errorf(
					"rotating failed upload session %s: %w",
					candidate.UploadID,
					deferErr,
				))
			}
			continue
		}
		if transitioned {
			expired++
		}
	}
	return expired, errors.Join(failures...)
}

// deferExpiryCandidate records a failed attempt using the existing
// trigger-maintained updated_at timestamp. The session remains expired in the
// application sense and eligible for later retry, but no longer monopolizes
// the oldest slots in the next bounded batch.
func (store *mediaJobStore) deferExpiryCandidate(
	ctx context.Context,
	candidate expirableUploadSession,
) error {
	return store.db.WithContext(ctx).Exec(`
		UPDATE media_upload_sessions
		SET updated_at = statement_timestamp()
		WHERE id = ?
		  AND org_id = ?
		  AND state = ?
		  AND session_expires_at <= statement_timestamp()`,
		candidate.UploadID,
		candidate.OrgID,
		domain.UploadSessionCreated,
	).Error
}

func (store *mediaJobStore) expireUploadSession(
	ctx context.Context,
	candidate expirableUploadSession,
) (bool, error) {
	transitioned := false
	err := store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var usage domain.OrganizationMediaUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ?", candidate.OrgID).
			Take(&usage).Error; err != nil {
			return err
		}

		result := tx.Exec(`
			UPDATE media_upload_sessions
			SET state = ?, expired_at = statement_timestamp(), updated_at = statement_timestamp()
			WHERE id = ?
			  AND org_id = ?
			  AND state = ?
			  AND session_expires_at <= statement_timestamp()`,
			domain.UploadSessionExpired,
			candidate.UploadID,
			candidate.OrgID,
			domain.UploadSessionCreated,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := releaseReservation(tx, candidate.OrgID, candidate.ExpectedSizeBytes); err != nil {
			return err
		}
		transitioned = true
		return nil
	})
	return transitioned, err
}

// ClaimPurgeableMediaObjects lists storage eligible for reclamation after the
// quarantine window, newest-last so a bounded batch drains the oldest backlog
// first.
//
// Two kinds qualify. An abandoned session never produced a version, so its raw
// object is referenced by nothing. An invalid session produced a version that
// failed, so its raw object and any partial derivatives are dead weight. A
// completed version qualifies under neither: its raw original is retained for
// the asset's lifetime, and the query never widens far enough to reach it.
func (store *mediaJobStore) ClaimPurgeableMediaObjects(
	ctx context.Context,
	quarantine time.Duration,
	limit int,
) ([]PurgeableMediaObjects, error) {
	if limit <= 0 {
		return nil, nil
	}

	var rows []struct {
		UploadID            string
		VersionID           *string
		OrgID               string
		AssetID             string
		RawObjectKey        string
		DeclaredContentType domain.MediaContentType
		StoredBytes         int64
	}
	err := store.db.WithContext(ctx).Raw(`
		SELECT sessions.id             AS upload_id,
		       versions.id             AS version_id,
		       sessions.org_id         AS org_id,
		       sessions.asset_id       AS asset_id,
		       sessions.raw_object_key AS raw_object_key,
		       versions.declared_content_type AS declared_content_type,
		       COALESCE(versions.original_size_bytes, 0) AS stored_bytes
		FROM media_upload_sessions AS sessions
		LEFT JOIN asset_media_versions AS versions ON versions.upload_id = sessions.id
		WHERE sessions.raw_object_purged_at IS NULL
		  AND (
		        (
		          versions.id IS NULL
		          AND sessions.state IN ('expired', 'cancelled', 'failed')
		          AND COALESCE(sessions.cancelled_at, sessions.expired_at) <= statement_timestamp() - ?::interval
		        )
		     OR (
		          versions.status = 'failed'
		          AND versions.failed_at <= statement_timestamp() - ?::interval
		        )
		  )
		ORDER BY COALESCE(sessions.cancelled_at, sessions.expired_at, versions.failed_at)
		LIMIT ?`,
		quarantine.String(), quarantine.String(), limit,
	).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("listing purgeable media objects: %w", err)
	}

	batches := make([]PurgeableMediaObjects, 0, len(rows))
	for _, row := range rows {
		batch := PurgeableMediaObjects{
			UploadID:     row.UploadID,
			OrgID:        row.OrgID,
			RawObjectKey: row.RawObjectKey,
		}
		if row.VersionID != nil {
			batch.VersionID = *row.VersionID
			// Only a committed upload ever converted its reservation into stored
			// bytes, which is exactly the case where a version exists.
			batch.StoredBytes = row.StoredBytes

			for _, policy := range domain.MediaOutputManifest {
				key, err := domain.ProcessedObjectKey(
					row.OrgID,
					row.AssetID,
					batch.VersionID,
					policy.Kind,
					row.DeclaredContentType,
				)
				if err != nil {
					return nil, fmt.Errorf("deriving %s key for failed version %s: %w", policy.Kind, batch.VersionID, err)
				}
				batch.ProcessedObjectKeys = append(batch.ProcessedObjectKeys, key.String())
			}
		}
		batches = append(batches, batch)
	}
	return batches, nil
}

// MarkMediaObjectsPurged records that storage has already been reclaimed. It
// runs after the deletes, so a marker never claims more than actually happened.
func (store *mediaJobStore) MarkMediaObjectsPurged(ctx context.Context, purged PurgeableMediaObjects) error {
	return store.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Every path that touches both quota and a session follows this order.
		// Holding the quota row while claiming the marker makes the claim the
		// exactly-once gate for the later quota release across worker replicas.
		if purged.StoredBytes > 0 {
			var usage domain.OrganizationMediaUsage
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("org_id = ?", purged.OrgID).
				Take(&usage).Error; err != nil {
				return err
			}
		}

		result := tx.Exec(
			`UPDATE media_upload_sessions
			 SET raw_object_purged_at = statement_timestamp()
			 WHERE id = ? AND org_id = ? AND raw_object_purged_at IS NULL`,
			purged.UploadID,
			purged.OrgID,
		)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		if purged.VersionID != "" {
			if err := tx.Exec(
				"DELETE FROM media_outputs WHERE version_id = ?", purged.VersionID,
			).Error; err != nil {
				return err
			}
			if err := tx.Exec(
				"UPDATE asset_media_versions SET raw_retained = false WHERE id = ?", purged.VersionID,
			).Error; err != nil {
				return err
			}
			if purged.StoredBytes > 0 {
				if err := releaseStoredBytes(tx, purged.OrgID, purged.StoredBytes); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
