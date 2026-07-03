package http_test

import (
	"context"
	"encoding/json"
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
	treeRootPath string
	createInput  domain.CreateFolderInput
	createErr    error
	updateInput  domain.UpdateFolderInput
	updateErr    error

	metadataItemsResp   []domain.MetadataItem
	metadataItemsErr    error
	metadataItemResp    domain.MetadataItem
	metadataItemErr     error
	metadataCreateInput domain.CreateMetadataInput
	metadataCreateErr   error
	metadataUpdateInput domain.UpdateMetadataInput
	metadataUpdateErr   error
	metadataSearchInput domain.MetadataSearchFilter

	moveFolderFunc   func(ctx context.Context, orgID, userID, folderID string, input domain.MoveFolderInput) (domain.Folder, error)
	deleteFolderFunc func(ctx context.Context, orgID, userID, folderID string) error
}

func (f *fakeAssetUsecase) GetFolderTree(_ context.Context, orgID, rootPath string) ([]domain.Folder, error) {
	f.called = true
	f.methodCalled = "GetFolderTree"
	f.orgID = orgID
	f.treeRootPath = rootPath
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

func (f *fakeAssetUsecase) CreateFolder(_ context.Context, orgID, userID string, input domain.CreateFolderInput) (domain.Folder, error) {
	f.called = true
	f.methodCalled = "CreateFolder"
	f.orgID = orgID
	f.createInput = input
	return domain.Folder{}, f.createErr
}

func (f *fakeAssetUsecase) UpdateFolder(_ context.Context, orgID, userID, folderID string, input domain.UpdateFolderInput) (domain.Folder, error) {
	f.called = true
	f.methodCalled = "UpdateFolder"
	f.orgID = orgID
	f.updateInput = input
	return domain.Folder{}, f.updateErr
}
func (f *fakeAssetUsecase) MoveFolder(ctx context.Context, orgID, userID, folderID string, input domain.MoveFolderInput) (domain.Folder, error) {
	f.called = true
	f.methodCalled = "MoveFolder"
	f.orgID = orgID
	if f.moveFolderFunc != nil {
		return f.moveFolderFunc(ctx, orgID, userID, folderID, input)
	}
	return domain.Folder{}, nil
}

func (f *fakeAssetUsecase) DeleteFolder(ctx context.Context, orgID, userID, folderID string) error {
	f.called = true
	f.methodCalled = "DeleteFolder"
	f.orgID = orgID
	if f.deleteFolderFunc != nil {
		return f.deleteFolderFunc(ctx, orgID, userID, folderID)
	}
	return nil
}

func (f *fakeAssetUsecase) EnsureRefs(_ context.Context, userID, orgID string) error {
	return nil
}

// GetMetadataItemsByFolder captures metadata list delegation for handler assertions.
func (f *fakeAssetUsecase) GetMetadataItemsByFolder(_ context.Context, orgID, folderID string) ([]domain.MetadataItem, error) {
	f.called = true
	f.methodCalled = "GetMetadataItemsByFolder"
	f.orgID = orgID
	return f.metadataItemsResp, f.metadataItemsErr
}

// GetMetadataItemByID captures metadata detail delegation for handler assertions.
func (f *fakeAssetUsecase) GetMetadataItemByID(_ context.Context, orgID, id string) (domain.MetadataItem, error) {
	f.called = true
	f.methodCalled = "GetMetadataItemByID"
	f.orgID = orgID
	return f.metadataItemResp, f.metadataItemErr
}

// CreateMetadataItem captures decoded create input and returns configured results.
func (f *fakeAssetUsecase) CreateMetadataItem(_ context.Context, orgID, userID string, input domain.CreateMetadataInput) (domain.MetadataItem, error) {
	f.called = true
	f.methodCalled = "CreateMetadataItem"
	f.orgID = orgID
	f.metadataCreateInput = input
	return f.metadataItemResp, f.metadataCreateErr
}

// UpdateMetadataItem captures sparse update presence for handler assertions.
func (f *fakeAssetUsecase) UpdateMetadataItem(_ context.Context, orgID, userID, id string, input domain.UpdateMetadataInput) (domain.MetadataItem, error) {
	f.called = true
	f.methodCalled = "UpdateMetadataItem"
	f.orgID = orgID
	f.metadataUpdateInput = input
	return f.metadataItemResp, f.metadataUpdateErr
}

// SearchMetadataItems captures decoded search input and returns configured results.
func (f *fakeAssetUsecase) SearchMetadataItems(_ context.Context, orgID string, filter domain.MetadataSearchFilter) ([]domain.MetadataItem, error) {
	f.called = true
	f.methodCalled = "SearchMetadataItems"
	f.orgID = orgID
	f.metadataSearchInput = filter
	return f.metadataItemsResp, f.metadataItemsErr
}

// DeleteMetadataItem captures delete presence for handler assertions.
func (f *fakeAssetUsecase) DeleteMetadataItem(_ context.Context, orgID, userID, id string) error {
	f.called = true
	f.methodCalled = "DeleteMetadataItem"
	f.orgID = orgID
	return f.metadataItemErr
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

func TestHandleFoldersListFullTreeUsesSingleTreeQuery(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	req := httptest.NewRequest(
		http.MethodGet,
		"/internal/api/v1/folders?orgId="+orgID+"&tree=true",
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
		t.Fatal("expected GetFolderTree to be called for tree=true")
	}
	if usecase.treeRootPath != "" {
		t.Fatalf("expected an empty tree root, got %q", usecase.treeRootPath)
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

func TestHandleFolderFactsReturnsScopedActiveFact(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const orgID = "00000000-0000-0000-0000-000000000002"
	const folderID = "10000000-0000-0000-0000-000000000000"

	usecase := &fakeAssetUsecase{folderByID: &domain.Folder{
		ID: folderID, OrgID: orgID, Path: "root", Name: "Root",
	}}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)
	req := httptest.NewRequest(http.MethodGet,
		"/internal/api/v1/facts/folders?orgId="+orgID+"&id="+folderID, nil)
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("X-Org-Id", orgID)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, response.Code, response.Body.String())
	}
	var fact struct {
		ResourceType string `json:"resource_type"`
		ID           string `json:"id"`
		OrgID        string `json:"org_id"`
		Active       bool   `json:"active"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &fact); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if fact.ResourceType != "folder" || fact.ID != folderID || fact.OrgID != orgID || !fact.Active {
		t.Fatalf("unexpected fact: %+v", fact)
	}
}

func TestHandleFolderFactsRejectsOrganizationMismatchBeforeUsecase(t *testing.T) {
	const userID = "00000000-0000-0000-0000-000000000001"
	const actorOrgID = "00000000-0000-0000-0000-000000000002"
	const requestedOrgID = "00000000-0000-0000-0000-000000000003"
	const folderID = "10000000-0000-0000-0000-000000000000"

	usecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	assetHTTP.NewAssetHandler(mux, usecase, nil)
	req := httptest.NewRequest(http.MethodGet,
		"/internal/api/v1/facts/folders?orgId="+requestedOrgID+"&id="+folderID, nil)
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
