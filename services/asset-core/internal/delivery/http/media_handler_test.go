package http_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

type fakeMediaUsecase struct {
	createResult  domain.UploadSessionResult
	createErr     error
	getResult     domain.UploadSessionResult
	getErr        error
	refreshResult domain.UploadSessionResult
	refreshErr    error
	cancelErr     error
	commitResult  domain.CommitUploadResult
	commitErr     error

	createRequest domain.CreateUploadSessionRequest
	getScope      repository.UploadSessionScope
	refreshScope  repository.UploadSessionScope
	cancelScope   repository.UploadSessionScope
	commitRequest domain.CommitUploadRequest
	statusResult  domain.MediaStatusResult
	statusErr     error
	statusScope   repository.MediaStatusScope
	statusCalls   int
	createCalls   int
	getCalls      int
	refreshCalls  int
	cancelCalls   int
	commitCalls   int
}

func (usecase *fakeMediaUsecase) GetMediaStatus(_ context.Context, scope repository.MediaStatusScope) (domain.MediaStatusResult, error) {
	usecase.statusCalls++
	usecase.statusScope = scope
	return usecase.statusResult, usecase.statusErr
}

func (usecase *fakeMediaUsecase) CreateUploadSession(_ context.Context, request domain.CreateUploadSessionRequest) (domain.UploadSessionResult, error) {
	usecase.createCalls++
	usecase.createRequest = request
	return usecase.createResult, usecase.createErr
}

func (usecase *fakeMediaUsecase) GetUploadSession(_ context.Context, scope repository.UploadSessionScope) (domain.UploadSessionResult, error) {
	usecase.getCalls++
	usecase.getScope = scope
	return usecase.getResult, usecase.getErr
}

func (usecase *fakeMediaUsecase) RefreshUploadSession(_ context.Context, scope repository.UploadSessionScope) (domain.UploadSessionResult, error) {
	usecase.refreshCalls++
	usecase.refreshScope = scope
	return usecase.refreshResult, usecase.refreshErr
}

func (usecase *fakeMediaUsecase) CancelUploadSession(_ context.Context, scope repository.UploadSessionScope) error {
	usecase.cancelCalls++
	usecase.cancelScope = scope
	return usecase.cancelErr
}

func (usecase *fakeMediaUsecase) CommitUpload(_ context.Context, request domain.CommitUploadRequest) (domain.CommitUploadResult, error) {
	usecase.commitCalls++
	usecase.commitRequest = request
	return usecase.commitResult, usecase.commitErr
}

