package repository_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

type mediaFixedClock struct{ at time.Time }

func (clock mediaFixedClock) Now() time.Time { return clock.at }

type mediaRepositoryContract interface {
	CreateUploadSession(context.Context, domain.CreateUploadSessionRequest, repository.CreateUploadSessionOptions) (domain.MediaUploadSession, bool, error)
	GetUploadSession(context.Context, repository.UploadSessionScope) (domain.MediaUploadSession, error)
	RefreshUploadSession(context.Context, repository.UploadSessionScope, time.Time) (domain.MediaUploadSession, error)
	CancelUploadSession(context.Context, repository.UploadSessionScope) error
	ExpireUploadSession(context.Context, repository.UploadSessionScope) error
	CommitUpload(context.Context, domain.CommitUploadRequest, domain.ObjectAttributes, repository.CommitUploadIDs) (domain.CommitAcceptance, bool, error)
}

type mediaRepositoryFixture struct {
	t      *testing.T
	db     *gorm.DB
	repo   mediaRepositoryContract
	ctx    context.Context
	now    time.Time
	orgID  string
	userID string
	assets []string
}

func newMediaRepositoryFixture(t *testing.T) *mediaRepositoryFixture {
	t.Helper()
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	fixture := &mediaRepositoryFixture{
		t:      t,
		db:     tx,
		ctx:    context.Background(),
		now:    time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC),
		orgID:  uuid.NewString(),
		userID: uuid.NewString(),
	}
	fixture.repo = repository.NewMediaRepository(tx, mediaFixedClock{at: fixture.now})
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", fixture.orgID).Error; err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", fixture.userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	for index := 0; index < 3; index++ {
		folderID := uuid.NewString()
		assetID := uuid.NewString()
		if err := tx.Exec(
			"INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
			folderID, fixture.orgID, strings.ReplaceAll(folderID, "-", ""), "Media Integration "+string(rune('A'+index)), fixture.userID,
		).Error; err != nil {
			t.Fatalf("seed folder: %v", err)
		}
		if err := tx.Exec(
			"INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES (?, ?, ?, ?)",
			assetID, folderID, "Media asset", fixture.userID,
		).Error; err != nil {
			t.Fatalf("seed asset: %v", err)
		}
		fixture.assets = append(fixture.assets, assetID)
	}
	return fixture
}

func (fixture *mediaRepositoryFixture) request(assetID, retryKey string, size int64) domain.CreateUploadSessionRequest {
	fixture.t.Helper()
	return domain.CreateUploadSessionRequest{
		OrgID:                  fixture.orgID,
		AssetID:                assetID,
		RequestedBy:            fixture.userID,
		IdempotencyKey:         retryKey,
		OriginalFilename:       "photo.png",
		DeclaredContentType:    domain.MediaContentTypePNG,
		ExpectedSizeBytes:      size,
		DeclaredChecksumSHA256: bytes.Repeat([]byte{0x2a}, domain.ChecksumByteLength),
	}
}

func (fixture *mediaRepositoryFixture) reserve(request domain.CreateUploadSessionRequest, uploadID string, quota int64) (domain.MediaUploadSession, bool, error) {
	fixture.t.Helper()
	key, err := domain.RawObjectKey(request.OrgID, request.AssetID, uploadID, request.DeclaredContentType)
	if err != nil {
		fixture.t.Fatalf("derive raw key: %v", err)
	}
	return fixture.repo.CreateUploadSession(fixture.ctx, request, repository.CreateUploadSessionOptions{
		UploadID:            uploadID,
		RawObjectKey:        key,
		DefaultQuotaBytes:   quota,
		CredentialExpiresAt: fixture.now.Add(time.Hour),
		SessionExpiresAt:    fixture.now.Add(24 * time.Hour),
	})
}

