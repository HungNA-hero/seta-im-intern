package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
)

var ErrMediaStatusNotFound = errors.New("media processing status not found in scope")

type MediaStatusScope struct {
	OrgID   string
	AssetID string
}

type MediaStatusRecord struct {
	Job     domain.MediaProcessingJob
	Version domain.AssetMediaVersion
	Session domain.MediaUploadSession
	Outputs []domain.MediaOutput
}

func (repository *mediaRepository) GetLatestMediaStatus(ctx context.Context, scope MediaStatusScope) (MediaStatusRecord, error) {
	var record MediaStatusRecord
	err := repository.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Raw(`
			SELECT jobs.*
			FROM media_processing_jobs AS jobs
			JOIN metadata_items AS assets ON assets.id = jobs.asset_id
			JOIN folders ON folders.id = assets.folder_id
			WHERE jobs.org_id = ?
			  AND jobs.asset_id = ?
			  AND folders.org_id = ?
			  AND assets.deleted_at IS NULL
			  AND folders.deleted_at IS NULL
			ORDER BY jobs.created_at DESC, jobs.id DESC
			LIMIT 1`,
			scope.OrgID, scope.AssetID, scope.OrgID,
		).Scan(&record.Job)
		if result.Error != nil {
			return fmt.Errorf("loading latest media job: %w", result.Error)
		}
		if result.RowsAffected != 1 || record.Job.ID == "" {
			return ErrMediaStatusNotFound
		}

		if err := tx.Where(
			"id = ? AND org_id = ? AND asset_id = ?",
			record.Job.VersionID, scope.OrgID, scope.AssetID,
		).Take(&record.Version).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMediaStatusNotFound
			}
			return fmt.Errorf("loading media status version: %w", err)
		}
		if err := tx.Where(
			"id = ? AND org_id = ? AND asset_id = ?",
			record.Version.UploadID, scope.OrgID, scope.AssetID,
		).Take(&record.Session).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrMediaStatusNotFound
			}
			return fmt.Errorf("loading media status upload session: %w", err)
		}
		if err := tx.Where("version_id = ?", record.Version.ID).
			Order("kind ASC").
			Find(&record.Outputs).Error; err != nil {
			return fmt.Errorf("loading media status outputs: %w", err)
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return MediaStatusRecord{}, err
	}
	return record, nil
}
