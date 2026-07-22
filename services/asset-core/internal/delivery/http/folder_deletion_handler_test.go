package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

type fakeFolderDeletionUsecase struct {
	preview      domain.FolderDeletionPreview
	job          domain.FolderDeletionJob
	err          error
	called       string
	actorIsAdmin bool
}

func (f *fakeFolderDeletionUsecase) PreviewFolderDeletion(_ context.Context, _, _, _ string) (domain.FolderDeletionPreview, error) {
	f.called = "preview"
	return f.preview, f.err
}

func (f *fakeFolderDeletionUsecase) ConfirmFolderDeletion(_ context.Context, _, _, _, _, _ string) (domain.FolderDeletionJob, error) {
	f.called = "confirm"
	return f.job, f.err
}

func (f *fakeFolderDeletionUsecase) GetFolderDeletionJob(_ context.Context, _, _, _ string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	f.called = "status"
	f.actorIsAdmin = actorIsOrgAdmin
	return f.job, f.err
}

func (f *fakeFolderDeletionUsecase) CancelFolderDeletionJob(_ context.Context, _, _, _ string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	f.called = "cancel"
	f.actorIsAdmin = actorIsOrgAdmin
	return f.job, f.err
}

func (f *fakeFolderDeletionUsecase) RetryFolderDeletionJob(_ context.Context, _, _, _ string, actorIsOrgAdmin bool) (domain.FolderDeletionJob, error) {
	f.called = "retry"
	f.actorIsAdmin = actorIsOrgAdmin
	return f.job, f.err
}

func folderDeletionRequest(method, path, userID, orgID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)
	return req
}

func TestHandleFolderDeletionPreview_ReturnsConfirmationWithoutStartingWorker(t *testing.T) {
	userID := uuid.NewString()
	orgID := uuid.NewString()
	folderID := uuid.NewString()
	fakeDeletion := &fakeFolderDeletionUsecase{
		preview: domain.FolderDeletionPreview{
			ID:                uuid.NewString(),
			RootFolderID:      folderID,
			ConfirmationToken: "token",
			ExpiresAt:         time.Now().UTC(),
		},
	}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, &fakeAssetUsecase{}, nil, fakeDeletion)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, folderDeletionRequest(http.MethodPost, "/internal/api/v1/folder-deletions/preview?orgId="+orgID+"&folderId="+folderID, userID, orgID))

	if response.Code != http.StatusOK || fakeDeletion.called != "preview" {
		t.Fatalf("expected preview response, status=%d called=%s", response.Code, fakeDeletion.called)
	}
	var body struct {
		Preview domain.FolderDeletionPreview `json:"preview"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	if body.Preview.ID != fakeDeletion.preview.ID {
		t.Fatalf("expected preview id %s, got %s", fakeDeletion.preview.ID, body.Preview.ID)
	}
}

func TestHandleFolderDeletionConfirm_RejectsMalformedToken(t *testing.T) {
	userID := uuid.NewString()
	orgID := uuid.NewString()
	folderID := uuid.NewString()
	mux := http.NewServeMux()
	fakeDeletion := &fakeFolderDeletionUsecase{}
	assetHTTP.NewAssetHandler(mux, &fakeAssetUsecase{}, nil, fakeDeletion)

	req := folderDeletionRequest(http.MethodPost, "/internal/api/v1/folder-deletions/confirm?orgId="+orgID+"&folderId="+folderID, userID, orgID)
	req.Body = io.NopCloser(strings.NewReader(`{"preview_id":"` + uuid.NewString() + `","confirmation_token":"not-a-valid-token"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest || fakeDeletion.called != "" {
		t.Fatalf("expected malformed token to be rejected before usecase, status=%d called=%s", response.Code, fakeDeletion.called)
	}
}

func TestHandleFolderDeletionJob_ForwardsTrustedOrgAdminFlag(t *testing.T) {
	userID := uuid.NewString()
	orgID := uuid.NewString()
	jobID := uuid.NewString()
	fakeDeletion := &fakeFolderDeletionUsecase{job: domain.FolderDeletionJob{ID: jobID, Status: domain.FolderDeletionQueued}}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, &fakeAssetUsecase{}, nil, fakeDeletion)

	req := folderDeletionRequest(http.MethodGet, "/internal/api/v1/folder-deletions/jobs?orgId="+orgID+"&id="+jobID, userID, orgID)
	req.Header.Set("X-Org-Admin", "true")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK || fakeDeletion.called != "status" || !fakeDeletion.actorIsAdmin {
		t.Fatalf("expected requester status lookup with org-admin flag, status=%d called=%s admin=%v", response.Code, fakeDeletion.called, fakeDeletion.actorIsAdmin)
	}
}