func TestMediaRepository_ReservesQuotaBeforeCreatingUploadAuthorityState(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	firstRequest := fixture.request(fixture.assets[0], uuid.NewString(), 6)
	first, replayed, err := fixture.reserve(firstRequest, uuid.NewString(), 10)
	if err != nil || replayed {
		t.Fatalf("reserve first session: replayed=%v err=%v", replayed, err)
	}
	_, _, err = fixture.reserve(fixture.request(fixture.assets[1], uuid.NewString(), 5), uuid.NewString(), 10)
	if !errors.Is(err, repository.ErrQuotaExceeded) {
		t.Fatalf("expected quota rejection before a second session, got %v", err)
	}

	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		t.Fatalf("read quota ledger: %v", err)
	}
	if usage.ReservedRawBytes != 6 || usage.StoredRawBytes != 0 {
		t.Fatalf("rejected admission changed quota: %#v", usage)
	}
	var sessions int64
	if err := fixture.db.Model(&domain.MediaUploadSession{}).Where("org_id = ?", fixture.orgID).Count(&sessions).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 || first.State != domain.UploadSessionCreated {
		t.Fatalf("expected only original created session, count=%d state=%q", sessions, first.State)
	}
}

func TestMediaRepository_RaisingQuotaAdmitsThePreviouslyRejectedDeclaration(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	firstRequest := fixture.request(fixture.assets[0], uuid.NewString(), 6)
	if _, _, err := fixture.reserve(firstRequest, uuid.NewString(), 10); err != nil {
		t.Fatalf("reserve first session: %v", err)
	}
	secondRequest := fixture.request(fixture.assets[1], uuid.NewString(), 5)
	secondUploadID := uuid.NewString()
	if _, _, err := fixture.reserve(secondRequest, secondUploadID, 10); !errors.Is(err, repository.ErrQuotaExceeded) {
		t.Fatalf("initial admission error = %v, want quota rejection", err)
	}

	if err := fixture.db.Exec(
		"UPDATE organization_media_usage SET raw_quota_bytes = 11 WHERE org_id = ?",
		fixture.orgID,
	).Error; err != nil {
		t.Fatalf("raise raw quota: %v", err)
	}
	admitted, replayed, err := fixture.reserve(secondRequest, secondUploadID, 10)
	if err != nil || replayed {
		t.Fatalf("admission after supported quota remedy: replayed=%v err=%v", replayed, err)
	}
	if admitted.ID != secondUploadID || admitted.State != domain.UploadSessionCreated {
		t.Fatalf("admitted session = %#v", admitted)
	}
	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		t.Fatalf("read quota ledger: %v", err)
	}
	if usage.RawQuotaBytes != 11 || usage.ReservedRawBytes != 11 || usage.StoredRawBytes != 0 {
		t.Fatalf("quota after remedy = %#v", usage)
	}
}

func TestMediaRepository_OneOpenSessionPerAssetAndScopedRetryIsImmutable(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	retryKey := uuid.NewString()
	request := fixture.request(fixture.assets[0], retryKey, 7)
	uploadID := uuid.NewString()
	original, replayed, err := fixture.reserve(request, uploadID, 100)
	if err != nil || replayed {
		t.Fatalf("create original session: replayed=%v err=%v", replayed, err)
	}

	replayedSession, replayed, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil || !replayed || replayedSession.ID != original.ID {
		t.Fatalf("expected original scoped replay, session=%s replayed=%v err=%v", replayedSession.ID, replayed, err)
	}
	changed := request
	changed.ExpectedSizeBytes++
	if _, _, err := fixture.reserve(changed, uuid.NewString(), 100); !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("expected changed retry metadata conflict, got %v", err)
	}
	if _, _, err := fixture.reserve(fixture.request(fixture.assets[0], uuid.NewString(), 7), uuid.NewString(), 100); !errors.Is(err, repository.ErrUploadInProgress) {
		t.Fatalf("expected one-open-session conflict, got %v", err)
	}

	stored, err := fixture.repo.GetUploadSession(fixture.ctx, repository.UploadSessionScope{
		OrgID: fixture.orgID, AssetID: fixture.assets[0], UploadID: uploadID, RequestedBy: fixture.userID,
	})
	if err != nil {
		t.Fatalf("read original session: %v", err)
	}
	if stored.ExpectedSizeBytes != 7 || stored.OriginalFilename != "photo.png" {
		t.Fatalf("conflicting retry altered original session: %#v", stored)
	}
}

