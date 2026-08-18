package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

const testQuarantine = 24 * time.Hour

func (fixture *mediaJobFixture) purgeStore() interface {
	ExpireUploadSessions(context.Context, int) (int, error)
	ClaimPurgeableMediaObjects(context.Context, time.Duration, int) ([]repository.PurgeableMediaObjects, error)
	MarkMediaObjectsPurged(context.Context, repository.PurgeableMediaObjects) error
} {
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
}

func (fixture *mediaJobFixture) seedOpenUploadSession(t *testing.T, expiresAt time.Time) string {
	return fixture.seedOpenUploadSessionForOrg(t, fixture.orgID, expiresAt)
}

func (fixture *mediaJobFixture) seedOpenUploadSessionForOrg(
	t *testing.T,
	orgID string,
	expiresAt time.Time,
) string {
	t.Helper()

	var folderID string
	if err := fixture.db.Raw(
		"SELECT folder_id FROM metadata_items WHERE id = ?", fixture.assetID,
	).Scan(&folderID).Error; err != nil {
		t.Fatalf("read fixture folder: %v", err)
	}
	assetID := uuid.NewString()
	if err := fixture.db.Exec(
		"INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES (?, ?, ?, ?)",
		assetID, folderID, "Expiry sweep asset", fixture.userID,
	).Error; err != nil {
		t.Fatalf("seed expiry asset: %v", err)
	}

	uploadID := uuid.NewString()
	createdAt := expiresAt.Add(-24 * time.Hour)
	if err := fixture.db.Exec(
		`INSERT INTO media_upload_sessions
		 (id, org_id, asset_id, requested_by, idempotency_key, request_fingerprint, state,
		  original_filename, declared_content_type, file_extension, expected_size_bytes,
		  declared_checksum_sha256, raw_object_key, credential_expires_at, session_expires_at,
		  created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, decode(repeat('2a', 32), 'hex'), 'created',
		         'photo.png', 'image/png', 'png', 1024,
		         decode(repeat('2a', 32), 'hex'), ?, now(), ?, ?, ?)`,
		uploadID,
		orgID,
		assetID,
		fixture.userID,
		uuid.NewString(),
		"raw/"+orgID+"/"+assetID+"/"+uploadID+"/original.png",
		expiresAt,
		createdAt,
		createdAt,
	).Error; err != nil {
		t.Fatalf("seed open upload session: %v", err)
	}
	return uploadID
}

func TestExpireUploadSessionsIsBoundedAndReleasesReservationsOnce(t *testing.T) {
	fixture := newMediaJobFixture(t)
	due := []string{
		fixture.seedOpenUploadSession(t, time.Now().Add(-3*time.Hour)),
		fixture.seedOpenUploadSession(t, time.Now().Add(-2*time.Hour)),
		fixture.seedOpenUploadSession(t, time.Now().Add(-time.Hour)),
	}
	live := fixture.seedOpenUploadSession(t, time.Now().Add(24*time.Hour))
	if err := fixture.db.Exec(
		`INSERT INTO organization_media_usage
		 (org_id, raw_quota_bytes, reserved_raw_bytes, stored_raw_bytes)
		 VALUES (?, 1048576, 4096, 0)`,
		fixture.orgID,
	).Error; err != nil {
		t.Fatalf("seed quota ledger: %v", err)
	}

	store := fixture.purgeStore()
	expired, err := store.ExpireUploadSessions(fixture.ctx, 2)
	if err != nil {
		t.Fatalf("first expiration sweep: %v", err)
	}
	if expired != 2 {
		t.Fatalf("first sweep expired %d sessions, want 2", expired)
	}

	var expiredRows int64
	if err := fixture.db.Model(&domain.MediaUploadSession{}).
		Where("id IN ? AND state = ?", due, domain.UploadSessionExpired).
		Count(&expiredRows).Error; err != nil {
		t.Fatalf("count expired sessions: %v", err)
	}
	if expiredRows != 2 {
		t.Errorf("expired rows = %d, want 2 after the bounded sweep", expiredRows)
	}

	var reserved int64
	if err := fixture.db.Raw(
		"SELECT reserved_raw_bytes FROM organization_media_usage WHERE org_id = ?", fixture.orgID,
	).Scan(&reserved).Error; err != nil {
		t.Fatalf("read quota after first sweep: %v", err)
	}
	if reserved != 2048 {
		t.Errorf("reserved bytes = %d, want 2048 after two releases", reserved)
	}

	expired, err = store.ExpireUploadSessions(fixture.ctx, 2)
	if err != nil {
		t.Fatalf("second expiration sweep: %v", err)
	}
	if expired != 1 {
		t.Fatalf("second sweep expired %d sessions, want 1", expired)
	}
	expired, err = store.ExpireUploadSessions(fixture.ctx, 2)
	if err != nil {
		t.Fatalf("repeat expiration sweep: %v", err)
	}
	if expired != 0 {
		t.Fatalf("repeat sweep expired %d sessions, want 0", expired)
	}

	if err := fixture.db.Raw(
		"SELECT reserved_raw_bytes FROM organization_media_usage WHERE org_id = ?", fixture.orgID,
	).Scan(&reserved).Error; err != nil {
		t.Fatalf("read final quota: %v", err)
	}
	if reserved != 1024 {
		t.Errorf("reserved bytes = %d, want only the live session's 1024", reserved)
	}

	var liveState domain.UploadSessionState
	if err := fixture.db.Raw(
		"SELECT state FROM media_upload_sessions WHERE id = ?", live,
	).Scan(&liveState).Error; err != nil {
		t.Fatalf("read live session: %v", err)
	}
	if liveState != domain.UploadSessionCreated {
		t.Errorf("live session state = %q, want %q", liveState, domain.UploadSessionCreated)
	}
}

