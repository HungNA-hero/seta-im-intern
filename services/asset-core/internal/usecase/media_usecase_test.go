package usecase_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type mediaUsecaseClock struct{ at time.Time }

func (clock mediaUsecaseClock) Now() time.Time { return clock.at }

type sequenceIDs struct {
	values []string
	index  int
}

func (ids *sequenceIDs) NewID() string {
	value := ids.values[ids.index]
	ids.index++
	return value
}

type mediaRepositoryFake struct {
	createSession  domain.MediaUploadSession
	createReplay   bool
	createErr      error
	getSession     domain.MediaUploadSession
	getErr         error
	acceptance     domain.CommitAcceptance
	commitReplay   bool
	commitErr      error
	createCalls    int
	commitCalls    int
	createOptions  repository.CreateUploadSessionOptions
	createRequest  domain.CreateUploadSessionRequest
	commitAttrs    domain.ObjectAttributes
	commitIDs      repository.CommitUploadIDs
	refreshSession domain.MediaUploadSession
	refreshErr     error
	refreshCalls   int
	refreshScope   repository.UploadSessionScope
	refreshExpiry  time.Time
	cancelErr      error
	cancelCalls    int
	cancelScope    repository.UploadSessionScope
	statusRecord   repository.MediaStatusRecord
	statusErr      error
	statusScope    repository.MediaStatusScope
	statusCalls    int
}

func (fake *mediaRepositoryFake) GetLatestMediaStatus(_ context.Context, scope repository.MediaStatusScope) (repository.MediaStatusRecord, error) {
	fake.statusCalls++
	fake.statusScope = scope
	return fake.statusRecord, fake.statusErr
}

func (fake *mediaRepositoryFake) CreateUploadSession(_ context.Context, request domain.CreateUploadSessionRequest, options repository.CreateUploadSessionOptions) (domain.MediaUploadSession, bool, error) {
	fake.createCalls++
	fake.createOptions = options
	fake.createRequest = request
	return fake.createSession, fake.createReplay, fake.createErr
}

func (fake *mediaRepositoryFake) GetUploadSession(_ context.Context, _ repository.UploadSessionScope) (domain.MediaUploadSession, error) {
	return fake.getSession, fake.getErr
}

func (fake *mediaRepositoryFake) CommitUpload(_ context.Context, _ domain.CommitUploadRequest, attributes domain.ObjectAttributes, ids repository.CommitUploadIDs) (domain.CommitAcceptance, bool, error) {
	fake.commitCalls++
	fake.commitAttrs = attributes
	fake.commitIDs = ids
	return fake.acceptance, fake.commitReplay, fake.commitErr
}

func (fake *mediaRepositoryFake) RefreshUploadSession(_ context.Context, scope repository.UploadSessionScope, credentialExpiresAt time.Time) (domain.MediaUploadSession, error) {
	fake.refreshCalls++
	fake.refreshScope = scope
	fake.refreshExpiry = credentialExpiresAt
	return fake.refreshSession, fake.refreshErr
}

func (fake *mediaRepositoryFake) CancelUploadSession(_ context.Context, scope repository.UploadSessionScope) error {
	fake.cancelCalls++
	fake.cancelScope = scope
	return fake.cancelErr
}

type mediaStorageFake struct {
	presignResult   domain.UploadDescriptor
	presignErr      error
	headResult      domain.ObjectAttributes
	headErr         error
	getBody         []byte
	getErr          error
	presignCalls    int
	headCalls       int
	getCalls        int
	presignInput    domain.PutAuthority
	headKey         domain.ObjectKey
	getKey          domain.ObjectKey
	presignGetCalls []struct {
		Key domain.ObjectKey
		TTL time.Duration
	}
	presignGetNow time.Time
}

