package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
)

const lifecycleCleanupSchedulerLeaseSeconds = 120

type lifecycleCleanupSchedulerRepository struct {
	db *gorm.DB
}

// NewLifecycleCleanupSchedulerRepository creates the durable coordination
// boundary used by the daily retention scheduler. It only claims a short
// scheduler lease and records a day; a worker owns future physical purge work.
func NewLifecycleCleanupSchedulerRepository(db *gorm.DB) domain.LifecycleCleanupSchedulerRepository {
	return &lifecycleCleanupSchedulerRepository{db: db}
}

// AcquireDailyCleanupRun lets at most one scheduler instance create the run
// record for a calendar date. PostgreSQL time controls the lease comparison so
// hosts with slightly different clocks cannot both become the active owner.
func (r *lifecycleCleanupSchedulerRepository) AcquireDailyCleanupRun(
	ctx context.Context,
	schedulerName string,
	workerID string,
	runDate time.Time,
	timezone string,
) (domain.LifecycleCleanupRunAcquireResult, error) {
	if strings.TrimSpace(schedulerName) == "" {
		return domain.LifecycleCleanupRunAcquireResult{}, fmt.Errorf("lifecycle cleanup scheduler name is required")
	}
	if strings.TrimSpace(workerID) == "" {
		return domain.LifecycleCleanupRunAcquireResult{}, fmt.Errorf("lifecycle cleanup scheduler worker ID is required")
	}
	if runDate.IsZero() {
		return domain.LifecycleCleanupRunAcquireResult{}, fmt.Errorf("lifecycle cleanup run date is required")
	}
	if strings.TrimSpace(timezone) == "" {
		return domain.LifecycleCleanupRunAcquireResult{}, fmt.Errorf("lifecycle cleanup timezone is required")
	}

	result := domain.LifecycleCleanupRunAcquireResult{}
	runDateValue := runDate.Format("2006-01-02")
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO asset_lifecycle_scheduler_leases (scheduler_name)
			VALUES (?)
			ON CONFLICT (scheduler_name) DO NOTHING
		`, schedulerName).Error; err != nil {
			return err
		}

		var lease struct {
			SchedulerName string `gorm:"column:scheduler_name"`
		}
		if err := tx.Raw(`
			UPDATE asset_lifecycle_scheduler_leases
			SET lease_owner = ?,
				lease_expires_at = statement_timestamp() + make_interval(secs => ?),
				updated_at = statement_timestamp()
			WHERE scheduler_name = ?
			  AND (
				lease_owner IS NULL
				OR lease_expires_at <= statement_timestamp()
				OR lease_owner = ?
			  )
			RETURNING scheduler_name
		`, workerID, lifecycleCleanupSchedulerLeaseSeconds, schedulerName, workerID).Scan(&lease).Error; err != nil {
			return err
		}
		if lease.SchedulerName == "" {
			return nil
		}
		result.LeaseAcquired = true

		var run domain.LifecycleCleanupRun
		if err := tx.Raw(`
			INSERT INTO asset_lifecycle_cleanup_runs (
				scheduler_name,
				run_date,
				timezone
			)
			VALUES (?, ?::date, ?)
			ON CONFLICT (scheduler_name, run_date) DO NOTHING
			RETURNING *
		`, schedulerName, runDateValue, timezone).Scan(&run).Error; err != nil {
			return err
		}
		if run.ID != "" {
			result.Run = &run
			result.Created = true
			return nil
		}

		if err := tx.Where("scheduler_name = ? AND run_date = ?::date", schedulerName, runDateValue).
			First(&run).Error; err != nil {
			return err
		}
		result.Run = &run
		return nil
	})
	return result, err
}