// ageSession backdates the timestamps the sweep actually reads. updated_at is
// deliberately not one of them: a trigger maintains it, so writing to it here
// would be overwritten and the test would prove nothing.
func (fixture *mediaJobFixture) ageSession(t *testing.T, uploadID string, age time.Duration) {
	t.Helper()
	if err := fixture.db.Exec(
		`UPDATE media_upload_sessions
		 SET cancelled_at = CASE WHEN cancelled_at IS NOT NULL THEN now() - ?::interval ELSE NULL END,
		     expired_at   = CASE WHEN expired_at IS NOT NULL THEN now() - ?::interval ELSE NULL END
		 WHERE id = ?`,
		age.String(), age.String(), uploadID,
	).Error; err != nil {
		t.Fatalf("age session: %v", err)
	}
}

func (fixture *mediaJobFixture) abandonSession(t *testing.T, state string, age time.Duration) string {
	t.Helper()

	uploadID := uuid.NewString()
	rawKey := "raw/" + fixture.orgID + "/" + fixture.assetID + "/" + uploadID + "/original.png"
	if err := fixture.db.Exec(
		`INSERT INTO media_upload_sessions
		 (id, org_id, asset_id, requested_by, idempotency_key, request_fingerprint, state,
		  original_filename, declared_content_type, file_extension, expected_size_bytes,
		  declared_checksum_sha256, raw_object_key, credential_expires_at, session_expires_at, expired_at, cancelled_at)
		 VALUES (?, ?, ?, ?, ?, decode(repeat('2a', 32), 'hex'), ?, 'photo.png', 'image/png', 'png', 1024,
		         decode(repeat('2a', 32), 'hex'), ?, now(), now(),
		         CASE WHEN ? = 'expired' THEN now() ELSE NULL END,
		         CASE WHEN ? = 'cancelled' THEN now() ELSE NULL END)`,
		uploadID, fixture.orgID, fixture.assetID, fixture.userID, uuid.NewString(), state, rawKey, state, state,
	).Error; err != nil {
		t.Fatalf("seed abandoned session: %v", err)
	}
	fixture.ageSession(t, uploadID, age)
	return rawKey
}

func (fixture *mediaJobFixture) failTheSeededVersion(t *testing.T, age time.Duration) {
	t.Helper()
	if err := fixture.db.Exec(
		"UPDATE asset_media_versions SET status = 'failed', failure_code = 'INVALID_IMAGE', failed_at = now() - ?::interval WHERE id = ?",
		age.String(), fixture.versionID,
	).Error; err != nil {
		t.Fatalf("fail version: %v", err)
	}
}