func (fake *mediaStorageFake) PresignPut(_ context.Context, input domain.PutAuthority) (domain.UploadDescriptor, error) {
	fake.presignCalls++
	fake.presignInput = input
	return fake.presignResult, fake.presignErr
}
func (fake *mediaStorageFake) PresignGet(_ context.Context, key domain.ObjectKey, ttl time.Duration) (domain.DownloadDescriptor, error) {
	fake.presignGetCalls = append(fake.presignGetCalls, struct {
		Key domain.ObjectKey
		TTL time.Duration
	}{Key: key, TTL: ttl})
	expiresAt := fake.presignGetNow.Add(ttl)
	return domain.DownloadDescriptor{
		URL:       "https://media.example.test/" + key.String() + "?expires=" + strconv.FormatInt(expiresAt.Unix(), 10),
		ExpiresAt: expiresAt,
	}, nil
}
func (fake *mediaStorageFake) Head(_ context.Context, key domain.ObjectKey) (domain.ObjectAttributes, error) {
	fake.headCalls++
	fake.headKey = key
	return fake.headResult, fake.headErr
}
func (*mediaStorageFake) Put(context.Context, domain.ObjectKey, io.Reader, domain.PutAttributes) error {
	panic("not used")
}
func (fake *mediaStorageFake) Get(_ context.Context, key domain.ObjectKey) (io.ReadCloser, error) {
	fake.getCalls++
	fake.getKey = key
	if fake.getErr != nil {
		return nil, fake.getErr
	}
	return io.NopCloser(bytes.NewReader(fake.getBody)), nil
}
func (*mediaStorageFake) Delete(context.Context, domain.ObjectKey) error { panic("not used") }

func mediaPolicy() domain.MediaConfig {
	return domain.MediaConfig{Limits: domain.MediaLimits{
		MinUploadSizeBytes: 1, MaxUploadSizeBytes: 50_000_000,
		CredentialTTL: time.Hour, SessionTTL: 24 * time.Hour, DefaultRawQuotaBytes: 100,
	}}
}

func validCreateRequest() domain.CreateUploadSessionRequest {
	return domain.CreateUploadSessionRequest{
		OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1", IdempotencyKey: "retry-1",
		OriginalFilename: "photo.png", DeclaredContentType: domain.MediaContentTypePNG,
		ExpectedSizeBytes: 7, DeclaredChecksumSHA256: bytes.Repeat([]byte{0x2a}, 32),
	}
}

func TestMediaUsecase_QuotaFailureIssuesNoUploadAuthority(t *testing.T) {
	repositoryFake := &mediaRepositoryFake{createErr: repository.ErrQuotaExceeded}
	storageFake := &mediaStorageFake{}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())

	_, err := service.CreateUploadSession(context.Background(), validCreateRequest())

	if !errors.Is(err, repository.ErrQuotaExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}
	if storageFake.presignCalls != 0 {
		t.Fatalf("quota rejection issued %d upload authorities", storageFake.presignCalls)
	}
}

func TestMediaUsecase_CreateBindsPresignToReservedSessionIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	request := validCreateRequest()
	rawKey, _ := domain.RawObjectKey(request.OrgID, request.AssetID, "upload-1", request.DeclaredContentType)
	repositoryFake := &mediaRepositoryFake{createSession: domain.MediaUploadSession{
		ID: "upload-1", OrgID: request.OrgID, AssetID: request.AssetID, RequestedBy: request.RequestedBy,
		State: domain.UploadSessionCreated, RawObjectKey: rawKey.String(), DeclaredContentType: request.DeclaredContentType,
		ExpectedSizeBytes: request.ExpectedSizeBytes, DeclaredChecksumSHA256: request.DeclaredChecksumSHA256,
		CredentialExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(24 * time.Hour),
	}}
	storageFake := &mediaStorageFake{presignResult: domain.UploadDescriptor{Method: "PUT", URL: "https://exact", Headers: map[string]string{"If-None-Match": "*"}}}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: now}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())

	result, err := service.CreateUploadSession(context.Background(), request)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if result.Session.ID != "upload-1" || result.Descriptor.URL != "https://exact" {
		t.Fatalf("unexpected session result: %#v", result)
	}
	authority := storageFake.presignInput
	if authority.Key != rawKey || authority.ContentType != domain.MediaContentTypePNG || authority.ExpectedSizeBytes != 7 || !bytes.Equal(authority.ChecksumSHA256, request.DeclaredChecksumSHA256) {
		t.Fatalf("presign was not bound to reserved identity: %#v", authority)
	}
}

