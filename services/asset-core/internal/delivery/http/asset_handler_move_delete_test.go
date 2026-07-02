package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	delivery "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/domain"
)

func TestHandleMoveFolder_Success(t *testing.T) {
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()
	destParentID := uuid.NewString()

	fakeUsecase := &fakeAssetUsecase{
		moveFolderFunc: func(ctx context.Context, oid, uid, fid string, in domain.MoveFolderInput) (domain.Folder, error) {
			if oid != orgID || uid != userID || fid != folderID {
				t.Errorf("unexpected args: %s, %s, %s", oid, uid, fid)
			}
			if in.DestinationParentID == nil || *in.DestinationParentID != destParentID {
				t.Errorf("unexpected input dest")
			}
			return domain.Folder{ID: folderID, Path: "new.path", Name: "Moved"}, nil
		},
	}

	mux := http.NewServeMux()
	delivery.NewAssetHandler(mux, fakeUsecase, nil)

	reqBody, _ := json.Marshal(domain.MoveFolderInput{DestinationParentID: &destParentID})
	req := httptest.NewRequest(http.MethodPatch, "/internal/api/v1/folders/move?id="+folderID+"&orgId="+orgID, bytes.NewReader(reqBody))
	req.Header.Set("x-user-id", userID)
	req.Header.Set("x-org-id", orgID)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestHandleMoveFolder_InvalidDestination(t *testing.T) {
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()
	emptyDest := ""

	fakeUsecase := &fakeAssetUsecase{}
	mux := http.NewServeMux()
	delivery.NewAssetHandler(mux, fakeUsecase, nil)

	reqBody, _ := json.Marshal(domain.MoveFolderInput{DestinationParentID: &emptyDest})
	req := httptest.NewRequest(http.MethodPatch, "/internal/api/v1/folders/move?id="+folderID+"&orgId="+orgID, bytes.NewReader(reqBody))
	req.Header.Set("x-user-id", userID)
	req.Header.Set("x-org-id", orgID)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for empty dest, got %d", rr.Code)
	}
}

func TestHandleDeleteFolder_Success(t *testing.T) {
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()

	fakeUsecase := &fakeAssetUsecase{
		deleteFolderFunc: func(ctx context.Context, oid, uid, fid string) error {
			if oid != orgID || uid != userID || fid != folderID {
				t.Errorf("unexpected args")
			}
			return nil
		},
	}

	mux := http.NewServeMux()
	delivery.NewAssetHandler(mux, fakeUsecase, nil)

	req := httptest.NewRequest(http.MethodDelete, "/internal/api/v1/folders?id="+folderID+"&orgId="+orgID, nil)
	req.Header.Set("x-user-id", userID)
	req.Header.Set("x-org-id", orgID)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rr.Code)
	}
}

func TestHandleMoveFolder_ErrorMapping(t *testing.T) {
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()
	destParentID := uuid.NewString()

	cases := []struct {
		name       string
		errReturn  error
		expectCode int
	}{
		{"not found", domain.ErrFolderNotFound, http.StatusNotFound},
		{"conflict", domain.ErrFolderConflict, http.StatusConflict},
		{"cycle", domain.ErrCycleDetected, http.StatusConflict},
		{"internal", context.DeadlineExceeded, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeUsecase := &fakeAssetUsecase{
				moveFolderFunc: func(ctx context.Context, oid, uid, fid string, in domain.MoveFolderInput) (domain.Folder, error) {
					return domain.Folder{}, tc.errReturn
				},
			}
			mux := http.NewServeMux()
			delivery.NewAssetHandler(mux, fakeUsecase, nil)

			reqBody, _ := json.Marshal(domain.MoveFolderInput{DestinationParentID: &destParentID})
			req := httptest.NewRequest(http.MethodPatch, "/internal/api/v1/folders/move?id="+folderID+"&orgId="+orgID, bytes.NewReader(reqBody))
			req.Header.Set("x-user-id", userID)
			req.Header.Set("x-org-id", orgID)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.expectCode {
				t.Errorf("expected %d, got %d", tc.expectCode, rr.Code)
			}
		})
	}
}

func TestHandleDeleteFolder_ErrorMapping(t *testing.T) {
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()

	cases := []struct {
		name       string
		errReturn  error
		expectCode int
	}{
		{"not empty", domain.ErrFolderNotEmpty, http.StatusConflict},
		{"not found", domain.ErrFolderNotFound, http.StatusNotFound},
		{"internal", context.DeadlineExceeded, http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeUsecase := &fakeAssetUsecase{
				deleteFolderFunc: func(ctx context.Context, oid, uid, fid string) error {
					return tc.errReturn
				},
			}
			mux := http.NewServeMux()
			delivery.NewAssetHandler(mux, fakeUsecase, nil)

			req := httptest.NewRequest(http.MethodDelete, "/internal/api/v1/folders?id="+folderID+"&orgId="+orgID, nil)
			req.Header.Set("x-user-id", userID)
			req.Header.Set("x-org-id", orgID)

			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.expectCode {
				t.Errorf("expected %d, got %d", tc.expectCode, rr.Code)
			}
		})
	}
}