func TestMediaRepository_IdempotencyReplayIsRequesterScoped(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	secondUserID := uuid.NewString()
	if err := fixture.db.Exec(
		"INSERT INTO user_ref (user_id) VALUES (?)",
		secondUserID,
	).Error; err != nil {
		t.Fatalf("seed second requester: %v", err)
	}

	retryKey := uuid.NewString()
	firstRequest := fixture.request(fixture.assets[0], retryKey, 7)
	firstSession, replayed, err := fixture.reserve(firstRequest, uuid.NewString(), 100)
	if err != nil || replayed {
		t.Fatalf("create first requester session: replayed=%v err=%v", replayed, err)
	}

	secondRequest := firstRequest
	secondRequest.RequestedBy = secondUserID
	replayedSession, replayed, err := fixture.reserve(
		secondRequest,
		uuid.NewString(),
		100,
	)
	if !errors.Is(err, repository.ErrUploadInProgress) {
		t.Fatalf("second requester error = %v, want upload in progress", err)
	}
	if replayed || replayedSession.ID != "" {
		t.Fatalf(
			"second requester received first session: session=%q replayed=%v",
			replayedSession.ID,
			replayed,
		)
	}

	if err := fixture.repo.CancelUploadSession(fixture.ctx, repository.UploadSessionScope{
		OrgID:       fixture.orgID,
		AssetID:     firstRequest.AssetID,
		UploadID:    firstSession.ID,
		RequestedBy: firstRequest.RequestedBy,
	}); err != nil {
		t.Fatalf("cancel first requester session: %v", err)
	}

	secondSession, replayed, err := fixture.reserve(
		secondRequest,
		uuid.NewString(),
		100,
	)
	if err != nil || replayed || secondSession.RequestedBy != secondUserID {
		t.Fatalf(
			"create second requester session: session=%#v replayed=%v err=%v",
			secondSession,
			replayed,
			err,
		)
	}
}

func TestMediaRepository_GetUploadSessionRequiresActiveAssetAndFolder(t *testing.T) {
	for _, target := range []string{"asset", "folder"} {
		t.Run(target, func(t *testing.T) {
			fixture := newMediaRepositoryFixture(t)
			request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
			session, _, err := fixture.reserve(request, uuid.NewString(), 100)
			if err != nil {
				t.Fatalf("reserve session: %v", err)
			}
			if target == "asset" {
				err = fixture.db.Exec("UPDATE metadata_items SET deleted_at = ? WHERE id = ?", fixture.now, request.AssetID).Error
			} else {
				err = fixture.db.Exec(`UPDATE folders SET deleted_at = ? WHERE id = (
					SELECT folder_id FROM metadata_items WHERE id = ?
				)`, fixture.now, request.AssetID).Error
			}
			if err != nil {
				t.Fatalf("delete %s: %v", target, err)
			}

			_, err = fixture.repo.GetUploadSession(fixture.ctx, repository.UploadSessionScope{
				OrgID: fixture.orgID, AssetID: request.AssetID, UploadID: session.ID, RequestedBy: fixture.userID,
			})
			if !errors.Is(err, repository.ErrAssetNotAvailable) {
				t.Fatalf("deleted %s session read error = %v", target, err)
			}
		})
	}
}

func TestMediaRepository_CancelReleasesReservationExactlyOnceAndFreesAsset(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	scope := repository.UploadSessionScope{
		OrgID: fixture.orgID, AssetID: request.AssetID, UploadID: session.ID, RequestedBy: fixture.userID,
	}

	if err := fixture.repo.CancelUploadSession(fixture.ctx, scope); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := fixture.repo.CancelUploadSession(fixture.ctx, scope); err != nil {
		t.Fatalf("repeated cancel: %v", err)
	}

	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.ReservedRawBytes != 0 || usage.StoredRawBytes != 0 {
		t.Fatalf("idempotent cancel usage = %#v", usage)
	}
	stored, err := fixture.repo.GetUploadSession(fixture.ctx, scope)
	if err != nil {
		t.Fatalf("read cancelled session: %v", err)
	}
	if stored.State != domain.UploadSessionCancelled || stored.CancelledAt == nil {
		t.Fatalf("cancelled session = %#v", stored)
	}
	if _, _, err := fixture.reserve(fixture.request(request.AssetID, uuid.NewString(), 7), uuid.NewString(), 100); err != nil {
		t.Fatalf("cancel did not free one-open-session slot: %v", err)
	}
}