func TestMediaUsecase_CreateNormalizesDurableSessionIdentityToDatabasePrecision(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 725, time.UTC)
	request := validCreateRequest()
	repositoryFake := &mediaRepositoryFake{createErr: repository.ErrQuotaExceeded}
	service := usecase.NewMediaUsecase(repositoryFake, &mediaStorageFake{}, mediaUsecaseClock{at: now}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())

	_, _ = service.CreateUploadSession(context.Background(), request)

	wantIssuedAt := now.Truncate(time.Microsecond)
	if repositoryFake.createOptions.CredentialExpiresAt != wantIssuedAt.Add(time.Hour) ||
		repositoryFake.createOptions.SessionExpiresAt != wantIssuedAt.Add(24*time.Hour) {
		t.Fatalf("durable expiries = credential %s session %s", repositoryFake.createOptions.CredentialExpiresAt, repositoryFake.createOptions.SessionExpiresAt)
	}
}

func TestMediaUsecase_CommitHeadsExactPrivateKeyBeforeDurableTransaction(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	checksum := bytes.Repeat([]byte{0x2a}, 32)
	repositoryFake := &mediaRepositoryFake{
		getSession: domain.MediaUploadSession{
			ID: "upload-1", OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1",
			State: domain.UploadSessionCreated, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
			OriginalFilename: "photo.png", DeclaredContentType: domain.MediaContentTypePNG,
			ExpectedSizeBytes: 7, DeclaredChecksumSHA256: checksum,
		},
		acceptance: domain.CommitAcceptance{VersionID: "version-1", JobID: "job-1", Status: domain.ProcessingJobQueued, AcceptedAt: now},
	}
	storageFake := &mediaStorageFake{headResult: domain.ObjectAttributes{SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: checksum}}
	ids := &sequenceIDs{values: []string{"version-1", "job-1", "outbox-1"}}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: now}, ids, mediaPolicy())

	result, err := service.CommitUpload(context.Background(), domain.CommitUploadRequest{
		OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1", UploadID: "upload-1",
	})
	if err != nil {
		t.Fatalf("commit upload: %v", err)
	}
	if storageFake.headKey != domain.ObjectKey(repositoryFake.getSession.RawObjectKey) || repositoryFake.commitCalls != 1 {
		t.Fatalf("expected exact HEAD before one commit, key=%q commits=%d", storageFake.headKey, repositoryFake.commitCalls)
	}
	if repositoryFake.commitAttrs.SizeBytes != 7 || !bytes.Equal(repositoryFake.commitAttrs.ChecksumSHA256, checksum) {
		t.Fatalf("repository did not receive verified attributes: %#v", repositoryFake.commitAttrs)
	}
	if result.JobID != "job-1" || result.Status != domain.ProcessingJobQueued || result.OriginalFilename != "photo.png" {
		t.Fatalf("unexpected durable result: %#v", result)
	}
}

func TestMediaUsecase_CommittedRetryReturnsDurableJobWithoutStorage(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	repositoryFake := &mediaRepositoryFake{
		getSession: domain.MediaUploadSession{
			ID: "upload-1", OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1",
			State: domain.UploadSessionCommitted, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
			OriginalFilename: "photo.png", DeclaredContentType: domain.MediaContentTypePNG,
			ExpectedSizeBytes: 7, DeclaredChecksumSHA256: bytes.Repeat([]byte{0x2a}, 32),
		},
		acceptance: domain.CommitAcceptance{
			VersionID: "version-1", JobID: "job-1", Status: domain.ProcessingJobQueued, AcceptedAt: now,
		},
		commitReplay: true,
	}
	storageFake := &mediaStorageFake{headErr: errors.New("storage temporarily unavailable")}
	service := usecase.NewMediaUsecase(
		repositoryFake,
		storageFake,
		mediaUsecaseClock{at: now},
		&sequenceIDs{},
		mediaPolicy(),
	)

	result, err := service.CommitUpload(context.Background(), domain.CommitUploadRequest{
		OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1", UploadID: "upload-1",
	})

	if err != nil {
		t.Fatalf("committed retry: %v", err)
	}
	if storageFake.headCalls != 0 {
		t.Fatalf("committed retry made %d storage HEAD calls", storageFake.headCalls)
	}
	if repositoryFake.commitCalls != 1 || !result.Replayed || result.JobID != "job-1" {
		t.Fatalf("durable replay = %#v, repository calls = %d", result, repositoryFake.commitCalls)
	}
}

