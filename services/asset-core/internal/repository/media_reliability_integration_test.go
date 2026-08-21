package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestMediaReliability_IdempotencyFingerprintBindsChecksumWithinAssetScope(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	retryKey := uuid.NewString()
	request := fixture.request(fixture.assets[0], retryKey, 7)

	original, replayed, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil || replayed {
		t.Fatalf("create original session: replayed=%v err=%v", replayed, err)
	}

	identical, replayed, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil || !replayed || identical.ID != original.ID {
		t.Fatalf("identical retry = session %q replayed=%v err=%v, want session %q", identical.ID, replayed, err, original.ID)
	}

	changedChecksum := request
	changedChecksum.DeclaredChecksumSHA256 = bytes.Repeat([]byte{0x2b}, domain.ChecksumByteLength)
	if _, _, err := fixture.reserve(changedChecksum, uuid.NewString(), 100); !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("checksum-changing retry error = %v, want %v", err, repository.ErrIdempotencyConflict)
	}

	otherAsset := fixture.request(fixture.assets[1], retryKey, 7)
	independent, replayed, err := fixture.reserve(otherAsset, uuid.NewString(), 100)
	if err != nil || replayed || independent.ID == original.ID {
		t.Fatalf("same key on another asset = session %q replayed=%v err=%v", independent.ID, replayed, err)
	}
}

func TestMediaReliability_RepeatedCommitReturnsTheSameDurableJob(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	commit := domain.CommitUploadRequest{
		OrgID: fixture.orgID, AssetID: request.AssetID, UploadID: session.ID, RequestedBy: fixture.userID,
	}
	attributes := domain.ObjectAttributes{
		SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: request.DeclaredChecksumSHA256,
	}
	firstIDs := repository.CommitUploadIDs{
		VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString(),
	}

	first, replayed, err := fixture.repo.CommitUpload(fixture.ctx, commit, attributes, firstIDs)
	if err != nil || replayed {
		t.Fatalf("first commit: replayed=%v err=%v", replayed, err)
	}
	if err := fixture.db.Model(&domain.MediaProcessingJob{}).
		Where("id = ?", first.JobID).
		Update("status", domain.ProcessingJobCompleted).Error; err != nil {
		t.Fatalf("complete job before replay: %v", err)
	}
	second, replayed, err := fixture.repo.CommitUpload(
		fixture.ctx,
		commit,
		domain.ObjectAttributes{},
		repository.CommitUploadIDs{
			VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString(),
		},
	)
	if err != nil || !replayed {
		t.Fatalf("repeated commit: replayed=%v err=%v", replayed, err)
	}
	if second != first || second.JobID != firstIDs.JobID || second.VersionID != firstIDs.VersionID {
		t.Fatalf("repeated acceptance = %#v, want %#v", second, first)
	}

	var jobCount, versionCount, outboxCount int64
	fixture.db.Model(&domain.MediaProcessingJob{}).Where("version_id = ?", first.VersionID).Count(&jobCount)
	fixture.db.Model(&domain.AssetMediaVersion{}).Where("upload_id = ?", session.ID).Count(&versionCount)
	fixture.db.Model(&domain.MediaJobOutboxRecord{}).Where("job_id = ?", first.JobID).Count(&outboxCount)
	if jobCount != 1 || versionCount != 1 || outboxCount != 1 {
		t.Fatalf("repeated commit created duplicates: jobs=%d versions=%d outbox=%d", jobCount, versionCount, outboxCount)
	}
}

