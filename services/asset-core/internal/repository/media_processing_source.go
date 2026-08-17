package repository

import (
	"context"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"
)

var ErrVersionNotFound = errors.New("media version not found")

type MediaProcessingSource struct {
	OrgID               string
	AssetID             string
	VersionID           string
	UploadID            string
	RawObjectKey        domain.ObjectKey
	DeclaredContentType domain.MediaContentType
	AdmittedSHA256      []byte
	OriginalSizeBytes   int64
}

func (store *mediaJobStore) LoadProcessingSource(ctx context.Context, versionID string) (MediaProcessingSource, error) {
	var row struct {
		OrgID               string
		AssetID             string
		VersionID           string
		UploadID            string
		RawObjectKey        string
		DeclaredContentType string
		AdmittedSha256      []byte
		OriginalSizeBytes   int64
	}
	result := store.db.WithContext(ctx).Raw(`
		SELECT versions.org_id,
		       versions.asset_id,
		       versions.id AS version_id,
		       versions.upload_id,
		       versions.raw_object_key,
		       versions.declared_content_type,
		       sessions.declared_checksum_sha256 AS admitted_sha256,
		       versions.original_size_bytes
		FROM asset_media_versions AS versions
		JOIN media_upload_sessions AS sessions ON sessions.id = versions.upload_id
		WHERE versions.id = ?`, versionID).Scan(&row)
	if result.Error != nil {
		return MediaProcessingSource{}, fmt.Errorf("loading the processing source for version %s: %w", versionID, result.Error)
	}
	if result.RowsAffected == 0 {
		return MediaProcessingSource{}, fmt.Errorf("%w: %s", ErrVersionNotFound, versionID)
	}

	return MediaProcessingSource{
		OrgID:               row.OrgID,
		AssetID:             row.AssetID,
		VersionID:           row.VersionID,
		UploadID:            row.UploadID,
		RawObjectKey:        domain.ObjectKey(row.RawObjectKey),
		DeclaredContentType: domain.MediaContentType(row.DeclaredContentType),
		AdmittedSHA256:      row.AdmittedSha256,
		OriginalSizeBytes:   row.OriginalSizeBytes,
	}, nil
}
