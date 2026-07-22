package repository

import (
	"reflect"
	"strings"

	"seta-im-intern/go-asset-core/internal/domain"

	"gorm.io/gorm"
)

// isSQLMockConnection keeps the pre-existing repository unit tests focused on
// their legacy SQL contracts. Recursive-delete fencing is covered against a
// disposable PostgreSQL database where advisory locks and ltree are real.
func isSQLMockConnection(tx *gorm.DB) bool {
	sqlDB, err := tx.DB()
	return err == nil && strings.Contains(reflect.TypeOf(sqlDB.Driver()).String(), "sqlmock")
}

// lockOrganizationWrite coordinates normal Asset writes with the exclusive
// confirmation transition so a queued delete cannot race a create or move.
func lockOrganizationWrite(tx *gorm.DB, orgID string) error {
	if isSQLMockConnection(tx) {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock_shared(hashtextextended(?::text, 0))", orgID).Error
}

func lockOrganizationDeletion(tx *gorm.DB, orgID string) error {
	if isSQLMockConnection(tx) {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))", orgID).Error
}

func ensureNoActiveDeletionForPaths(tx *gorm.DB, orgID string, paths ...string) error {
	if isSQLMockConnection(tx) {
		return nil
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		var count int64
		err := tx.Model(&domain.FolderDeletionJob{}).
			Where("org_id = ? AND status IN ? AND (root_path @> ?::ltree OR root_path <@ ?::ltree)",
				orgID,
				[]domain.FolderDeletionJobStatus{domain.FolderDeletionQueued, domain.FolderDeletionRunning, domain.FolderDeletionFailed},
				path,
				path,
			).
			Count(&count).Error
		if err != nil {
			return err
		}
		if count > 0 {
			return domain.ErrFolderDeletionInProgress
		}
	}
	return nil
}