func TestMediaReliability_ExpiredThirdAttemptBecomesTerminalWithoutAFourthExecution(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	store := fixture.store()

	for attempt := 1; attempt <= 2; attempt++ {
		fixture.publishPendingOutbox(t)
		job, lease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-retry")
		if err != nil {
			t.Fatalf("claim attempt %d: %v", attempt, err)
		}
		settled, err := store.SettleExecutionFailure(fixture.ctx, job, lease)
		if err != nil || !settled {
			t.Fatalf("settle attempt %d: applied=%v err=%v", attempt, settled, err)
		}
		if err := fixture.db.Exec(
			"UPDATE media_processing_jobs SET next_attempt_at = statement_timestamp() WHERE id = ?",
			fixture.jobID,
		).Error; err != nil {
			t.Fatalf("make attempt %d due: %v", attempt+1, err)
		}
	}

	fixture.publishPendingOutbox(t)
	third, thirdLease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-crashed")
	if err != nil {
		t.Fatalf("claim attempt three: %v", err)
	}
	if third.AttemptCount != 3 {
		t.Fatalf("third claim attempt count = %d, want 3", third.AttemptCount)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = ?",
		fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire third-attempt lease: %v", err)
	}

	recovered, recoveryLease, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-recovery")
	if !errors.Is(err, repository.ErrJobTerminal) {
		t.Fatalf("recover exhausted lease error = %v, want %v", err, repository.ErrJobTerminal)
	}
	if recovered.ID != "" || !recoveryLease.ExpiresAt.IsZero() {
		t.Fatalf("exhausted recovery exposed a fourth execution: job=%#v lease=%#v", recovered, recoveryLease)
	}

	current := fixture.jobRow()
	if current.Status != domain.ProcessingJobFailed || current.AttemptCount != 3 || current.FailedAt == nil {
		t.Fatalf("recovered exhausted job = %#v, want terminal failure at attempt three", current)
	}
	if current.LeaseOwner != nil || current.LeaseExpiresAt != nil {
		t.Fatalf("terminal recovery retained lease state: %#v", current)
	}
	if current.LastErrorCode == nil || *current.LastErrorCode != "MEDIA_PROCESSING_FAILED" {
		t.Fatalf("terminal recovery error code = %v", current.LastErrorCode)
	}
	var version domain.AssetMediaVersion
	if err := fixture.db.Take(&version, "id = ?", fixture.versionID).Error; err != nil {
		t.Fatalf("load failed version: %v", err)
	}
	if version.Status != domain.MediaVersionFailed || version.FailedAt == nil {
		t.Fatalf("recovered exhausted version = %#v, want failed", version)
	}
	_, pending := fixture.assetPointers(t)
	if pending != nil {
		t.Fatalf("terminal recovery retained pending pointer %v", pending)
	}

	_ = thirdLease // The crashed worker deliberately never settles this lease.
}

func TestMediaReliability_FiveRedeliveriesDoNotSpendProcessingAttempts(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := fixture.store()

	if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-owner"); err != nil {
		t.Fatalf("claim execution: %v", err)
	}
	for redelivery := 1; redelivery <= 5; redelivery++ {
		if _, _, err := store.ClaimJob(fixture.ctx, fixture.jobID, "worker-redelivery"); !errors.Is(err, repository.ErrJobLeased) {
			t.Fatalf("redelivery %d error = %v, want %v", redelivery, err, repository.ErrJobLeased)
		}
	}
	if attempts := fixture.jobRow().AttemptCount; attempts != 1 {
		t.Fatalf("five redeliveries spent %d processing attempts, want 1", attempts)
	}
}

func TestMediaReliability_NotificationIsolationIsDurableIdempotentAndPreservesPendingMedia(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	fixture.publishPendingOutbox(t)
	store := repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
	record := consume.QuarantinedRecord{
		SourceTopic:     "media-processing.v1",
		SourcePartition: 0,
		SourceOffset:    42,
		QuarantineID:    "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273",
		AggregateID:     fixture.jobID,
		ReasonCode:      "UNSUPPORTED_SCHEMA_VERSION",
	}

	if err := store.IsolateNotification(fixture.ctx, record); err != nil {
		t.Fatalf("IsolateNotification: %v", err)
	}
	first := fixture.jobRow()
	if first.Status != domain.ProcessingJobFailed || first.NotificationIsolatedAt == nil ||
		first.LastErrorCode == nil || *first.LastErrorCode != "MEDIA_NOTIFICATION_ISOLATED" {
		t.Fatalf("isolated job = %#v", first)
	}
	if first.AttemptCount != 0 || first.LeaseOwner != nil || first.LeaseExpiresAt != nil {
		t.Fatalf("quarantine consumed an attempt or retained a lease: %#v", first)
	}
	var version domain.AssetMediaVersion
	if err := fixture.db.Take(&version, "id = ?", fixture.versionID).Error; err != nil {
		t.Fatalf("load pending version: %v", err)
	}
	if version.Status != domain.MediaVersionPending || version.FailedAt != nil {
		t.Fatalf("notification isolation changed pending media version: %#v", version)
	}
	_, pending := fixture.assetPointers(t)
	if pending == nil || *pending != fixture.versionID {
		t.Fatalf("notification isolation removed pending media pointer: %v", pending)
	}

	if err := store.IsolateNotification(fixture.ctx, record); err != nil {
		t.Fatalf("idempotent IsolateNotification: %v", err)
	}
	second := fixture.jobRow()
	if second.NotificationIsolatedAt == nil || !second.NotificationIsolatedAt.Equal(*first.NotificationIsolatedAt) {
		t.Fatalf("redelivery changed isolation identity time: first=%v second=%v", first.NotificationIsolatedAt, second.NotificationIsolatedAt)
	}
}