func TestMediaUsecase_StorageMismatchNeverStartsAssetTransaction(t *testing.T) {
	repositoryFake := &mediaRepositoryFake{getSession: domain.MediaUploadSession{
		ID: "upload-1", OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1",
		State: domain.UploadSessionCreated, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
		DeclaredContentType: domain.MediaContentTypePNG, ExpectedSizeBytes: 7,
		DeclaredChecksumSHA256: bytes.Repeat([]byte{0x2a}, 32),
	}}
	storageFake := &mediaStorageFake{headResult: domain.ObjectAttributes{SizeBytes: 8, ContentType: "image/png"}}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{values: []string{"v", "j", "o"}}, mediaPolicy())

	_, err := service.CommitUpload(context.Background(), domain.CommitUploadRequest{
		OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1", UploadID: "upload-1",
	})
	if !errors.Is(err, repository.ErrMediaObjectMismatch) {
		t.Fatalf("expected object mismatch, got %v", err)
	}
	if repositoryFake.commitCalls != 0 {
		t.Fatalf("mismatched object started %d asset transactions", repositoryFake.commitCalls)
	}
}

func TestMediaUsecase_ChecksumlessHeadRejectsDifferentStoredBytesBeforeDurableCommit(t *testing.T) {
	declaredChecksum := sha256.Sum256([]byte("correct"))
	session := domain.MediaUploadSession{
		ID:                     "upload-1",
		OrgID:                  "org-1",
		AssetID:                "asset-1",
		RequestedBy:            "user-1",
		State:                  domain.UploadSessionCreated,
		RawObjectKey:           "raw/org-1/asset-1/upload-1/original.png",
		DeclaredContentType:    domain.MediaContentTypePNG,
		ExpectedSizeBytes:      7,
		DeclaredChecksumSHA256: declaredChecksum[:],
	}
	repositoryFake := &mediaRepositoryFake{getSession: session}
	storageFake := &mediaStorageFake{
		headResult: domain.ObjectAttributes{
			SizeBytes:   7,
			ContentType: "image/png",
		},
		getBody: []byte("altered"),
	}
	service := usecase.NewMediaUsecase(
		repositoryFake,
		storageFake,
		mediaUsecaseClock{at: time.Now()},
		&sequenceIDs{values: []string{"version-1", "job-1", "outbox-1"}},
		mediaPolicy(),
	)

	_, err := service.CommitUpload(context.Background(), domain.CommitUploadRequest{
		OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1", UploadID: "upload-1",
	})

	if !errors.Is(err, repository.ErrMediaObjectMismatch) {
		t.Fatalf("checksumless mismatch error = %v, want media object mismatch", err)
	}
	if storageFake.getCalls != 1 || storageFake.getKey != domain.ObjectKey(session.RawObjectKey) {
		t.Fatalf("checksumless verification GET calls=%d key=%q", storageFake.getCalls, storageFake.getKey)
	}
	if repositoryFake.commitCalls != 0 {
		t.Fatalf("checksumless mismatch started %d durable commits", repositoryFake.commitCalls)
	}
}