func collectRawKeys(batches []repository.PurgeableMediaObjects) []string {
	keys := make([]string, 0, len(batches))
	for _, batch := range batches {
		if batch.RawObjectKey != "" {
			keys = append(keys, batch.RawObjectKey)
		}
	}
	return keys
}

// An upload the client walked away from leaves an object nothing references.
// After the quarantine window it is reclaimable; before it, it is not.
func TestClaimPurgeableSkipsObjectsInsideTheQuarantineWindow(t *testing.T) {
	fixture := newMediaJobFixture(t)
	recent := fixture.abandonSession(t, "cancelled", time.Hour)

	batches, err := fixture.purgeStore().ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("ClaimPurgeableMediaObjects: %v", err)
	}

	for _, key := range collectRawKeys(batches) {
		if key == recent {
			t.Error("an object still inside its 24-hour quarantine was offered for deletion")
		}
	}
}

func TestClaimPurgeableOffersAbandonedAndInvalidObjects(t *testing.T) {
	fixture := newMediaJobFixture(t)
	cancelled := fixture.abandonSession(t, "cancelled", 30*time.Hour)
	expired := fixture.abandonSession(t, "expired", 30*time.Hour)
	fixture.failTheSeededVersion(t, 30*time.Hour)
	invalidRaw := "raw/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.uploadID + "/original.png"

	batches, err := fixture.purgeStore().ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("ClaimPurgeableMediaObjects: %v", err)
	}

	offered := collectRawKeys(batches)
	for _, wanted := range []string{cancelled, expired, invalidRaw} {
		if !contains(offered, wanted) {
			t.Errorf("offered = %v, want it to include %s", offered, wanted)
		}
	}
}

// The rule that must never break: a completed version's raw original is kept
// for the asset's lifetime. The sweep is the only thing that deletes raw bytes,
// so it is the only place this can go wrong.
func TestClaimPurgeableNeverOffersACompletedVersionsRawObject(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.seedCompletedActiveVersion(t)
	if err := fixture.db.Exec("UPDATE media_upload_sessions SET updated_at = now() - interval '60 days'").Error; err != nil {
		t.Fatalf("age every session: %v", err)
	}

	batches, err := fixture.purgeStore().ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("ClaimPurgeableMediaObjects: %v", err)
	}

	var completedRaw string
	if err := fixture.db.Raw(
		"SELECT raw_object_key FROM asset_media_versions WHERE asset_id = ? AND status = 'completed'", fixture.assetID,
	).Scan(&completedRaw).Error; err != nil {
		t.Fatalf("read completed raw key: %v", err)
	}
	if completedRaw == "" {
		t.Fatal("the fixture did not seed a completed version")
	}
	if contains(collectRawKeys(batches), completedRaw) {
		t.Error("a completed version's raw original was offered for deletion")
	}
}

// A worker can crash after writing a derivative but before promotion creates
// media_outputs rows. The sweep must still derive every immutable key from the
// failed version's trusted identifiers.
func TestClaimPurgeableDerivesFailedVersionKeysWithoutOutputRows(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.failTheSeededVersion(t, 30*time.Hour)

	batches, err := fixture.purgeStore().ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("ClaimPurgeableMediaObjects: %v", err)
	}

	var processed []string
	for _, batch := range batches {
		if batch.VersionID == fixture.versionID {
			processed = append(processed, batch.ProcessedObjectKeys...)
		}
	}
	wantThumbnail := "processed/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.versionID + "/thumbnail-256.png"
	wantWeb := "processed/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.versionID + "/web-1080.png"
	if !contains(processed, wantThumbnail) {
		t.Errorf("processed = %v, want %s", processed, wantThumbnail)
	}
	if !contains(processed, wantWeb) {
		t.Errorf("processed = %v, want %s", processed, wantWeb)
	}
}

// Without a durable marker the same candidates would be re-offered on every
// sweep, and a bounded batch would never reach newer ones.
func TestMarkPurgedRemovesTheObjectFromLaterSweeps(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.abandonSession(t, "cancelled", 30*time.Hour)
	store := fixture.purgeStore()

	first, err := store.ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("the first sweep offered nothing")
	}
	for _, batch := range first {
		if err := store.MarkMediaObjectsPurged(fixture.ctx, batch); err != nil {
			t.Fatalf("MarkMediaObjectsPurged: %v", err)
		}
	}

	second, err := store.ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second sweep offered %d batches, want none", len(second))
	}
}

