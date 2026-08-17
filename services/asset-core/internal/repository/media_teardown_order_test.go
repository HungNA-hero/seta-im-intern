package repository_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type teardownFixture struct {
	*mediaJobFixture
	priorVersionID string
	storedBytes    int64
}

func newTeardownFixture(t *testing.T) *teardownFixture {
	t.Helper()

	base := newMediaJobFixture(t)
	fixture := &teardownFixture{mediaJobFixture: base, storedBytes: 2048}
	fixture.priorVersionID = base.seedCompletedActiveVersion(t)
	base.attachPending(t)

	for _, versionID := range []string{base.versionID, fixture.priorVersionID} {
		for _, kind := range []string{"thumbnail", "web"} {
			if err := base.db.Exec(
				`INSERT INTO media_outputs (version_id, kind, object_key, content_type, width, height, size_bytes, sha256)
				 VALUES (?, ?, ?, 'image/png', 64, 64, 1024, decode(repeat('33', 32), 'hex'))`,
				versionID, kind, "processed/"+versionID+"/"+kind+"-"+uuid.NewString()+".png",
			).Error; err != nil {
				t.Fatalf("seed output: %v", err)
			}
		}
	}

	if err := base.db.Exec(
		`INSERT INTO organization_media_usage (org_id, raw_quota_bytes, reserved_raw_bytes, stored_raw_bytes)
		 VALUES (?, 1000000, 0, ?)
		 ON CONFLICT (org_id) DO UPDATE SET stored_raw_bytes = EXCLUDED.stored_raw_bytes`,
		base.orgID, fixture.storedBytes,
	).Error; err != nil {
		t.Fatalf("seed quota ledger: %v", err)
	}
	return fixture
}

func (fixture *teardownFixture) storedRawBytes(t *testing.T) int64 {
	t.Helper()
	var stored int64
	if err := fixture.db.Raw(
		"SELECT stored_raw_bytes FROM organization_media_usage WHERE org_id = ?", fixture.orgID,
	).Scan(&stored).Error; err != nil {
		t.Fatalf("read quota ledger: %v", err)
	}
	return stored
}

func (fixture *teardownFixture) rowCount(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var count int64
	if err := fixture.db.Raw(query, args...).Scan(&count).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return count
}