func TestMediaUsecase_EveryAdmittedSizeUsesOneWholeObjectPresignedDescriptor(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	limits := mediaPolicy().Limits
	for size := int64(1); size <= 50_000_000; size++ {
		if !limits.AdmitsUploadSize(size) {
			t.Fatalf("size %d unexpectedly selects no direct-upload path", size)
		}
	}
	for _, size := range []int64{1, 2, 999, 5_000_000, 49_999_999, 50_000_000} {
		t.Run(strconv.FormatInt(size, 10)+"-bytes", func(t *testing.T) {
			request := validCreateRequest()
			request.ExpectedSizeBytes = size
			rawKey, _ := domain.RawObjectKey(request.OrgID, request.AssetID, "upload-1", request.DeclaredContentType)
			repositoryFake := &mediaRepositoryFake{createSession: domain.MediaUploadSession{
				ID: "upload-1", OrgID: request.OrgID, AssetID: request.AssetID, RequestedBy: request.RequestedBy,
				State: domain.UploadSessionCreated, RawObjectKey: rawKey.String(), DeclaredContentType: request.DeclaredContentType,
				ExpectedSizeBytes: size, DeclaredChecksumSHA256: append([]byte(nil), request.DeclaredChecksumSHA256...),
				CredentialExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(24 * time.Hour),
			}}
			storageFake := &mediaStorageFake{presignResult: domain.UploadDescriptor{
				Protocol: "HTTP", Method: "PUT", URL: "https://uploads.test/exact", Headers: map[string]string{},
			}}
			service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: now}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())

			result, err := service.CreateUploadSession(context.Background(), request)
			if err != nil {
				t.Fatalf("create size %d: %v", size, err)
			}
			if result.Descriptor.Protocol != "HTTP" || result.Descriptor.Method != "PUT" || storageFake.presignCalls != 1 {
				t.Fatalf("size %d descriptor = %#v, presign calls = %d", size, result.Descriptor, storageFake.presignCalls)
			}
			if storageFake.presignInput.ExpectedSizeBytes != size || storageFake.presignInput.Key != rawKey {
				t.Fatalf("size %d authority = %#v", size, storageFake.presignInput)
			}
		})
	}
}

func TestMediaUsecase_RefreshPreservesSessionKeyChecksumAndTwentyFourHourIdentity(t *testing.T) {
	now := time.Date(2026, time.August, 13, 11, 0, 1, 0, time.UTC)
	checksum := bytes.Repeat([]byte{0x2a}, 32)
	original := domain.MediaUploadSession{
		ID: "upload-1", OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1",
		State: domain.UploadSessionCreated, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
		DeclaredContentType: domain.MediaContentTypePNG, ExpectedSizeBytes: 50_000_000,
		DeclaredChecksumSHA256: checksum, CredentialExpiresAt: now.Add(-time.Second),
		SessionExpiresAt: time.Date(2026, time.August, 14, 10, 0, 0, 0, time.UTC),
	}
	refreshed := original
	refreshed.CredentialExpiresAt = now.Add(time.Hour)
	repositoryFake := &mediaRepositoryFake{getSession: original, refreshSession: refreshed}
	storageFake := &mediaStorageFake{
		headErr:       domain.ErrObjectNotFound,
		presignResult: domain.UploadDescriptor{Protocol: "HTTP", Method: "PUT", URL: "https://uploads.test/refreshed"},
	}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: now}, &sequenceIDs{}, mediaPolicy())

	result, err := service.RefreshUploadSession(context.Background(), repository.UploadSessionScope{
		OrgID: original.OrgID, AssetID: original.AssetID, UploadID: original.ID, RequestedBy: original.RequestedBy,
	})
	if err != nil {
		t.Fatalf("refresh credential: %v", err)
	}
	if repositoryFake.refreshCalls != 1 || repositoryFake.refreshExpiry != now.Add(time.Hour) {
		t.Fatalf("refresh persistence calls=%d expiry=%s", repositoryFake.refreshCalls, repositoryFake.refreshExpiry)
	}
	if storageFake.headKey != domain.ObjectKey(original.RawObjectKey) {
		t.Fatalf("refresh HEAD key = %q", storageFake.headKey)
	}
	if result.Session.ID != original.ID || result.Session.RawObjectKey != original.RawObjectKey ||
		result.Session.SessionExpiresAt != original.SessionExpiresAt ||
		!bytes.Equal(result.Session.DeclaredChecksumSHA256, original.DeclaredChecksumSHA256) {
		t.Fatalf("refresh changed immutable identity: before=%#v after=%#v", original, result.Session)
	}
	if storageFake.presignInput.Key != domain.ObjectKey(original.RawObjectKey) ||
		storageFake.presignInput.ExpectedSizeBytes != original.ExpectedSizeBytes ||
		!bytes.Equal(storageFake.presignInput.ChecksumSHA256, original.DeclaredChecksumSHA256) {
		t.Fatalf("refreshed authority changed whole-file identity: %#v", storageFake.presignInput)
	}
}

