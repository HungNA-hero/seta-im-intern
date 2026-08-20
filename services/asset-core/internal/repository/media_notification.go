package repository

import (
	"context"
	"errors"
	"fmt"

	"seta-im-intern/go-asset-core/internal/eventing/media"
)

var ErrNotificationMismatch = errors.New("media notification does not match current database truth")

type mediaNotificationTruth struct {
	OrgID     string
	AssetID   string
	VersionID string
	UploadID  string
}

func (store *mediaJobStore) VerifyNotification(ctx context.Context, orgID string, payload media.Payload) error {
	var truth mediaNotificationTruth
	result := store.db.WithContext(ctx).Raw(`
		SELECT jobs.org_id::text AS org_id,
		       jobs.asset_id::text AS asset_id,
		       jobs.version_id::text AS version_id,
		       versions.upload_id::text AS upload_id
		FROM media_processing_jobs AS jobs
		JOIN asset_media_versions AS versions ON versions.id = jobs.version_id
		WHERE jobs.id = ?`, payload.JobID).Scan(&truth)
	if result.Error != nil {
		return fmt.Errorf("loading truth for media notification job %s: %w", payload.JobID, result.Error)
	}
	if result.RowsAffected != 1 || truth.OrgID == "" {
		return ErrJobNotFound
	}
	if truth.OrgID != orgID || truth.AssetID != payload.AssetID ||
		truth.VersionID != payload.VersionID || truth.UploadID != payload.UploadID {
		return ErrNotificationMismatch
	}
	return nil
}