func TestMediaReliability_PrivilegedReplayRebuildsCurrentEventAndAuditsOperatorAtomically(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.attachPending(t)
	fixture.publishPendingOutbox(t)
	operatorID := uuid.NewString()
	if err := fixture.db.Exec("INSERT INTO user_ref (user_id) VALUES (?)", operatorID).Error; err != nil {
		t.Fatalf("seed replay operator: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET attempt_count = 2 WHERE id = ?",
		fixture.jobID,
	).Error; err != nil {
		t.Fatalf("seed processing attempts: %v", err)
	}
	store := repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
	quarantineID := "64f4f7570b3f7b1ec67f1ea7a80ff2ec9f44acb91544a456b820087aa62ed273"
	if err := store.IsolateNotification(fixture.ctx, consume.QuarantinedRecord{
		AggregateID:  fixture.jobID,
		QuarantineID: quarantineID,
	}); err != nil {
		t.Fatalf("isolate notification: %v", err)
	}

	eventID, err := outbox.Replay(fixture.ctx, store, outbox.ReplayRequest{
		QuarantineID: quarantineID,
		JobID:        fixture.jobID,
		Operator:     operatorID,
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	job := fixture.jobRow()
	if job.Status != domain.ProcessingJobQueued || job.NotificationIsolatedAt != nil ||
		job.Stage != nil || job.LeaseOwner != nil || job.LeaseExpiresAt != nil {
		t.Fatalf("replayed job = %#v", job)
	}
	if job.AttemptCount != 2 || job.LastErrorCode != nil || job.FailedAt != nil {
		t.Fatalf("replay reset durable attempts incorrectly or retained failure: %#v", job)
	}

	var record domain.MediaJobOutboxRecord
	if err := fixture.db.Take(&record, "id = ? AND status = 'pending'", eventID.String()).Error; err != nil {
		t.Fatalf("load replay outbox event: %v", err)
	}
	envelope, err := event.Parse(record.Payload, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("parse replay envelope: %v", err)
	}
	payload, err := media.Parse(envelope)
	if err != nil {
		t.Fatalf("parse replay payload: %v", err)
	}
	if envelope.EventID != eventID.String() || envelope.OrgID != fixture.orgID ||
		payload.JobID != fixture.jobID || payload.AssetID != fixture.assetID ||
		payload.UploadID != fixture.uploadID || payload.VersionID != fixture.versionID {
		t.Fatalf("replay event was not rebuilt from current database truth: envelope=%#v payload=%#v", envelope, payload)
	}
	if bytes.Contains(record.Payload, []byte(quarantineID)) {
		t.Fatal("replay copied dead-letter diagnostics into the main-topic payload")
	}

	var updatedBy *string
	if err := fixture.db.Raw("SELECT updated_by::text FROM metadata_items WHERE id = ?", fixture.assetID).Scan(&updatedBy).Error; err != nil {
		t.Fatalf("read replay audit actor: %v", err)
	}
	if updatedBy == nil || *updatedBy != operatorID {
		t.Fatalf("replay audit actor = %v, want %s", updatedBy, operatorID)
	}

	if _, err := outbox.Replay(fixture.ctx, store, outbox.ReplayRequest{
		QuarantineID: quarantineID,
		JobID:        fixture.jobID,
		Operator:     operatorID,
	}); !errors.Is(err, outbox.ErrNotIsolated) {
		t.Fatalf("second replay error = %v, want %v", err, outbox.ErrNotIsolated)
	}
}

func TestMediaReliability_NotificationVerificationUsesAllCurrentDatabaseIdentities(t *testing.T) {
	fixture := newMediaJobFixture(t)
	store := repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
	payload := media.Payload{
		AssetID: fixture.assetID, UploadID: fixture.uploadID,
		VersionID: fixture.versionID, JobID: fixture.jobID,
	}
	if err := store.VerifyNotification(fixture.ctx, fixture.orgID, payload); err != nil {
		t.Fatalf("verify matching notification: %v", err)
	}

	for field, mutate := range map[string]func(*media.Payload) string{
		"organization": func(*media.Payload) string { return uuid.NewString() },
		"asset": func(payload *media.Payload) string {
			payload.AssetID = uuid.NewString()
			return fixture.orgID
		},
		"upload": func(payload *media.Payload) string {
			payload.UploadID = uuid.NewString()
			return fixture.orgID
		},
		"version": func(payload *media.Payload) string {
			payload.VersionID = uuid.NewString()
			return fixture.orgID
		},
	} {
		t.Run(field, func(t *testing.T) {
			mismatched := payload
			orgID := mutate(&mismatched)
			if err := store.VerifyNotification(fixture.ctx, orgID, mismatched); !errors.Is(err, repository.ErrNotificationMismatch) {
				t.Fatalf("VerifyNotification error = %v, want %v", err, repository.ErrNotificationMismatch)
			}
		})
	}

	missing := payload
	missing.JobID = uuid.NewString()
	if err := store.VerifyNotification(fixture.ctx, fixture.orgID, missing); !errors.Is(err, repository.ErrJobNotFound) {
		t.Fatalf("missing-job error = %v, want %v", err, repository.ErrJobNotFound)
	}
	if attempts := fixture.jobRow().AttemptCount; attempts != 0 {
		t.Fatalf("verification mismatches consumed %d attempts, want 0", attempts)
	}
}

func TestMediaReliability_StatusReadsTheNewestJobInCurrentOrganizationAndAssetScope(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.seedAdditionalJobsWithOutboxRows(t, 1)
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET created_at = statement_timestamp() - interval '1 hour' WHERE id = ?",
		fixture.jobID,
	).Error; err != nil {
		t.Fatalf("age original job: %v", err)
	}
	var expectedJobID string
	if err := fixture.db.Raw(
		"SELECT id::text FROM media_processing_jobs WHERE org_id = ? AND asset_id = ? ORDER BY created_at DESC, id DESC LIMIT 1",
		fixture.orgID,
		fixture.assetID,
	).Scan(&expectedJobID).Error; err != nil {
		t.Fatalf("load expected newest job: %v", err)
	}
	repo := repository.NewMediaRepository(fixture.db, mediaFixedClock{at: time.Now().UTC()})

	record, err := repo.GetLatestMediaStatus(fixture.ctx, repository.MediaStatusScope{
		OrgID: fixture.orgID, AssetID: fixture.assetID,
	})
	if err != nil {
		t.Fatalf("GetLatestMediaStatus: %v", err)
	}
	if record.Job.ID != expectedJobID || record.Job.OrgID != fixture.orgID || record.Job.AssetID != fixture.assetID {
		t.Fatalf("newest scoped job = %#v, want %s", record.Job, expectedJobID)
	}
	if record.Version.ID != record.Job.VersionID || record.Session.ID != record.Version.UploadID {
		t.Fatalf("status joins do not share one media identity: job=%#v version=%#v session=%#v", record.Job, record.Version, record.Session)
	}

	for name, scope := range map[string]repository.MediaStatusScope{
		"other organization": {OrgID: uuid.NewString(), AssetID: fixture.assetID},
		"other asset":        {OrgID: fixture.orgID, AssetID: uuid.NewString()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := repo.GetLatestMediaStatus(fixture.ctx, scope); !errors.Is(err, repository.ErrMediaStatusNotFound) {
				t.Fatalf("error = %v, want %v", err, repository.ErrMediaStatusNotFound)
			}
		})
	}
	if err := fixture.db.Exec("UPDATE metadata_items SET deleted_at = statement_timestamp() WHERE id = ?", fixture.assetID).Error; err != nil {
		t.Fatalf("soft delete status asset: %v", err)
	}
	if _, err := repo.GetLatestMediaStatus(fixture.ctx, repository.MediaStatusScope{
		OrgID: fixture.orgID, AssetID: fixture.assetID,
	}); !errors.Is(err, repository.ErrMediaStatusNotFound) {
		t.Fatalf("deleted asset error = %v, want %v", err, repository.ErrMediaStatusNotFound)
	}
}

func TestMediaReliability_CompletedStatusLoadsOnlyTheVersionsDirectOutputRows(t *testing.T) {
	fixture := newMediaJobFixture(t)
	if err := fixture.db.Exec(`
		UPDATE asset_media_versions
		SET status = 'completed', detected_content_type = 'image/png',
		    source_width = 1080, source_height = 540,
		    sha256 = decode(repeat('2a', 32), 'hex'), completed_at = statement_timestamp()
		WHERE id = ?`, fixture.versionID).Error; err != nil {
		t.Fatalf("complete status version: %v", err)
	}
	if err := fixture.db.Exec(`
		UPDATE media_processing_jobs
		SET status = 'completed', completed_at = statement_timestamp()
		WHERE id = ?`, fixture.jobID).Error; err != nil {
		t.Fatalf("complete status job: %v", err)
	}
	for _, output := range []domain.MediaOutput{
		{
			ID: uuid.NewString(), VersionID: fixture.versionID, Kind: domain.MediaOutputThumbnail,
			ObjectKey:   "processed/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.versionID + "/thumbnail-256.png",
			ContentType: domain.MediaContentTypePNG, Width: 256, Height: 128, SizeBytes: 900,
			SHA256: bytes.Repeat([]byte{0x3a}, domain.ChecksumByteLength),
		},
		{
			ID: uuid.NewString(), VersionID: fixture.versionID, Kind: domain.MediaOutputWeb,
			ObjectKey:   "processed/" + fixture.orgID + "/" + fixture.assetID + "/" + fixture.versionID + "/web-1080.png",
			ContentType: domain.MediaContentTypePNG, Width: 1080, Height: 540, SizeBytes: 1800,
			SHA256: bytes.Repeat([]byte{0x4a}, domain.ChecksumByteLength),
		},
	} {
		if err := fixture.db.Create(&output).Error; err != nil {
			t.Fatalf("seed status output %s: %v", output.Kind, err)
		}
	}
	repo := repository.NewMediaRepository(fixture.db, mediaFixedClock{at: time.Now().UTC()})

	record, err := repo.GetLatestMediaStatus(fixture.ctx, repository.MediaStatusScope{
		OrgID: fixture.orgID, AssetID: fixture.assetID,
	})
	if err != nil {
		t.Fatalf("GetLatestMediaStatus: %v", err)
	}
	if len(record.Outputs) != 2 || record.Outputs[0].VersionID != fixture.versionID || record.Outputs[1].VersionID != fixture.versionID {
		t.Fatalf("direct status outputs = %#v", record.Outputs)
	}
	for _, output := range record.Outputs {
		if output.Kind != domain.MediaOutputThumbnail && output.Kind != domain.MediaOutputWeb {
			t.Fatalf("unexpected output kind in status: %#v", output)
		}
		if output.SizeBytes <= 0 || output.ObjectKey == "" || domain.ObjectKey(output.ObjectKey).IsRaw() {
			t.Fatalf("unsafe or incomplete status output: %#v", output)
		}
	}
}

func TestMediaReliability_ReconcilesAStalePublishedQueuedJobFromDatabaseTruth(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.publishPendingOutbox(t)
	if err := fixture.db.Exec(
		"UPDATE media_job_outbox SET published_at = statement_timestamp() - interval '61 seconds' WHERE id = ?",
		fixture.outboxID,
	).Error; err != nil {
		t.Fatalf("age published dispatch: %v", err)
	}
	store := repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)

	inserted, err := store.ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil {
		t.Fatalf("ReconcileStaleDispatches: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("reconciled rows = %d, want 1", inserted)
	}

	var record domain.MediaJobOutboxRecord
	if err := fixture.db.
		Where("job_id = ? AND status = 'pending'", fixture.jobID).
		Take(&record).Error; err != nil {
		t.Fatalf("load reconciliation event: %v", err)
	}
	envelope, err := event.Parse(record.Payload, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("parse reconciliation envelope: %v", err)
	}
	payload, err := media.Parse(envelope)
	if err != nil {
		t.Fatalf("parse reconciliation payload: %v", err)
	}
	if envelope.EventID != record.ID || envelope.OrgID != fixture.orgID ||
		payload.AssetID != fixture.assetID || payload.UploadID != fixture.uploadID ||
		payload.VersionID != fixture.versionID || payload.JobID != fixture.jobID {
		t.Fatalf("reconciliation event does not reflect database truth: envelope=%#v payload=%#v", envelope, payload)
	}
	var generic map[string]any
	if err := json.Unmarshal(record.Payload, &generic); err != nil {
		t.Fatalf("decode reconciliation event: %v", err)
	}
	if _, carriesTrace := generic["traceparent"]; carriesTrace {
		t.Fatal("database reconciliation invented an originating traceparent")
	}
	job := fixture.jobRow()
	if job.Status != domain.ProcessingJobQueued || job.AttemptCount != 0 || !job.NextAttemptAt.Before(time.Now().Add(time.Second)) {
		t.Fatalf("reconciliation changed job authority: %#v", job)
	}
}

func TestMediaReliability_ReconciliationLeavesRelayOwnedAndIneligibleJobsAlone(t *testing.T) {
	cases := []string{
		"pending publication",
		"publishing lease",
		"fresh publication",
		"never published",
		"not due",
		"terminal",
		"notification isolated",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newMediaJobFixture(t)
			switch name {
			case "pending publication":
				// The atomically inserted row still belongs to the relay.
			case "publishing lease":
				if err := fixture.db.Exec(`
					UPDATE media_job_outbox
					SET status = 'publishing', lease_owner = 'relay-a',
					    lease_expires_at = statement_timestamp() + interval '30 seconds'
					WHERE id = ?`, fixture.outboxID).Error; err != nil {
					t.Fatalf("lease publication: %v", err)
				}
			case "fresh publication":
				fixture.publishPendingOutbox(t)
			case "never published":
				if err := fixture.db.Exec("DELETE FROM media_job_outbox WHERE id = ?", fixture.outboxID).Error; err != nil {
					t.Fatalf("remove unpublished fixture event: %v", err)
				}
			case "not due":
				agePublishedDispatch(t, fixture)
				if err := fixture.db.Exec(
					"UPDATE media_processing_jobs SET next_attempt_at = statement_timestamp() + interval '1 hour' WHERE id = ?",
					fixture.jobID,
				).Error; err != nil {
					t.Fatalf("delay job: %v", err)
				}
			case "terminal":
				agePublishedDispatch(t, fixture)
				if err := fixture.db.Exec(
					"UPDATE media_processing_jobs SET status = 'completed', completed_at = statement_timestamp() WHERE id = ?",
					fixture.jobID,
				).Error; err != nil {
					t.Fatalf("complete job: %v", err)
				}
			case "notification isolated":
				agePublishedDispatch(t, fixture)
				if err := fixture.db.Exec(`
					UPDATE media_processing_jobs
					SET status = 'failed', failed_at = statement_timestamp(),
					    last_error_code = 'MEDIA_NOTIFICATION_ISOLATED',
					    notification_isolated_at = statement_timestamp()
					WHERE id = ?`, fixture.jobID).Error; err != nil {
					t.Fatalf("isolate job: %v", err)
				}
			}

			inserted, err := reconciliationStore(fixture).ReconcileStaleDispatches(
				fixture.ctx,
				50,
				60*time.Second,
			)
			if err != nil {
				t.Fatalf("ReconcileStaleDispatches: %v", err)
			}
			if inserted != 0 {
				t.Fatalf("ineligible job produced %d reconciliation events", inserted)
			}
		})
	}
}