func TestMediaUsecase_RefreshRecoversLostPutResponseWithExactHead(t *testing.T) {
	checksum := bytes.Repeat([]byte{0x2a}, 32)
	session := domain.MediaUploadSession{
		ID: "upload-1", OrgID: "org-1", AssetID: "asset-1", RequestedBy: "user-1",
		State: domain.UploadSessionCreated, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
		DeclaredContentType: domain.MediaContentTypePNG, ExpectedSizeBytes: 7,
		DeclaredChecksumSHA256: checksum, SessionExpiresAt: time.Now().Add(23 * time.Hour),
	}
	repositoryFake := &mediaRepositoryFake{getSession: session}
	storageFake := &mediaStorageFake{headResult: domain.ObjectAttributes{
		SizeBytes: 7, ContentType: "image/png", ChecksumSHA256: checksum,
	}}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{}, mediaPolicy())

	_, err := service.RefreshUploadSession(context.Background(), repository.UploadSessionScope{
		OrgID: session.OrgID, AssetID: session.AssetID, UploadID: session.ID, RequestedBy: session.RequestedBy,
	})
	if !errors.Is(err, repository.ErrUploadStateConflict) {
		t.Fatalf("refresh after lost PUT response error = %v, want state conflict directing commit", err)
	}
	if storageFake.headKey != domain.ObjectKey(session.RawObjectKey) || repositoryFake.refreshCalls != 0 || storageFake.presignCalls != 0 {
		t.Fatalf("lost-response recovery head=%q refresh=%d presign=%d", storageFake.headKey, repositoryFake.refreshCalls, storageFake.presignCalls)
	}
}

func TestMediaUsecase_RefreshRejectsChecksumlessObjectWithDifferentStoredBytes(t *testing.T) {
	declaredChecksum := sha256.Sum256([]byte("correct"))
	session := domain.MediaUploadSession{
		ID:                     "upload-1",
		OrgID:                  "org-1",
		AssetID:                "asset-1",
		RequestedBy:            "user-1",
		State:                  domain.UploadSessionCreated,
		RawObjectKey:           "raw/org-1/asset-1/upload-1/original.png",
		DeclaredContentType:    domain.MediaContentTypePNG,
		ExpectedSizeBytes:      7,
		DeclaredChecksumSHA256: declaredChecksum[:],
		SessionExpiresAt:       time.Now().Add(23 * time.Hour),
	}
	repositoryFake := &mediaRepositoryFake{getSession: session}
	storageFake := &mediaStorageFake{
		headResult: domain.ObjectAttributes{
			SizeBytes:   7,
			ContentType: "image/png",
		},
		getBody: []byte("altered"),
	}
	service := usecase.NewMediaUsecase(
		repositoryFake,
		storageFake,
		mediaUsecaseClock{at: time.Now()},
		&sequenceIDs{},
		mediaPolicy(),
	)

	_, err := service.RefreshUploadSession(
		context.Background(),
		repository.UploadSessionScope{
			OrgID: session.OrgID, AssetID: session.AssetID,
			UploadID: session.ID, RequestedBy: session.RequestedBy,
		},
	)

	if !errors.Is(err, repository.ErrMediaObjectMismatch) {
		t.Fatalf("checksumless refresh error = %v, want media object mismatch", err)
	}
	if storageFake.getCalls != 1 || storageFake.getKey != domain.ObjectKey(session.RawObjectKey) {
		t.Fatalf("checksumless refresh GET calls=%d key=%q", storageFake.getCalls, storageFake.getKey)
	}
	if repositoryFake.refreshCalls != 0 || storageFake.presignCalls != 0 {
		t.Fatalf(
			"mismatched refresh persisted=%d presigned=%d",
			repositoryFake.refreshCalls,
			storageFake.presignCalls,
		)
	}
}

func TestMediaUsecase_CancelDelegatesExactScopeWithoutTouchingStorage(t *testing.T) {
	repositoryFake := &mediaRepositoryFake{}
	storageFake := &mediaStorageFake{}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{}, mediaPolicy())
	scope := repository.UploadSessionScope{OrgID: "org-1", AssetID: "asset-1", UploadID: "upload-1", RequestedBy: "user-1"}

	if err := service.CancelUploadSession(context.Background(), scope); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if repositoryFake.cancelCalls != 1 || repositoryFake.cancelScope != scope {
		t.Fatalf("cancel calls=%d scope=%#v", repositoryFake.cancelCalls, repositoryFake.cancelScope)
	}
	if storageFake.headCalls != 0 || storageFake.presignCalls != 0 {
		t.Fatalf("cancel performed storage I/O: head=%d presign=%d", storageFake.headCalls, storageFake.presignCalls)
	}
}