// A failed version's bytes were converted to stored quota at commit. Purging
// them without releasing the quota would leak an organization's allowance.
func TestMarkPurgedReleasesStoredQuotaForAnInvalidObject(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.failTheSeededVersion(t, 30*time.Hour)
	if err := fixture.db.Exec(
		`INSERT INTO organization_media_usage (org_id, raw_quota_bytes, reserved_raw_bytes, stored_raw_bytes)
		 VALUES (?, 1000000, 0, 2048)
		 ON CONFLICT (org_id) DO UPDATE SET stored_raw_bytes = 2048`, fixture.orgID,
	).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	store := fixture.purgeStore()

	batches, err := store.ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 100)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	for _, batch := range batches {
		if err := store.MarkMediaObjectsPurged(fixture.ctx, batch); err != nil {
			t.Fatalf("MarkMediaObjectsPurged: %v", err)
		}
		if err := store.MarkMediaObjectsPurged(fixture.ctx, batch); err != nil {
			t.Fatalf("repeated MarkMediaObjectsPurged: %v", err)
		}
	}

	var stored int64
	if err := fixture.db.Raw(
		"SELECT stored_raw_bytes FROM organization_media_usage WHERE org_id = ?", fixture.orgID,
	).Scan(&stored).Error; err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if stored != 1024 {
		t.Errorf("storedRawBytes = %d, want one 1024-byte invalid object released exactly once", stored)
	}
}

func TestClaimPurgeableRespectsItsBatchLimit(t *testing.T) {
	fixture := newMediaJobFixture(t)
	for i := 0; i < 5; i++ {
		fixture.abandonSession(t, "cancelled", 30*time.Hour)
	}

	batches, err := fixture.purgeStore().ClaimPurgeableMediaObjects(fixture.ctx, testQuarantine, 2)
	if err != nil {
		t.Fatalf("ClaimPurgeableMediaObjects: %v", err)
	}

	if len(batches) > 2 {
		t.Errorf("returned %d batches, want at most the limit of 2", len(batches))
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Even a full batch of persistent failures must move behind untouched sessions
// on the next sweep. Otherwise the LIMIT query selects the same broken rows
// forever and later reservations are never released.
func TestExpireUploadSessionsRotatesPastAFailedBatch(t *testing.T) {
	fixture := newMediaJobFixture(t)
	strandedOrg := uuid.NewString()
	if err := fixture.db.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", strandedOrg).Error; err != nil {
		t.Fatalf("seed stranded organization: %v", err)
	}
	for index := 0; index < 2; index++ {
		fixture.seedOpenUploadSessionForOrg(
			t,
			strandedOrg,
			time.Now().Add(time.Duration(-4+index)*time.Hour),
		)
	}
	healthy := fixture.seedOpenUploadSession(t, time.Now().Add(-time.Hour))
	if err := fixture.db.Exec(
		`INSERT INTO organization_media_usage (org_id, raw_quota_bytes, reserved_raw_bytes, stored_raw_bytes)
		 VALUES (?, 1048576, 1024, 0)`, fixture.orgID,
	).Error; err != nil {
		t.Fatalf("seed quota ledger: %v", err)
	}

	store := fixture.purgeStore()
	expired, err := store.ExpireUploadSessions(fixture.ctx, 2)

	if err == nil {
		t.Error("the failed batch must be reported")
	}
	if expired != 0 {
		t.Errorf("first sweep expired %d sessions, want 0", expired)
	}

	expired, err = store.ExpireUploadSessions(fixture.ctx, 2)
	if err == nil {
		t.Error("the retried failed candidate must still be reported")
	}
	if expired != 1 {
		t.Errorf("second sweep expired %d sessions, want the healthy session reached", expired)
	}

	var state string
	if err := fixture.db.Raw("SELECT state FROM media_upload_sessions WHERE id = ?", healthy).Scan(&state).Error; err != nil {
		t.Fatalf("read healthy session: %v", err)
	}
	if state != string(domain.UploadSessionExpired) {
		t.Errorf("healthy session state = %q, want expired", state)
	}
}