func TestMediaRepository_CancelledSessionRetiresItsIdempotencyKey(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	scope := repository.UploadSessionScope{
		OrgID: fixture.orgID, AssetID: request.AssetID, UploadID: session.ID, RequestedBy: fixture.userID,
	}
	if err := fixture.repo.CancelUploadSession(fixture.ctx, scope); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	replayed, _, err := fixture.reserve(request, uuid.NewString(), 100)

	if !errors.Is(err, repository.ErrIdempotencyConflict) {
		t.Fatalf("cancelled session replayed as %#v (err %v)", replayed, err)
	}
	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.ReservedRawBytes != 0 {
		t.Fatalf("rejected replay reserved %d bytes", usage.ReservedRawBytes)
	}
}

func TestMediaRepository_CancelCommittedConflictsAndScopedProbesAreSafeNotFound(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	scope := repository.UploadSessionScope{
		OrgID: fixture.orgID, AssetID: request.AssetID, UploadID: session.ID, RequestedBy: fixture.userID,
	}
	_, _, err = fixture.repo.CommitUpload(fixture.ctx, domain.CommitUploadRequest{
		OrgID: scope.OrgID, AssetID: scope.AssetID, UploadID: scope.UploadID, RequestedBy: scope.RequestedBy,
	}, domain.ObjectAttributes{SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: request.DeclaredChecksumSHA256}, repository.CommitUploadIDs{
		VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString(),
	})
	if err != nil {
		t.Fatalf("commit session: %v", err)
	}
	if err := fixture.repo.CancelUploadSession(fixture.ctx, scope); !errors.Is(err, repository.ErrUploadStateConflict) {
		t.Fatalf("cancel committed error = %v", err)
	}

	for name, changed := range map[string]repository.UploadSessionScope{
		"organization": {OrgID: uuid.NewString(), AssetID: scope.AssetID, UploadID: scope.UploadID, RequestedBy: scope.RequestedBy},
		"asset":        {OrgID: scope.OrgID, AssetID: fixture.assets[1], UploadID: scope.UploadID, RequestedBy: scope.RequestedBy},
		"requester":    {OrgID: scope.OrgID, AssetID: scope.AssetID, UploadID: scope.UploadID, RequestedBy: uuid.NewString()},
	} {
		t.Run(name, func(t *testing.T) {
			if err := fixture.repo.CancelUploadSession(fixture.ctx, changed); !errors.Is(err, repository.ErrUploadNotFound) && !errors.Is(err, repository.ErrAssetNotAvailable) {
				t.Fatalf("scoped cancel error = %v, want safe missing", err)
			}
		})
	}
}