func TestMediaUsecase_CommittedReplayIssuesNoFurtherUploadAuthority(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	request := validCreateRequest()
	repositoryFake := &mediaRepositoryFake{createReplay: true, createSession: domain.MediaUploadSession{
		ID: "upload-1", OrgID: request.OrgID, AssetID: request.AssetID, State: domain.UploadSessionCommitted,
		RawObjectKey: "org-1/asset-1/upload-1.png", CredentialExpiresAt: now.Add(time.Hour),
	}}
	storageFake := &mediaStorageFake{}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: now}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())

	result, err := service.CreateUploadSession(context.Background(), request)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !result.Replayed || result.Descriptor.URL != "" {
		t.Fatalf("committed replay returned upload authority: %#v", result)
	}
	if storageFake.presignCalls != 0 {
		t.Fatalf("committed replay issued %d upload authorities", storageFake.presignCalls)
	}
}

func TestMediaUsecase_GetReportsUnpublishedSessionAsGone(t *testing.T) {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	scope := repository.UploadSessionScope{OrgID: "org-1", AssetID: "asset-1", UploadID: "upload-1", RequestedBy: "user-1"}

	for _, state := range []domain.UploadSessionState{domain.UploadSessionCancelled, domain.UploadSessionExpired, domain.UploadSessionFailed} {
		repositoryFake := &mediaRepositoryFake{getSession: domain.MediaUploadSession{
			ID: "upload-1", State: state, CredentialExpiresAt: now.Add(time.Hour),
		}}
		service := usecase.NewMediaUsecase(repositoryFake, &mediaStorageFake{}, mediaUsecaseClock{at: now}, &sequenceIDs{}, mediaPolicy())

		_, err := service.GetUploadSession(context.Background(), scope)

		if !errors.Is(err, repository.ErrUploadNotFound) {
			t.Fatalf("state %s returned %v, want gone", state, err)
		}
	}
}

func TestMediaUsecase_AdmissionRejectsHostileFilenames(t *testing.T) {
	hostile := map[string]string{
		"posix traversal":    "../../etc/passwd.png",
		"windows sepator":    "dir\\photo.png",
		"line break":         "photo\n.png",
		"nul":                "photo\x00.png",
		"direction override": "photo‮gnp.png",
		"only extension":     ".png",
	}

	for name, filename := range hostile {
		t.Run(name, func(t *testing.T) {
			repositoryFake := &mediaRepositoryFake{}
			storageFake := &mediaStorageFake{}
			service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())
			request := validCreateRequest()
			request.OriginalFilename = filename

			_, err := service.CreateUploadSession(context.Background(), request)

			if !errors.Is(err, usecase.ErrInvalidMediaFilename) {
				t.Fatalf("error = %v, want %v", err, usecase.ErrInvalidMediaFilename)
			}
			if repositoryFake.createCalls != 0 || storageFake.presignCalls != 0 {
				t.Error("a rejected filename must never reach the database or storage")
			}
		})
	}
}

func TestMediaUsecase_AdmissionStoresTheNormalizedFilename(t *testing.T) {
	repositoryFake := &mediaRepositoryFake{}
	storageFake := &mediaStorageFake{}
	service := usecase.NewMediaUsecase(repositoryFake, storageFake, mediaUsecaseClock{at: time.Now()}, &sequenceIDs{values: []string{"upload-1"}}, mediaPolicy())
	request := validCreateRequest()
	request.OriginalFilename = "  café.png  "

	if _, err := service.CreateUploadSession(context.Background(), request); err != nil {
		t.Fatalf("CreateUploadSession: %v", err)
	}

	if stored := repositoryFake.createRequest.OriginalFilename; stored != "café.png" {
		t.Errorf("stored filename = %q, want the trimmed composed form", stored)
	}
}