func mediaInternalRequest(method, target, userID, orgID, retryKey, body string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("X-User-Id", userID)
	request.Header.Set("X-Org-Id", orgID)
	if retryKey != "" {
		request.Header.Set("Idempotency-Key", retryKey)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func validUploadSessionResult(assetID, uploadID string) domain.UploadSessionResult {
	now := time.Date(2026, time.August, 13, 10, 0, 0, 0, time.UTC)
	return domain.UploadSessionResult{
		Session: domain.MediaUploadSession{
			ID: uploadID, AssetID: assetID, State: domain.UploadSessionCreated,
			CredentialExpiresAt: now.Add(time.Hour), SessionExpiresAt: now.Add(24 * time.Hour), CreatedAt: now,
		},
		Descriptor: domain.UploadDescriptor{
			Protocol: "HTTP", Method: http.MethodPut, URL: "https://uploads.example.test/media/raw/exact.png",
			Headers:             map[string]string{"If-None-Match": "*", "Content-Type": "image/png"},
			CredentialExpiresAt: now.Add(time.Hour),
		},
	}
}

func TestMediaHandler_CreateRequiresIdentityRetryKeyAndSHA256(t *testing.T) {
	mux := http.NewServeMux()
	usecase := &fakeMediaUsecase{}
	assetHTTP.NewMediaHandler(mux, usecase)
	assetID := uuid.NewString()
	validChecksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	cases := []struct {
		name, userID, orgID, retryKey, body string
	}{
		{name: "missing user", orgID: uuid.NewString(), retryKey: uuid.NewString(), body: `{"filename":"photo.png","content_type":"image/png","size_bytes":7,"checksum_sha256":"` + validChecksum + `"}`},
		{name: "missing organization", userID: uuid.NewString(), retryKey: uuid.NewString(), body: `{"filename":"photo.png","content_type":"image/png","size_bytes":7,"checksum_sha256":"` + validChecksum + `"}`},
		{name: "missing retry key", userID: uuid.NewString(), orgID: uuid.NewString(), body: `{"filename":"photo.png","content_type":"image/png","size_bytes":7,"checksum_sha256":"` + validChecksum + `"}`},
		{name: "missing checksum", userID: uuid.NewString(), orgID: uuid.NewString(), retryKey: uuid.NewString(), body: `{"filename":"photo.png","content_type":"image/png","size_bytes":7}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := mediaInternalRequest(http.MethodPost, "/internal/api/v1/metadata-items/"+assetID+"/media/uploads", testCase.userID, testCase.orgID, testCase.retryKey, testCase.body)
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusUnauthorized {
				t.Fatalf("expected request rejection, got %d: %s", response.Code, response.Body.String())
			}
		})
	}
	if usecase.createCalls != 0 {
		t.Fatalf("invalid context or checksum delegated %d times", usecase.createCalls)
	}
}

func TestMediaHandler_CreateRejectsSizeAndDeclaredTypeBeforeUsecase(t *testing.T) {
	mux := http.NewServeMux()
	usecase := &fakeMediaUsecase{}
	assetHTTP.NewMediaHandler(mux, usecase)
	checksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	cases := []struct {
		name        string
		size        int64
		contentType string
		status      int
	}{
		{name: "empty", size: 0, contentType: "image/png", status: http.StatusBadRequest},
		{name: "too large", size: 50_000_001, contentType: "image/png", status: http.StatusRequestEntityTooLarge},
		{name: "unsupported", size: 7, contentType: "image/gif", status: http.StatusUnsupportedMediaType},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"filename": "photo.png", "content_type": testCase.contentType,
				"size_bytes": testCase.size, "checksum_sha256": checksum,
			})
			response := httptest.NewRecorder()
			request := mediaInternalRequest(http.MethodPost, "/internal/api/v1/metadata-items/"+uuid.NewString()+"/media/uploads", uuid.NewString(), uuid.NewString(), uuid.NewString(), string(body))
			mux.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("expected %d, got %d: %s", testCase.status, response.Code, response.Body.String())
			}
		})
	}
	if usecase.createCalls != 0 {
		t.Fatalf("invalid admission delegated %d times", usecase.createCalls)
	}
}

func TestMediaHandler_CreatePassesScopedContextAndReturnsSignedDescriptor(t *testing.T) {
	mux := http.NewServeMux()
	assetID, uploadID, userID, orgID, retryKey := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	usecase := &fakeMediaUsecase{createResult: validUploadSessionResult(assetID, uploadID)}
	assetHTTP.NewMediaHandler(mux, usecase)
	checksumBytes := bytes.Repeat([]byte{0x2a}, 32)
	body := `{"filename":"photo.png","content_type":"image/png","size_bytes":7,"checksum_sha256":"` + base64.StdEncoding.EncodeToString(checksumBytes) + `"}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodPost, "/internal/api/v1/metadata-items/"+assetID+"/media/uploads", userID, orgID, retryKey, body))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", response.Code, response.Body.String())
	}
	request := usecase.createRequest
	if request.AssetID != assetID || request.OrgID != orgID || request.RequestedBy != userID || request.IdempotencyKey != retryKey || !bytes.Equal(request.DeclaredChecksumSHA256, checksumBytes) {
		t.Fatalf("handler lost authorization or checksum scope: %#v", request)
	}
	var payload struct {
		Data struct {
			UploadID string `json:"upload_id"`
			Upload   struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
			} `json:"upload"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if payload.Data.UploadID != uploadID || payload.Data.Upload.Method != http.MethodPut || payload.Data.Upload.Headers["If-None-Match"] != "*" {
		t.Fatalf("unexpected create response: %#v", payload.Data)
	}
}

func TestMediaHandler_GetIsScopedToUserOrganizationAssetAndUpload(t *testing.T) {
	mux := http.NewServeMux()
	assetID, uploadID, userID, orgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	usecase := &fakeMediaUsecase{getResult: validUploadSessionResult(assetID, uploadID)}
	assetHTTP.NewMediaHandler(mux, usecase)
	response := httptest.NewRecorder()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media/uploads/" + uploadID
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodGet, target, userID, orgID, "", ""))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	if usecase.getScope != (repository.UploadSessionScope{OrgID: orgID, AssetID: assetID, UploadID: uploadID, RequestedBy: userID}) {
		t.Fatalf("unexpected get scope: %#v", usecase.getScope)
	}
}

func TestMediaHandler_StatusReturnsRequiredNullableAuthoritativeFields(t *testing.T) {
	assetID, uploadID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	userID, orgID := uuid.NewString(), uuid.NewString()
	now := time.Date(2026, time.August, 19, 11, 0, 0, 0, time.UTC)
	usecase := &fakeMediaUsecase{statusResult: domain.MediaStatusResult{
		AssetID: assetID, UploadID: uploadID, JobID: jobID,
		Status: domain.ProcessingJobQueued, AttemptCount: 0,
		Original: domain.MediaStatusOriginal{
			Filename: "photo.png", DeclaredContentType: domain.MediaContentTypePNG, SizeBytes: 7,
		},
		AcceptedAt: now,
	}}
	mux := http.NewServeMux()
	assetHTTP.NewMediaHandler(mux, usecase)
	response := httptest.NewRecorder()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media/status"
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodGet, target, userID, orgID, "", ""))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if usecase.statusCalls != 1 || usecase.statusScope != (repository.MediaStatusScope{OrgID: orgID, AssetID: assetID}) {
		t.Fatalf("status calls=%d scope=%#v", usecase.statusCalls, usecase.statusScope)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	for _, requiredNullable := range []string{"stage", "outputs", "error", "started_at", "completed_at", "failed_at"} {
		value, present := envelope.Data[requiredNullable]
		if !present || value != nil {
			t.Fatalf("field %s = %#v present=%v, want explicit null", requiredNullable, value, present)
		}
	}
	if envelope.Data["asset_id"] != assetID || envelope.Data["upload_id"] != uploadID ||
		envelope.Data["job_id"] != jobID || envelope.Data["status"] != "queued" ||
		envelope.Data["attempt_count"] != float64(0) {
		t.Fatalf("status data = %#v", envelope.Data)
	}
}

func TestMediaHandler_RefreshIsStrictAndScopedToRequester(t *testing.T) {
	assetID, uploadID, userID, orgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media/uploads/" + uploadID + "/refresh"

	t.Run("refreshes with no transfer negotiation body", func(t *testing.T) {
		mux := http.NewServeMux()
		usecase := &fakeMediaUsecase{refreshResult: validUploadSessionResult(assetID, uploadID)}
		assetHTTP.NewMediaHandler(mux, usecase)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, mediaInternalRequest(http.MethodPost, target, userID, orgID, "", ""))

		if response.Code != http.StatusOK {
			t.Fatalf("expected 200 refresh, got %d: %s", response.Code, response.Body.String())
		}
		wantScope := repository.UploadSessionScope{OrgID: orgID, AssetID: assetID, UploadID: uploadID, RequestedBy: userID}
		if usecase.refreshScope != wantScope {
			t.Fatalf("refresh scope = %#v, want %#v", usecase.refreshScope, wantScope)
		}
	})

	for _, body := range []string{
		`{"strategy":"MULTIPART"}`,
		`{"capabilities":{"multipart":true}}`,
		`{"manifest":[{"part":1}]}`,
	} {
		t.Run("rejects obsolete refresh body "+body, func(t *testing.T) {
			mux := http.NewServeMux()
			usecase := &fakeMediaUsecase{}
			assetHTTP.NewMediaHandler(mux, usecase)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, mediaInternalRequest(http.MethodPost, target, userID, orgID, "", body))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("expected strict 400, got %d: %s", response.Code, response.Body.String())
			}
			if usecase.refreshCalls != 0 {
				t.Fatalf("obsolete refresh body delegated %d times", usecase.refreshCalls)
			}
		})
	}
}

func TestMediaHandler_CommitRejectsAnythingBeyondUploadIdentity(t *testing.T) {
	assetID, uploadID, userID, orgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, field := range []string{"strategy", "capabilities", "manifest", "checksum_sha256"} {
		t.Run(field, func(t *testing.T) {
			mux := http.NewServeMux()
			usecase := &fakeMediaUsecase{}
			assetHTTP.NewMediaHandler(mux, usecase)
			response := httptest.NewRecorder()
			body := `{"upload_id":"` + uploadID + `","` + field + `":{}}`
			mux.ServeHTTP(response, mediaInternalRequest(http.MethodPut, "/internal/api/v1/metadata-items/"+assetID+"/media", userID, orgID, "", body))

			if response.Code != http.StatusBadRequest || usecase.commitCalls != 0 {
				t.Fatalf("field %s: status=%d commit calls=%d body=%s", field, response.Code, usecase.commitCalls, response.Body.String())
			}
		})
	}
}

func TestMediaHandler_DeleteCancelsExactScopedSessionAndReturnsNoContent(t *testing.T) {
	assetID, uploadID, userID, orgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	mux := http.NewServeMux()
	usecase := &fakeMediaUsecase{}
	assetHTTP.NewMediaHandler(mux, usecase)
	response := httptest.NewRecorder()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media/uploads/" + uploadID
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodDelete, target, userID, orgID, "", ""))

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("cancel response = %d %q", response.Code, response.Body.String())
	}
	want := repository.UploadSessionScope{OrgID: orgID, AssetID: assetID, UploadID: uploadID, RequestedBy: userID}
	if usecase.cancelCalls != 1 || usecase.cancelScope != want {
		t.Fatalf("cancel calls=%d scope=%#v, want %#v", usecase.cancelCalls, usecase.cancelScope, want)
	}
}

func TestMediaHandler_DeleteMapsCommittedConflictAndScopedMissingSafely(t *testing.T) {
	assetID, uploadID, userID, orgID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media/uploads/" + uploadID
	for _, testCase := range []struct {
		name       string
		usecaseErr error
		status     int
		code       string
	}{
		{name: "committed", usecaseErr: repository.ErrUploadStateConflict, status: http.StatusConflict, code: "MEDIA_UPLOAD_STATE_CONFLICT"},
		{name: "scoped missing", usecaseErr: repository.ErrUploadNotFound, status: http.StatusNotFound, code: "MEDIA_UPLOAD_NOT_FOUND"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			mux := http.NewServeMux()
			assetHTTP.NewMediaHandler(mux, &fakeMediaUsecase{cancelErr: testCase.usecaseErr})
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, mediaInternalRequest(http.MethodDelete, target, userID, orgID, "", ""))
			if response.Code != testCase.status || !strings.Contains(response.Body.String(), `"code":"`+testCase.code+`"`) {
				t.Fatalf("cancel error = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMediaHandler_CommitReturns202OnlyForDurableQueuedAcceptance(t *testing.T) {
	mux := http.NewServeMux()
	assetID, uploadID, userID, orgID, jobID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	acceptedAt := time.Date(2026, time.August, 13, 10, 1, 0, 0, time.UTC)
	usecase := &fakeMediaUsecase{commitResult: domain.CommitUploadResult{
		AssetID: assetID, UploadID: uploadID, JobID: jobID, Status: domain.ProcessingJobQueued,
		OriginalFilename: "photo.png", OriginalContentType: domain.MediaContentTypePNG,
		OriginalSizeBytes: 7, AcceptedAt: acceptedAt,
	}}
	assetHTTP.NewMediaHandler(mux, usecase)
	response := httptest.NewRecorder()
	target := "/internal/api/v1/metadata-items/" + assetID + "/media"
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodPut, target, userID, orgID, "", `{"upload_id":"`+uploadID+`"}`))

	if response.Code != http.StatusAccepted {
		t.Fatalf("expected durable 202, got %d: %s", response.Code, response.Body.String())
	}
	if usecase.commitRequest != (domain.CommitUploadRequest{OrgID: orgID, AssetID: assetID, RequestedBy: userID, UploadID: uploadID}) {
		t.Fatalf("unexpected commit scope: %#v", usecase.commitRequest)
	}
	var payload struct {
		Data struct {
			AssetID  string `json:"asset_id"`
			UploadID string `json:"upload_id"`
			JobID    string `json:"job_id"`
			Status   string `json:"status"`
			Original struct {
				Filename    string `json:"filename"`
				ContentType string `json:"content_type"`
				SizeBytes   int64  `json:"size_bytes"`
			} `json:"original"`
			AcceptedAt time.Time `json:"accepted_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode acceptance: %v", err)
	}
	if payload.Data.AssetID != assetID || payload.Data.UploadID != uploadID || payload.Data.JobID != jobID || payload.Data.Status != "queued" || payload.Data.Original.Filename != "photo.png" || !payload.Data.AcceptedAt.Equal(acceptedAt) {
		t.Fatalf("unexpected durable acceptance response: %#v", payload.Data)
	}
}

func TestMediaHandler_MapsSafeMediaErrorsWithoutStorageDetails(t *testing.T) {
	mux := http.NewServeMux()
	usecase := &fakeMediaUsecase{createErr: repository.ErrQuotaExceeded}
	assetHTTP.NewMediaHandler(mux, usecase)
	checksum := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x2a}, 32))
	response := httptest.NewRecorder()
	body := `{"filename":"photo.png","content_type":"image/png","size_bytes":7,"checksum_sha256":"` + checksum + `"}`
	mux.ServeHTTP(response, mediaInternalRequest(http.MethodPost, "/internal/api/v1/metadata-items/"+uuid.NewString()+"/media/uploads", uuid.NewString(), uuid.NewString(), uuid.NewString(), body))

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected quota 413, got %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if payload.Error.Code != "MEDIA_QUOTA_EXCEEDED" || payload.Error.Message == "" || errors.Is(usecase.createErr, nil) {
		t.Fatalf("unexpected safe quota error: %#v", payload.Error)
	}
}