func TestMediaReliability_ReconciliationUsesTheLatestPublishedDispatch(t *testing.T) {
	fixture := newMediaJobFixture(t)
	agePublishedDispatch(t, fixture)
	latestID := uuid.NewString()
	if err := fixture.db.Exec(`
		INSERT INTO media_job_outbox
			(id, job_id, event_type, schema_version, payload, status, next_attempt_at, published_at)
		VALUES (?, ?, ?, 1, '{}'::jsonb, 'published', statement_timestamp(), statement_timestamp())`,
		latestID,
		fixture.jobID,
		media.EventType,
	).Error; err != nil {
		t.Fatalf("seed latest published dispatch: %v", err)
	}

	inserted, err := reconciliationStore(fixture).ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil {
		t.Fatalf("reconcile fresh latest dispatch: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("older dispatch made a fresh publication look stale: inserted=%d", inserted)
	}

	if err := fixture.db.Exec(
		"UPDATE media_job_outbox SET published_at = statement_timestamp() - interval '61 seconds' WHERE id = ?",
		latestID,
	).Error; err != nil {
		t.Fatalf("age latest dispatch: %v", err)
	}
	inserted, err = reconciliationStore(fixture).ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil || inserted != 1 {
		t.Fatalf("reconcile stale latest dispatch: inserted=%d err=%v", inserted, err)
	}
}

func TestMediaReliability_ReconciliationRecoversOnlyAnExpiredProcessingLease(t *testing.T) {
	fixture := newMediaJobFixture(t)
	agePublishedDispatch(t, fixture)
	if _, _, err := fixture.store().ClaimJob(fixture.ctx, fixture.jobID, "worker-crashed"); err != nil {
		t.Fatalf("claim job: %v", err)
	}

	inserted, err := reconciliationStore(fixture).ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil || inserted != 0 {
		t.Fatalf("live lease reconciliation: inserted=%d err=%v", inserted, err)
	}
	if err := fixture.db.Exec(
		"UPDATE media_processing_jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = ?",
		fixture.jobID,
	).Error; err != nil {
		t.Fatalf("expire job lease: %v", err)
	}

	inserted, err = reconciliationStore(fixture).ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil || inserted != 1 {
		t.Fatalf("expired lease reconciliation: inserted=%d err=%v", inserted, err)
	}
	if attempts := fixture.jobRow().AttemptCount; attempts != 1 {
		t.Fatalf("reconciliation spent a processing attempt: %d", attempts)
	}
}

func TestMediaReliability_ReconciliationHonorsTheFiftyJobBatch(t *testing.T) {
	fixture := newMediaJobFixture(t)
	fixture.seedAdditionalJobsWithOutboxRows(t, 50)
	if err := fixture.db.Exec(`
		UPDATE media_job_outbox
		SET status = 'published', published_at = statement_timestamp() - interval '61 seconds'`).Error; err != nil {
		t.Fatalf("publish stale fixture dispatches: %v", err)
	}
	store := reconciliationStore(fixture)

	inserted, err := store.ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil || inserted != 50 {
		t.Fatalf("first reconciliation batch: inserted=%d err=%v", inserted, err)
	}
	inserted, err = store.ReconcileStaleDispatches(fixture.ctx, 50, 60*time.Second)
	if err != nil || inserted != 1 {
		t.Fatalf("second reconciliation batch: inserted=%d err=%v", inserted, err)
	}
	var pending int64
	if err := fixture.db.Model(&domain.MediaJobOutboxRecord{}).
		Where("status = 'pending'").
		Count(&pending).Error; err != nil {
		t.Fatalf("count reconciliation events: %v", err)
	}
	if pending != 51 {
		t.Fatalf("pending reconciliation events = %d, want 51", pending)
	}
}

func TestMediaReliability_ConcurrentReconcilersConvergeOnOneUnpublishedEvent(t *testing.T) {
	fixture := newMediaJobFixture(t)
	agePublishedDispatch(t, fixture)
	var folderID string
	if err := fixture.db.Raw("SELECT folder_id FROM metadata_items WHERE id = ?", fixture.assetID).Scan(&folderID).Error; err != nil {
		t.Fatalf("load fixture folder: %v", err)
	}
	if err := fixture.db.Commit().Error; err != nil {
		t.Fatalf("commit concurrent reconciliation fixture: %v", err)
	}

	database, err := gorm.Open(postgres.Open(os.Getenv("ASSET_TEST_DATABASE_URL")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open concurrent reconciliation database: %v", err)
	}
	t.Cleanup(func() {
		cleanup := []struct {
			query string
			value string
		}{
			{"DELETE FROM media_job_outbox WHERE job_id = ?", fixture.jobID},
			{"DELETE FROM media_processing_jobs WHERE id = ?", fixture.jobID},
			{"DELETE FROM asset_media_versions WHERE id = ?", fixture.versionID},
			{"DELETE FROM media_upload_sessions WHERE id = ?", fixture.uploadID},
			{"DELETE FROM metadata_items WHERE id = ?", fixture.assetID},
			{"DELETE FROM folders WHERE id = ?", folderID},
			{"DELETE FROM user_ref WHERE user_id = ?", fixture.userID},
			{"DELETE FROM organization_ref WHERE org_id = ?", fixture.orgID},
		}
		for _, statement := range cleanup {
			if err := database.Exec(statement.query, statement.value).Error; err != nil {
				t.Errorf("cleanup concurrent reconciliation fixture: %v", err)
			}
		}
	})

	stores := []interface {
		ReconcileStaleDispatches(context.Context, int, time.Duration) (int, error)
	}{
		repository.NewMediaJobStore(database, domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry}, testRetryPolicy()),
		repository.NewMediaJobStore(database, domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry}, testRetryPolicy()),
	}
	start := make(chan struct{})
	type result struct {
		inserted int
		err      error
	}
	results := make(chan result, len(stores))
	var workers sync.WaitGroup
	for _, store := range stores {
		workers.Add(1)
		go func(store interface {
			ReconcileStaleDispatches(context.Context, int, time.Duration) (int, error)
		}) {
			defer workers.Done()
			<-start
			inserted, err := store.ReconcileStaleDispatches(context.Background(), 50, 60*time.Second)
			results <- result{inserted: inserted, err: err}
		}(store)
	}
	close(start)
	workers.Wait()
	close(results)

	totalInserted := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent reconciliation: %v", result.err)
		}
		totalInserted += result.inserted
	}
	if totalInserted != 1 {
		t.Fatalf("concurrent reconcilers inserted %d events, want 1", totalInserted)
	}
	var pending int64
	if err := database.Model(&domain.MediaJobOutboxRecord{}).
		Where("job_id = ? AND status = 'pending'", fixture.jobID).
		Count(&pending).Error; err != nil {
		t.Fatalf("count converged reconciliation event: %v", err)
	}
	if pending != 1 {
		t.Fatalf("pending events after concurrent reconciliation = %d, want 1", pending)
	}
}

func agePublishedDispatch(t *testing.T, fixture *mediaJobFixture) {
	t.Helper()
	fixture.publishPendingOutbox(t)
	if err := fixture.db.Exec(
		"UPDATE media_job_outbox SET published_at = statement_timestamp() - interval '61 seconds' WHERE id = ?",
		fixture.outboxID,
	).Error; err != nil {
		t.Fatalf("age published dispatch: %v", err)
	}
}

func reconciliationStore(fixture *mediaJobFixture) interface {
	ReconcileStaleDispatches(context.Context, int, time.Duration) (int, error)
} {
	return repository.NewMediaJobStore(
		fixture.db,
		domain.MediaLeasePolicy{RenewalInterval: testJobLeaseRenewal, Expiry: testJobLeaseExpiry},
		testRetryPolicy(),
	)
}
