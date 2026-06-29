package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

type fakeAssetUsecase struct {
	called bool
	orgID  string
}

func (f *fakeAssetUsecase) GetFolderTree(_ context.Context, orgID, _ string) ([]domain.Folder, error) {
	f.called = true
	f.orgID = orgID
	return []domain.Folder{}, nil
}

func (f *fakeAssetUsecase) EnsureRefs(_ context.Context, _, _ string) error {
	return nil
}

func TestHandleFoldersRejectsOrganizationContextMismatch(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const actorOrgID = "00000000-0000-0000-0000-000000000002"
	const requestedOrgID = "00000000-0000-0000-0000-000000000003"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+requestedOrgID+"&rootPath=root",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", actorOrgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected status %d, got %d", http.StatusForbidden, response.Code)
	}
	if usecase.called {
		t.Fatal("expected organization mismatch to be rejected before the use case")
	}
}

func TestHandleFoldersUsesMatchingOrganizationContext(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+orgID+"&rootPath=root",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if !usecase.called || usecase.orgID != orgID {
		t.Fatalf("expected use case to receive org %s", orgID)
	}
}