func TestMediaRepository_CancelVersusExpiryConvergesWithoutDoubleRelease(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	orgID, userID, folderID, assetID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if err := database.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if err := database.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := database.Exec(
		"INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		folderID, orgID, strings.ReplaceAll(folderID, "-", ""), "Media race", userID,
	).Error; err != nil {
		t.Fatalf("seed folder: %v", err)
	}
	if err := database.Exec(
		"INSERT INTO metadata_items (id, folder_id, title, created_by) VALUES (?, ?, ?, ?)",
		assetID, folderID, "Media race asset", userID,
	).Error; err != nil {
		t.Fatalf("seed asset: %v", err)
	}
	t.Cleanup(func() {
		for _, cleanup := range []struct {
			query string
			value string
		}{
			{query: "DELETE FROM media_upload_sessions WHERE org_id = ?", value: orgID},
			{query: "DELETE FROM organization_media_usage WHERE org_id = ?", value: orgID},
			{query: "DELETE FROM metadata_items WHERE id = ?", value: assetID},
			{query: "DELETE FROM folders WHERE id = ?", value: folderID},
			{query: "DELETE FROM user_ref WHERE user_id = ?", value: userID},
			{query: "DELETE FROM organization_ref WHERE org_id = ?", value: orgID},
		} {
			if err := database.Exec(cleanup.query, cleanup.value).Error; err != nil {
				t.Errorf("cleanup %q: %v", cleanup.query, err)
			}
		}
	})

	repo := repository.NewMediaRepository(database, mediaFixedClock{at: now})
	request := domain.CreateUploadSessionRequest{
		OrgID: orgID, AssetID: assetID, RequestedBy: userID, IdempotencyKey: uuid.NewString(),
		OriginalFilename: "photo.png", DeclaredContentType: domain.MediaContentTypePNG,
		ExpectedSizeBytes: 7, DeclaredChecksumSHA256: bytes.Repeat([]byte{0x2a}, domain.ChecksumByteLength),
	}
	uploadID := uuid.NewString()
	key, _ := domain.RawObjectKey(orgID, assetID, uploadID, domain.MediaContentTypePNG)
	session, _, err := repo.CreateUploadSession(context.Background(), request, repository.CreateUploadSessionOptions{
		UploadID: uploadID, RawObjectKey: key, DefaultQuotaBytes: 100,
		CredentialExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	if err := database.Model(&domain.MediaUploadSession{}).Where("id = ?", session.ID).
		Update("session_expires_at", now.Add(-time.Second)).Error; err != nil {
		t.Fatalf("make session due for expiry: %v", err)
	}
	scope := repository.UploadSessionScope{
		OrgID: orgID, AssetID: assetID, UploadID: session.ID, RequestedBy: userID,
	}

	errs := make(chan error, 2)
	go func() { errs <- repo.CancelUploadSession(context.Background(), scope) }()
	go func() { errs <- repo.ExpireUploadSession(context.Background(), scope) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent terminal transition: %v", err)
		}
	}

	var usage domain.OrganizationMediaUsage
	if err := database.Take(&usage, "org_id = ?", orgID).Error; err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if usage.ReservedRawBytes != 0 || usage.StoredRawBytes != 0 {
		t.Fatalf("cancel/expiry released reservation more than once: %#v", usage)
	}
	var terminal domain.MediaUploadSession
	if err := database.Take(&terminal, "id = ?", session.ID).Error; err != nil {
		t.Fatalf("read terminal session: %v", err)
	}
	if terminal.State != domain.UploadSessionCancelled && terminal.State != domain.UploadSessionExpired {
		t.Fatalf("terminal state = %q", terminal.State)
	}
}

func TestMediaRepository_CommitRejectsUnverifiedObjectBeforeAssetTransaction(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	wrongChecksum := bytes.Repeat([]byte{0x99}, domain.ChecksumByteLength)
	_, _, err = fixture.repo.CommitUpload(fixture.ctx, domain.CommitUploadRequest{
		OrgID: fixture.orgID, AssetID: fixture.assets[0], RequestedBy: fixture.userID, UploadID: session.ID,
	}, domain.ObjectAttributes{SizeBytes: 8, ContentType: "image/jpeg", ChecksumSHA256: wrongChecksum}, repository.CommitUploadIDs{
		VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString(),
	})
	if !errors.Is(err, repository.ErrMediaObjectMismatch) {
		t.Fatalf("expected object mismatch before commit, got %v", err)
	}

	var versions int64
	if err := fixture.db.Model(&domain.AssetMediaVersion{}).Where("upload_id = ?", session.ID).Count(&versions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 0 {
		t.Fatalf("unverified object created %d versions", versions)
	}
}

func TestMediaRepository_CommitRejectsMissingVerifiedChecksumBeforeAssetTransaction(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}

	_, _, err = fixture.repo.CommitUpload(
		fixture.ctx,
		domain.CommitUploadRequest{
			OrgID: fixture.orgID, AssetID: request.AssetID,
			RequestedBy: fixture.userID, UploadID: session.ID,
		},
		domain.ObjectAttributes{
			SizeBytes: 7, ContentType: "image/png",
		},
		repository.CommitUploadIDs{
			VersionID: uuid.NewString(),
			JobID:     uuid.NewString(),
			OutboxID:  uuid.NewString(),
		},
	)
	if !errors.Is(err, repository.ErrMediaObjectMismatch) {
		t.Fatalf("missing verified checksum error = %v, want media object mismatch", err)
	}

	var versions int64
	if err := fixture.db.Model(&domain.AssetMediaVersion{}).
		Where("upload_id = ?", session.ID).
		Count(&versions).Error; err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versions != 0 {
		t.Fatalf("missing verified checksum created %d versions", versions)
	}
}

func TestMediaRepository_VersionJobPointerQuotaAndOutboxCommitAtomically(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	ids := repository.CommitUploadIDs{VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString()}
	const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	commitContext := requestcontext.WithCorrelation(
		fixture.ctx,
		&requestcontext.Correlation{TraceID: traceID},
	)
	acceptance, replayed, err := fixture.repo.CommitUpload(commitContext, domain.CommitUploadRequest{
		OrgID: fixture.orgID, AssetID: fixture.assets[0], RequestedBy: fixture.userID, UploadID: session.ID,
	}, domain.ObjectAttributes{
		SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: request.DeclaredChecksumSHA256,
	}, ids)
	if err != nil || replayed {
		t.Fatalf("commit verified upload: replayed=%v err=%v", replayed, err)
	}
	if acceptance.VersionID != ids.VersionID || acceptance.JobID != ids.JobID || acceptance.Status != domain.ProcessingJobQueued {
		t.Fatalf("unexpected durable acceptance: %#v", acceptance)
	}

	var counts struct{ Versions, Jobs, Outbox, Outputs int64 }
	fixture.db.Model(&domain.AssetMediaVersion{}).Where("id = ?", ids.VersionID).Count(&counts.Versions)
	fixture.db.Model(&domain.MediaProcessingJob{}).Where("id = ? AND status = 'queued'", ids.JobID).Count(&counts.Jobs)
	fixture.db.Model(&domain.MediaJobOutboxRecord{}).Where("id = ? AND status = 'pending' AND published_at IS NULL", ids.OutboxID).Count(&counts.Outbox)
	fixture.db.Model(&domain.MediaOutput{}).Where("version_id = ?", ids.VersionID).Count(&counts.Outputs)
	if counts.Versions != 1 || counts.Jobs != 1 || counts.Outbox != 1 || counts.Outputs != 0 {
		t.Fatalf("202 must mean durable queued acceptance, not publication or rendition: %#v", counts)
	}
	var outbox domain.MediaJobOutboxRecord
	if err := fixture.db.Take(&outbox, "id = ?", ids.OutboxID).Error; err != nil {
		t.Fatalf("read committed outbox envelope: %v", err)
	}
	var envelope struct {
		Traceparent string `json:"traceparent"`
	}
	if err := json.Unmarshal(outbox.Payload, &envelope); err != nil {
		t.Fatalf("decode committed outbox envelope: %v", err)
	}
	gotTraceID, valid := requestcontext.ParseTraceparent(envelope.Traceparent)
	if !valid || gotTraceID != traceID {
		t.Fatalf("outbox traceparent = %q, want trace ID %q", envelope.Traceparent, traceID)
	}
	var pointer *string
	if err := fixture.db.Raw("SELECT pending_media_version_id FROM metadata_items WHERE id = ?", fixture.assets[0]).Scan(&pointer).Error; err != nil {
		t.Fatalf("read pending pointer: %v", err)
	}
	if pointer == nil || *pointer != ids.VersionID {
		t.Fatalf("pending pointer does not reference committed version: %#v", pointer)
	}
	var usage domain.OrganizationMediaUsage
	if err := fixture.db.Take(&usage, "org_id = ?", fixture.orgID).Error; err != nil {
		t.Fatalf("read quota after commit: %v", err)
	}
	if usage.ReservedRawBytes != 0 || usage.StoredRawBytes != 7 {
		t.Fatalf("commit did not atomically convert quota: %#v", usage)
	}
}

func TestMediaRepository_SourceDimensionsRequireNullParity(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	ids := repository.CommitUploadIDs{VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString()}
	if _, _, err := fixture.repo.CommitUpload(fixture.ctx, domain.CommitUploadRequest{
		OrgID: fixture.orgID, AssetID: request.AssetID, RequestedBy: fixture.userID, UploadID: session.ID,
	}, domain.ObjectAttributes{
		SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: request.DeclaredChecksumSHA256,
	}, ids); err != nil {
		t.Fatalf("commit verified upload: %v", err)
	}

	if err := fixture.db.Exec("SAVEPOINT dimension_null_parity").Error; err != nil {
		t.Fatalf("create constraint savepoint: %v", err)
	}
	partial := fixture.db.Exec(
		"UPDATE asset_media_versions SET source_width = 100, source_height = NULL WHERE id = ?",
		ids.VersionID,
	)
	if partial.Error == nil {
		t.Fatal("dimension constraint accepted width without height")
	}
	if err := fixture.db.Exec("ROLLBACK TO SAVEPOINT dimension_null_parity").Error; err != nil {
		t.Fatalf("recover after expected constraint rejection: %v", err)
	}
	if err := fixture.db.Exec(
		"UPDATE asset_media_versions SET source_width = 100, source_height = 100 WHERE id = ?",
		ids.VersionID,
	).Error; err != nil {
		t.Fatalf("dimension constraint rejected complete positive pair: %v", err)
	}
}

func TestMediaRepository_OutboxFailureRollsBackEntireAcceptance(t *testing.T) {
	fixture := newMediaRepositoryFixture(t)
	request := fixture.request(fixture.assets[0], uuid.NewString(), 7)
	session, _, err := fixture.reserve(request, uuid.NewString(), 100)
	if err != nil {
		t.Fatalf("reserve session: %v", err)
	}
	if err := fixture.db.Exec(`
		CREATE FUNCTION reject_test_media_outbox() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'injected outbox failure'; END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER reject_test_media_outbox BEFORE INSERT ON media_job_outbox
		FOR EACH ROW EXECUTE FUNCTION reject_test_media_outbox();`).Error; err != nil {
		t.Fatalf("install rollback probe: %v", err)
	}
	ids := repository.CommitUploadIDs{VersionID: uuid.NewString(), JobID: uuid.NewString(), OutboxID: uuid.NewString()}
	_, _, err = fixture.repo.CommitUpload(fixture.ctx, domain.CommitUploadRequest{
		OrgID: fixture.orgID, AssetID: fixture.assets[0], RequestedBy: fixture.userID, UploadID: session.ID,
	}, domain.ObjectAttributes{SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: request.DeclaredChecksumSHA256}, ids)
	if err == nil {
		t.Fatal("expected injected outbox failure")
	}

	for name, model := range map[string]any{
		"version": &domain.AssetMediaVersion{}, "job": &domain.MediaProcessingJob{}, "outbox": &domain.MediaJobOutboxRecord{},
	} {
		var count int64
		if err := fixture.db.Model(model).Where("id IN ?", []string{ids.VersionID, ids.JobID, ids.OutboxID}).Count(&count).Error; err != nil {
			t.Fatalf("count %s rollback state: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s survived failed atomic commit", name)
		}
	}
	stored, err := fixture.repo.GetUploadSession(fixture.ctx, repository.UploadSessionScope{
		OrgID: fixture.orgID, AssetID: fixture.assets[0], UploadID: session.ID, RequestedBy: fixture.userID,
	})
	if err != nil || stored.State != domain.UploadSessionCreated {
		t.Fatalf("failed acceptance altered session: state=%q err=%v", stored.State, err)
	}
}
