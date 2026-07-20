package http_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

// metadataRequest builds an authenticated internal request for the metadata route.
func metadataRequest(method, target, orgID, body string) *http.Request {
	request := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	request.Header.Set("X-User-Id", uuid.NewString())
	request.Header.Set("X-Org-Id", orgID)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

// TestHandleMetadata_List verifies list routing and a stable empty-array response.
func TestHandleMetadata_List(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	folderID := uuid.NewString()
	request := metadataRequest(http.MethodGet, "/internal/api/v1/metadata-items?orgId="+orgID+"&folderId="+folderID, orgID, "")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", response.Code)
	}
	if !fakeUsecase.called || fakeUsecase.methodCalled != "GetMetadataItemsByFolder" {
		t.Fatalf("expected list use case delegation")
	}
	var payload struct {
		Items []domain.MetadataItem `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Items == nil || len(payload.Items) != 0 {
		t.Fatalf("expected a non-nil empty items array")
	}
}

// TestHandleMetadata_GetRejectsAmbiguousSelector verifies clients cannot mix list and detail contracts.
func TestHandleMetadata_GetRejectsAmbiguousSelector(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	target := "/internal/api/v1/metadata-items?orgId=" + orgID + "&id=" + uuid.NewString() + "&folderId=" + uuid.NewString()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodGet, target, orgID, ""))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", response.Code)
	}
	if fakeUsecase.called {
		t.Fatal("expected ambiguous selector to stop before the use case")
	}
}

// TestHandleMetadata_DetailMapsNotFound verifies typed metadata misses become a safe 404.
func TestHandleMetadata_DetailMapsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{metadataItemErr: domain.ErrMetadataNotFound}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	target := "/internal/api/v1/metadata-items?orgId=" + orgID + "&id=" + uuid.NewString()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodGet, target, orgID, ""))

	if response.Code != http.StatusNotFound {
		t.Fatalf("expected 404 Not Found, got %d", response.Code)
	}
}

// TestHandleMetadata_CreateMapsInvalidInput verifies shared validation errors keep metadata semantics.
func TestHandleMetadata_CreateMapsInvalidInput(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{metadataCreateErr: domain.ErrInvalidInput}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	body := `{"folder_id":"` + uuid.NewString() + `","title":"Test Title"}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodPost, "/internal/api/v1/metadata-items?orgId="+orgID, orgID, body))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", response.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "METADATA_VALIDATION_ERROR" {
		t.Fatalf("expected metadata validation error, got %q", payload.Error.Code)
	}
}

// TestHandleMetadata_CreateRejectsMalformedFolderID verifies malformed resource identifiers fail before writes.
func TestHandleMetadata_CreateRejectsMalformedFolderID(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	body := `{"folder_id":"not-a-uuid","title":"Test Title"}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodPost, "/internal/api/v1/metadata-items?orgId="+orgID, orgID, body))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", response.Code)
	}
	if fakeUsecase.called {
		t.Fatal("expected malformed folder_id to stop before the use case")
	}
}

// TestHandleMetadata_Create verifies valid JSON transport delegates to the create use case.
func TestHandleMetadata_Create(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	folderID := uuid.NewString()
	body := `{"folder_id":"` + folderID + `","title":"Test Title","description":null}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodPost, "/internal/api/v1/metadata-items?orgId="+orgID, orgID, body))

	if response.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", response.Code)
	}
	if !fakeUsecase.called || fakeUsecase.metadataCreateInput.FolderID != folderID {
		t.Fatalf("expected create use case delegation")
	}
	if fakeUsecase.metadataCreateInput.Description != nil {
		t.Fatalf("expected explicit null description to remain nil")
	}
}

// TestHandleMetadata_UpdatePreservesExplicitNulls verifies PATCH presence survives HTTP decoding.
func TestHandleMetadata_UpdatePreservesExplicitNulls(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	target := "/internal/api/v1/metadata-items?orgId=" + orgID + "&id=" + uuid.NewString()
	body := `{"description":null,"labels":null,"metadata_json":null}`
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodPatch, target, orgID, body))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", response.Code)
	}
	input := fakeUsecase.metadataUpdateInput
	if !input.DescriptionSet || input.Description != nil {
		t.Fatalf("expected explicit null description")
	}
	if !input.LabelsSet || input.Labels == nil || len(*input.Labels) != 0 {
		t.Fatalf("expected labels:null to map to an empty array")
	}
	if !input.MetadataJSONSet || input.MetadataJSON != nil {
		t.Fatalf("expected metadata_json:null presence with nil value")
	}
}

// TestHandleMetadata_RejectsOrganizationMismatch verifies org isolation before any metadata use-case call.
func TestHandleMetadata_RejectsOrganizationMismatch(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	actorOrgID := uuid.NewString()
	requestedOrgID := uuid.NewString()
	target := "/internal/api/v1/metadata-items?orgId=" + requestedOrgID + "&folderId=" + uuid.NewString()
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodGet, target, actorOrgID, ""))

	if response.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", response.Code)
	}
	if fakeUsecase.called {
		t.Fatal("expected org mismatch to stop before the use case")
	}
}

// TestHandleMetadata_Search verifies search routing and query parameters.
func TestHandleMetadata_Search(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	folderID := uuid.NewString()
	target := "/internal/api/v1/metadata-items/search?orgId=" + orgID + "&folderId=" + folderID + "&limit=10&offset=5&query=test&label=alpha&label=beta&category=photo&externalSource=dam"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodGet, target, orgID, ""))

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", response.Code)
	}
	if !fakeUsecase.called || fakeUsecase.methodCalled != "SearchMetadataItems" {
		t.Fatalf("expected search use case delegation")
	}
	filter := fakeUsecase.metadataSearchInput
	if filter.FolderID == nil || *filter.FolderID != folderID || filter.Query == nil || *filter.Query != "test" {
		t.Fatalf("unexpected search selectors: %#v", filter)
	}
	if len(filter.Labels) != 2 || filter.Labels[0] != "alpha" || filter.Labels[1] != "beta" {
		t.Fatalf("expected repeated labels in input order, got %#v", filter.Labels)
	}
	if filter.Category == nil || *filter.Category != "photo" || filter.ExternalSource == nil || *filter.ExternalSource != "dam" {
		t.Fatalf("unexpected exact filters: %#v", filter)
	}
	if filter.Limit != 10 || filter.Offset != 5 {
		t.Fatalf("unexpected pagination: %#v", filter)
	}
}

// TestHandleMetadata_SearchRejectsMalformedPagination verifies parsing fails before the use case.
func TestHandleMetadata_SearchRejectsMalformedPagination(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	target := "/internal/api/v1/metadata-items/search?orgId=" + orgID + "&query=test&limit=invalid"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodGet, target, orgID, ""))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", response.Code)
	}
	if fakeUsecase.called {
		t.Fatal("expected malformed pagination to stop before the use case")
	}
}

// TestHandleMetadata_Delete verifies DELETE routing.
func TestHandleMetadata_Delete(t *testing.T) {
	mux := http.NewServeMux()
	fakeUsecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUsecase, nil)

	orgID := uuid.NewString()
	itemID := uuid.NewString()
	target := "/internal/api/v1/metadata-items?orgId=" + orgID + "&id=" + itemID
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, metadataRequest(http.MethodDelete, target, orgID, ""))

	if response.Code != http.StatusNoContent {
		t.Fatalf("expected 204 No Content, got %d", response.Code)
	}
	if !fakeUsecase.called || fakeUsecase.methodCalled != "DeleteMetadataItem" {
		t.Fatalf("expected delete use case delegation")
	}
}
