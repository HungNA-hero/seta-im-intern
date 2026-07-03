package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

func TestHandleCreateFolder_Success(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	org1 := uuid.NewString()

	reqBody := `{"name": "New Folder"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/api/v1/folders?orgId="+org1, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", org1)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201 Created, got %d", rr.Code)
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "success" {
		t.Errorf("expected success status, got %v", resp["status"])
	}
}

func TestHandleUpdateFolder_Success(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	org1 := uuid.NewString()
	folder1 := uuid.NewString()

	reqBody := `{"name": "Renamed"}`
	req := httptest.NewRequest(http.MethodPatch, "/internal/api/v1/folders?orgId="+org1+"&id="+folder1, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", org1)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200 OK, got %d", rr.Code)
	}

	var resp map[string]any
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp["status"] != "success" {
		t.Errorf("expected success status, got %v", resp["status"])
	}
}

func TestHandleCreateFolder_OrgMismatch(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	org1 := uuid.NewString()
	org2 := uuid.NewString()

	reqBody := `{"name": "New Folder"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/api/v1/folders?orgId="+org2, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", org1) // actor org is org1

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403 Forbidden, got %d", rr.Code)
	}
}

func TestHandleCreateFolder_InvalidBody(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	org1 := uuid.NewString()

	reqBody := `{"invalid_field": "test"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/api/v1/folders?orgId="+org1, bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", org1)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request, got %d", rr.Code)
	}
}

func TestHandleCreateFolder_RejectsTrailingJSON(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	orgID := uuid.NewString()
	req := httptest.NewRequest(
		http.MethodPost,
		"/internal/api/v1/folders?orgId="+orgID,
		bytes.NewBufferString(`{"name":"First"} {"name":"Second"}`),
	)
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
	if fakeUc.called {
		t.Fatal("expected trailing JSON to be rejected before the use case")
	}
}

func TestHandleUpdateFolder_DescriptionPresence(t *testing.T) {
	tests := []struct {
		name               string
		body               string
		wantDescriptionSet bool
		wantDescription    *string
	}{
		{
			name:               "explicit null clears description",
			body:               `{"description":null}`,
			wantDescriptionSet: true,
		},
		{
			name:               "omitted description remains unchanged",
			body:               `{"name":"Renamed"}`,
			wantDescriptionSet: false,
		},
		{
			name:               "string description is forwarded",
			body:               `{"description":"Updated"}`,
			wantDescriptionSet: true,
			wantDescription:    stringPointer("Updated"),
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mux := http.NewServeMux()
			fakeUc := &fakeAssetUsecase{}
			assetHTTP.NewAssetHandler(mux, fakeUc, nil)

			orgID := uuid.NewString()
			req := httptest.NewRequest(
				http.MethodPatch,
				"/internal/api/v1/folders?orgId="+orgID+"&id="+uuid.NewString(),
				bytes.NewBufferString(testCase.body),
			)
			req.Header.Set("X-User-Id", uuid.NewString())
			req.Header.Set("X-Org-Id", orgID)

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)

			if response.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, response.Code)
			}
			if fakeUc.updateInput.DescriptionSet != testCase.wantDescriptionSet {
				t.Fatalf("expected DescriptionSet=%v, got %v", testCase.wantDescriptionSet, fakeUc.updateInput.DescriptionSet)
			}
			if testCase.wantDescription == nil {
				if fakeUc.updateInput.Description != nil {
					t.Fatalf("expected nil description, got %q", *fakeUc.updateInput.Description)
				}
			} else if fakeUc.updateInput.Description == nil || *fakeUc.updateInput.Description != *testCase.wantDescription {
				t.Fatalf("expected description %q", *testCase.wantDescription)
			}
		})
	}
}

func TestHandleUpdateFolder_InvalidUUID(t *testing.T) {
	mux := http.NewServeMux()
	fakeUc := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, fakeUc, nil)

	orgID := uuid.NewString()
	req := httptest.NewRequest(
		http.MethodPatch,
		"/internal/api/v1/folders?orgId="+orgID+"&id=not-a-uuid",
		bytes.NewBufferString(`{"name":"Renamed"}`),
	)
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", orgID)

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, response.Code)
	}
	if fakeUc.called {
		t.Fatal("expected invalid UUID to be rejected before the use case")
	}
}

func TestHandleCreateFolder_MapsDomainErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "not found", err: domain.ErrFolderNotFound, wantStatus: http.StatusNotFound},
		{name: "conflict", err: domain.ErrFolderConflict, wantStatus: http.StatusConflict},
		{name: "invalid input", err: domain.ErrInvalidInput, wantStatus: http.StatusBadRequest},
		{name: "internal error", err: errors.New("database unavailable"), wantStatus: http.StatusInternalServerError},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			mux := http.NewServeMux()
			fakeUc := &fakeAssetUsecase{createErr: testCase.err}
			assetHTTP.NewAssetHandler(mux, fakeUc, nil)

			orgID := uuid.NewString()
			req := httptest.NewRequest(
				http.MethodPost,
				"/internal/api/v1/folders?orgId="+orgID,
				bytes.NewBufferString(`{"name":"Folder"}`),
			)
			req.Header.Set("X-User-Id", uuid.NewString())
			req.Header.Set("X-Org-Id", orgID)

			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)

			if response.Code != testCase.wantStatus {
				t.Fatalf("expected status %d, got %d", testCase.wantStatus, response.Code)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}
func (f *fakeAssetUsecase) ImportSample(ctx context.Context, orgID, userID string, payload []byte, dryRun bool) (domain.ImportSummary, error) {
	return domain.ImportSummary{}, nil
}
