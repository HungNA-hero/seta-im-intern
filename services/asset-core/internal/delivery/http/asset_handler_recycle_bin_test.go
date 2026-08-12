package http_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	assetHTTP "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

func TestHandleRecycleBin_CandidatePage(t *testing.T) {
	mux := http.NewServeMux()
	orgID := uuid.NewString()
	usecase := &fakeAssetUsecase{recycleBinEntries: []domain.RecycleBinEntry{
		{LifecycleUnitID: uuid.NewString(), ResourceType: domain.LifecycleResourceFolder, ResourceID: uuid.NewString(), DisplayName: "A"},
		{LifecycleUnitID: uuid.NewString(), ResourceType: domain.LifecycleResourceMetadata, ResourceID: uuid.NewString(), DisplayName: "B"},
		{LifecycleUnitID: uuid.NewString(), ResourceType: domain.LifecycleResourceFolder, ResourceID: uuid.NewString(), DisplayName: "C"},
	}}
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/api/v1/recycle-bin?orgId="+orgID+"&limit=2", nil)
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", orgID)
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", response.Code)
	}
	if usecase.methodCalled != "ListRecycleBinEntries" || usecase.recycleBinFilter.Limit != 3 {
		t.Fatalf("expected look-ahead candidate filter, got method=%q filter=%#v", usecase.methodCalled, usecase.recycleBinFilter)
	}
	var payload struct {
		Count   int                      `json:"count"`
		Entries []domain.RecycleBinEntry `json:"entries"`
		HasMore bool                     `json:"hasMore"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Count != 2 || len(payload.Entries) != 2 || !payload.HasMore {
		t.Fatalf("expected trimmed candidate page with hasMore, got %#v", payload)
	}
}

func TestHandleRecycleBin_RejectsMalformedCursor(t *testing.T) {
	mux := http.NewServeMux()
	orgID := uuid.NewString()
	usecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/api/v1/recycle-bin?orgId="+orgID+"&afterDeletedAt=not-a-time&afterId=not-a-uuid", nil)
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", orgID)
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", response.Code)
	}
	var payload struct {
		Error struct {
			Code   string `json:"code"`
			Number int    `json:"number"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != "CURSOR_INVALID" || payload.Error.Number != 1003 || usecase.called {
		t.Fatalf("expected CURSOR_INVALID/1003 before usecase, got error=%#v called=%t", payload.Error, usecase.called)
	}
}

func TestHandleRecycleBin_PassesContinuationTuple(t *testing.T) {
	mux := http.NewServeMux()
	orgID := uuid.NewString()
	usecase := &fakeAssetUsecase{}
	assetHTTP.NewAssetHandler(mux, usecase, nil)

	deletedAt := time.Date(2026, time.August, 12, 3, 0, 0, 0, time.UTC)
	afterID := uuid.NewString()
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/api/v1/recycle-bin?orgId="+orgID+"&afterDeletedAt="+deletedAt.Format(time.RFC3339Nano)+"&afterId="+afterID, nil)
	req.Header.Set("X-User-Id", uuid.NewString())
	req.Header.Set("X-Org-Id", orgID)
	mux.ServeHTTP(response, req)

	if response.Code != http.StatusOK || usecase.recycleBinFilter.AfterDeletedAt == nil || usecase.recycleBinFilter.AfterLifecycleID == nil {
		t.Fatalf("expected valid continuation tuple, got code=%d filter=%#v", response.Code, usecase.recycleBinFilter)
	}
	if !usecase.recycleBinFilter.AfterDeletedAt.Equal(deletedAt) || *usecase.recycleBinFilter.AfterLifecycleID != afterID {
		t.Fatalf("unexpected continuation tuple: %#v", usecase.recycleBinFilter)
	}
}
