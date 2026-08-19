package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"seta-im-intern/go-asset-core/internal/domain"
)

const lifecycleCleanupUnitBatchSize = 100

type lifecycleCleanupRunCheckpoint struct {
	LastOrgID         string `json:"last_org_id,omitempty"`
	LastUnitID        string `json:"last_unit_id,omitempty"`
	QueuedInLastBatch int    `json:"queued_in_last_batch"`
}

// NewLifecycleCleanupWorkerRepository exposes the bounded daily cleanup
// dispatch boundary without giving the scheduler permission to delete Assets.
func NewLifecycleCleanupWorkerRepository(db *gorm.DB) domain.LifecycleCleanupWorkerRepository {
	return &lifecycleCleanupSchedulerRepository{db: db}
}

// ProcessNextLifecycleCleanupBatch claims one cleanup run briefly, then queues
// at most one organization-scoped batch of eligible lifecycle roots. The unit
// state is moved to PURGE_QUEUED in the same transaction as the PURGE job so a
// later tick cannot queue the same root twice.
func (r *lifecycleCleanupSchedulerRepository) ProcessNextLifecycleCleanupBatch(ctx context.Context) (*domain.LifecycleCleanupBatchResult, error) {
	var result *domain.LifecycleCleanupBatchResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run domain.LifecycleCleanupRun
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status IN ?", []domain.LifecycleCleanupRunStatus{domain.LifecycleCleanupRunQueued, domain.LifecycleCleanupRunRunning}).
			Order("run_date ASC, created_at ASC").
			First(&run).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil
			}
			return err
		}

		now := time.Now().UTC()
		if run.Status == domain.LifecycleCleanupRunQueued {
			run.Status = domain.LifecycleCleanupRunRunning
			run.StartedAt = &now
		}

		var orgID string
		if err := tx.Raw(`
			SELECT unit.org_id
			FROM asset_lifecycle_units AS unit
			WHERE unit.state = ?
			  AND unit.retention_until IS NOT NULL
			  AND unit.retention_until <= statement_timestamp()
			  AND (
				unit.root_resource_type = ?
				OR NOT EXISTS (
					SELECT 1
					FROM asset_lifecycle_units AS dependent
					WHERE dependent.org_id = unit.org_id
					  AND dependent.id <> unit.id
					  AND dependent.state NOT IN (?, ?)
					  AND dependent.root_folder_path <@ unit.root_folder_path
				)
			  )
			ORDER BY unit.retention_until ASC, unit.org_id ASC, unit.id ASC
			LIMIT 1
		`, domain.LifecycleDeleted, domain.LifecycleResourceMetadata, domain.LifecycleRestored, domain.LifecyclePurged).Scan(&orgID).Error; err != nil {
			return err
		}

		if orgID == "" {
			run.Status = domain.LifecycleCleanupRunSucceeded
			run.CompletedAt = &now
			if err := tx.Save(&run).Error; err != nil {
				return err
			}
			result = &domain.LifecycleCleanupBatchResult{RunID: run.ID, Completed: true}
			return nil
		}

		if err := lockOrganizationDeletion(tx, orgID); err != nil {
			return err
		}

		var units []domain.LifecycleUnit
		if err := tx.Raw(`
			SELECT unit.*
			FROM asset_lifecycle_units AS unit
			WHERE unit.org_id = ?
			  AND unit.state = ?
			  AND unit.retention_until IS NOT NULL
			  AND unit.retention_until <= statement_timestamp()
			  AND (
				unit.root_resource_type = ?
				OR NOT EXISTS (
					SELECT 1
					FROM asset_lifecycle_units AS dependent
					WHERE dependent.org_id = unit.org_id
					  AND dependent.id <> unit.id
					  AND dependent.state NOT IN (?, ?)
					  AND dependent.root_folder_path <@ unit.root_folder_path
				)
			  )
			ORDER BY unit.retention_until ASC, unit.id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT ?
		`, orgID, domain.LifecycleDeleted, domain.LifecycleResourceMetadata, domain.LifecycleRestored, domain.LifecyclePurged, lifecycleCleanupUnitBatchSize).Scan(&units).Error; err != nil {
			return err
		}
		if len(units) == 0 {
			return fmt.Errorf("eligible lifecycle cleanup organization %s had no lockable roots", orgID)
		}

		for _, unit := range units {
			rootFolderID := unit.RootResourceID
			if unit.RootResourceType == domain.LifecycleResourceMetadata {
				if unit.OriginalFolderID == nil {
					return fmt.Errorf("metadata lifecycle unit %s has no original folder", unit.ID)
				}
				rootFolderID = *unit.OriginalFolderID
			}
			checkpoint, err := json.Marshal(map[string]any{})
			if err != nil {
				return fmt.Errorf("encode lifecycle purge checkpoint: %w", err)
			}
			unitID := unit.ID
			job := domain.LifecycleJob{
				ID:               uuid.NewString(),
				OrgID:            unit.OrgID,
				UnitID:           &unitID,
				RootResourceType: unit.RootResourceType,
				RootResourceID:   unit.RootResourceID,
				RootFolderID:     rootFolderID,
				RootFolderPath:   unit.RootFolderPath,
				InitiatedByType:  domain.LifecycleJobInitiatorSystem,
				Operation:        domain.LifecycleJobPurge,
				Status:           domain.LifecycleJobQueued,
				Checkpoint:       checkpoint,
				NextRunAt:        &now,
				QueuedAt:         &now,
			}
			if err := tx.Create(&job).Error; err != nil {
				return err
			}
			if err := tx.Model(&domain.LifecycleUnit{}).
				Where("id = ? AND org_id = ? AND state = ?", unit.ID, unit.OrgID, domain.LifecycleDeleted).
				Update("state", domain.LifecyclePurgeQueued).Error; err != nil {
				return err
			}
		}

		checkpoint, err := json.Marshal(lifecycleCleanupRunCheckpoint{
			LastOrgID:         orgID,
			LastUnitID:        units[len(units)-1].ID,
			QueuedInLastBatch: len(units),
		})
		if err != nil {
			return fmt.Errorf("encode lifecycle cleanup checkpoint: %w", err)
		}
		run.Checkpoint = checkpoint
		run.Status = domain.LifecycleCleanupRunRunning
		if err := tx.Save(&run).Error; err != nil {
			return err
		}
		result = &domain.LifecycleCleanupBatchResult{RunID: run.ID, QueuedJobs: len(units)}
		return nil
	})
	return result, err
}