func TestDocumentedTeardownOrderPurgesAnAssetCompletely(t *testing.T) {
	fixture := newTeardownFixture(t)

	err := fixture.db.Transaction(func(tx *gorm.DB) error {
		steps := []struct {
			name string
			sql  string
			args []any
		}{
			{
				"1. break the pointer cycle",
				`UPDATE metadata_items SET active_media_version_id = NULL, pending_media_version_id = NULL WHERE id = ?`,
				[]any{fixture.assetID},
			},
			{
				"2. delete outbox rows",
				`DELETE FROM media_job_outbox WHERE job_id IN (SELECT id FROM media_processing_jobs WHERE asset_id = ?)`,
				[]any{fixture.assetID},
			},
			{
				"3. delete processing jobs",
				`DELETE FROM media_processing_jobs WHERE asset_id = ?`,
				[]any{fixture.assetID},
			},
			{
				"4. delete outputs",
				`DELETE FROM media_outputs WHERE version_id IN (SELECT id FROM asset_media_versions WHERE asset_id = ?)`,
				[]any{fixture.assetID},
			},
			{
				"5. delete versions",
				`DELETE FROM asset_media_versions WHERE asset_id = ?`,
				[]any{fixture.assetID},
			},
			{
				"6. delete upload sessions",
				`DELETE FROM media_upload_sessions WHERE asset_id = ?`,
				[]any{fixture.assetID},
			},
			{
				"7. release stored bytes",
				`UPDATE organization_media_usage SET stored_raw_bytes = GREATEST(stored_raw_bytes - ?, 0) WHERE org_id = ?`,
				[]any{fixture.storedBytes, fixture.orgID},
			},
			{
				"8. delete the asset",
				`DELETE FROM metadata_items WHERE id = ?`,
				[]any{fixture.assetID},
			},
		}

		for _, step := range steps {
			if err := tx.Exec(step.sql, step.args...).Error; err != nil {
				return &teardownStepError{step: step.name, cause: err}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("the documented purge order failed: %v", err)
	}

	if remaining := fixture.rowCount(t, "SELECT count(*) FROM metadata_items WHERE id = ?", fixture.assetID); remaining != 0 {
		t.Errorf("metadata_items rows = %d, want the asset gone", remaining)
	}
	for table, query := range map[string]string{
		"asset_media_versions":  "SELECT count(*) FROM asset_media_versions WHERE asset_id = ?",
		"media_upload_sessions": "SELECT count(*) FROM media_upload_sessions WHERE asset_id = ?",
		"media_processing_jobs": "SELECT count(*) FROM media_processing_jobs WHERE asset_id = ?",
	} {
		if remaining := fixture.rowCount(t, query, fixture.assetID); remaining != 0 {
			t.Errorf("%s rows = %d, want none", table, remaining)
		}
	}
	if stored := fixture.storedRawBytes(t); stored != 0 {
		t.Errorf("storedRawBytes = %d, want the ledger returned to zero", stored)
	}
}

type teardownStepError struct {
	step  string
	cause error
}

func (err *teardownStepError) Error() string { return err.step + ": " + err.cause.Error() }
func (err *teardownStepError) Unwrap() error { return err.cause }

// The order is load-bearing, not incidental. Each case performs the documented
// steps up to a point and then takes the next one too early, and asserts the
// database refuses it with a foreign-key violation specifically — an ordinary
// error would let this pass on a typo.
func TestTeardownStepsOutOfOrderAreRefusedByTheSchema(t *testing.T) {
	cases := map[string]struct {
		blockedBy string
		attempt   func(tx *gorm.DB, fixture *teardownFixture) error
	}{
		"deleting the asset before anything else": {
			blockedBy: "media_upload_sessions_asset_id_fkey",
			attempt: func(tx *gorm.DB, fixture *teardownFixture) error {
				return tx.Exec("DELETE FROM metadata_items WHERE id = ?", fixture.assetID).Error
			},
		},
		"deleting versions while the pointers still reference them": {
			blockedBy: "metadata_items_pending_media_version_id_fkey",
			attempt: func(tx *gorm.DB, fixture *teardownFixture) error {
				if err := fixture.clearDependents(tx); err != nil {
					return err
				}
				return tx.Exec("DELETE FROM asset_media_versions WHERE asset_id = ?", fixture.assetID).Error
			},
		},
		"deleting sessions before the versions that reference them": {
			blockedBy: "asset_media_versions_upload_id_fkey",
			attempt: func(tx *gorm.DB, fixture *teardownFixture) error {
				return tx.Exec("DELETE FROM media_upload_sessions WHERE asset_id = ?", fixture.assetID).Error
			},
		},
		"deleting jobs before their outbox rows": {
			blockedBy: "media_job_outbox_job_id_fkey",
			attempt: func(tx *gorm.DB, fixture *teardownFixture) error {
				return tx.Exec("DELETE FROM media_processing_jobs WHERE asset_id = ?", fixture.assetID).Error
			},
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newTeardownFixture(t)

			err := fixture.db.Transaction(func(tx *gorm.DB) error {
				return testCase.attempt(tx, fixture)
			})

			if err == nil {
				t.Fatal("the schema permitted an out-of-order delete; the documented order is not enforced")
			}
			if !isForeignKeyViolation(err) {
				t.Fatalf("error = %v, want a foreign-key violation", err)
			}
			if !strings.Contains(err.Error(), testCase.blockedBy) {
				t.Errorf("error = %v, want it blocked by %s", err, testCase.blockedBy)
			}
		})
	}
}

// clearDependents performs the documented steps that precede version deletion,
// so the version delete is refused by the pointer cycle rather than by outputs
// or jobs that the real sequence would already have removed.
func (fixture *teardownFixture) clearDependents(tx *gorm.DB) error {
	statements := []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM media_job_outbox WHERE job_id IN (SELECT id FROM media_processing_jobs WHERE asset_id = ?)`, []any{fixture.assetID}},
		{`DELETE FROM media_processing_jobs WHERE asset_id = ?`, []any{fixture.assetID}},
		{`DELETE FROM media_outputs WHERE version_id IN (SELECT id FROM asset_media_versions WHERE asset_id = ?)`, []any{fixture.assetID}},
	}
	for _, statement := range statements {
		if err := tx.Exec(statement.sql, statement.args...).Error; err != nil {
			return err
		}
	}
	return nil
}

func isForeignKeyViolation(err error) bool {
	return strings.Contains(err.Error(), "SQLSTATE 23503")
}
