package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gorm.io/gorm"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

type fakeAssetUsecase struct {
	called       bool
	methodCalled string
	orgID        string
	folderByID   *domain.Folder
	folderByErr  error
	childrenResp []domain.Folder
	childrenErr  error
	rootResp     []domain.Folder
	rootErr      error
}

func (f *fakeAssetUsecase) GetFolderTree(_ context.Context, orgID, _ string) ([]domain.Folder, error) {
	f.called = true
	f.methodCalled = "GetFolderTree"
	f.orgID = orgID
	return []domain.Folder{}, nil
}

func (f *fakeAssetUsecase) GetRootFolders(_ context.Context, orgID string) ([]domain.Folder, error) {
	f.called = true
	f.methodCalled = "GetRootFolders"
	f.orgID = orgID
	if f.rootErr != nil {
		return nil, f.rootErr
	}
	return f.rootResp, nil
}

func (f *fakeAssetUsecase) GetFolderByID(_ context.Context, orgID, folderID string) (domain.Folder, error) {
	f.called = true
	f.methodCalled = "GetFolderByID"
	f.orgID = orgID
	if f.folderByErr != nil {
		return domain.Folder{}, f.folderByErr
	}
	if f.folderByID != nil {
		return *f.folderByID, nil
	}
	return domain.Folder{}, gorm.ErrRecordNotFound
}

func (f *fakeAssetUsecase) GetFolderChildren(_ context.Context, orgID, parentPath string) ([]domain.Folder, error) {
	f.called = true
	f.methodCalled = "GetFolderChildren"
	f.orgID = orgID
	if f.childrenErr != nil {
		return nil, f.childrenErr
	}
	return f.childrenResp, nil
}

func (f *fakeAssetUsecase) EnsureRefs(_ context.Context, _, _ string) error {
	return nil
}

// ────────────────────────────────────────────────────────────
// HandleFolders (List) tests
// ────────────────────────────────────────────────────────────

func TestHandleFoldersListRejectsOrganizationContextMismatch(t *testing.T) {
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

func TestHandleFoldersListUsesMatchingOrganizationContext(t *testing.T) {
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
	if !usecase.called || usecase.methodCalled != "GetFolderTree" {
		t.Fatalf("expected GetFolderTree to be called")
	}
}

func TestHandleFoldersListMissingOrgId(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?rootPath=root",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
}

func TestHandleFoldersListWithoutRootPathCallsRootLevel(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+orgID,
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if !usecase.called || usecase.methodCalled != "GetRootFolders" {
		t.Fatal("expected GetRootFolders to be called for empty rootPath")
	}
}

func TestHandleFoldersListChildrenOnly(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{
		childrenResp: []domain.Folder{
			{ID: "child-1", OrgID: orgID, Path: "root.child1", Name: "Child 1"},
		},
	}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+orgID+"&rootPath=root&children=true",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
	}
	if !usecase.called || usecase.methodCalled != "GetFolderChildren" {
		t.Fatal("expected GetFolderChildren to be called")
	}
}

func TestHandleFoldersListChildrenMissingRootPathReturns400(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+orgID+"&children=true",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
}

// ────────────────────────────────────────────────────────────
// HandleFolders (Detail) tests
// ────────────────────────────────────────────────────────────

func TestHandleFolderDetailValidRequest(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"
	const folderID = "10000000-0000-0000-0000-000000000000"

	usecase := &fakeAssetUsecase{
		folderByID: &domain.Folder{
			ID:    folderID,
			OrgID: orgID,
			Path:  "root",
			Name:  "Root",
		},
	}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?id="+folderID,
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d; body: %s", http.StatusOK, response.Code, response.Body.String())
	}
	if !usecase.called || usecase.methodCalled != "GetFolderByID" {
		t.Fatal("expected GetFolderByID to be called")
	}
}

func TestHandleFolderDetailNotFound(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"
	const folderID = "99999999-0000-0000-0000-000000000000"

	usecase := &fakeAssetUsecase{
		folderByErr: gorm.ErrRecordNotFound,
	}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?id="+folderID,
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected status %d, got %d", http.StatusNotFound, response.Code)
	}
}

func TestHandleFolderDetailInvalidUUID(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?id=not-a-uuid",
		nil,
	)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
	if usecase.called {
		t.Fatal("expected usecase not to be called for invalid UUID")
	}
}

func TestHandleFolderDetailOrgMismatch(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const actorOrgID = "00000000-0000-0000-0000-000000000002"
	const reqOrgID = "00000000-0000-0000-0000-000000000003"
	const folderID = "10000000-0000-0000-0000-000000000000"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?id="+folderID+"&orgId="+reqOrgID,
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
		t.Fatal("expected usecase not to be called on org mismatch")
	}
}
