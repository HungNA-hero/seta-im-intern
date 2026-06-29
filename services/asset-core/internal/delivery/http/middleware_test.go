package http_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	assetHttp "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

func TestRequireActor(t *testing.T) {
	tests := []struct {
		name           string
		userIDHeader   string
		orgIDHeader    string
		expectedStatus int
		expectActor    bool
	}{
		{
			name:           "valid context",
			userIDHeader:   uuid.NewString(),
			orgIDHeader:    uuid.NewString(),
			expectedStatus: http.StatusOK,
			expectActor:    true,
		},
		{
			name:           "missing user header",
			userIDHeader:   "",
			orgIDHeader:    uuid.NewString(),
			expectedStatus: http.StatusUnauthorized,
			expectActor:    false,
		},
		{
			name:           "missing org header",
			userIDHeader:   uuid.NewString(),
			orgIDHeader:    "",
			expectedStatus: http.StatusUnauthorized,
			expectActor:    false,
		},
		{
			name:           "malformed user UUID",
			userIDHeader:   "invalid-uuid",
			orgIDHeader:    uuid.NewString(),
			expectedStatus: http.StatusBadRequest,
			expectActor:    false,
		},
		{
			name:           "malformed org UUID",
			userIDHeader:   uuid.NewString(),
			orgIDHeader:    "invalid-uuid",
			expectedStatus: http.StatusBadRequest,
			expectActor:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedActor requestcontext.Actor
			var actorCaptured bool

			mockHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actor, err := requestcontext.GetActor(r.Context())
				if err == nil {
					capturedActor = actor
					actorCaptured = true
				}
				w.WriteHeader(http.StatusOK)
			})

			handler := assetHttp.RequireActor(mockHandler)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.userIDHeader != "" {
				req.Header.Set("X-User-Id", tt.userIDHeader)
			}
			if tt.orgIDHeader != "" {
				req.Header.Set("X-Org-Id", tt.orgIDHeader)
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}

			if tt.expectActor {
				if !actorCaptured {
					t.Errorf("expected actor to be captured, but it wasn't")
				}
				if capturedActor.UserID != tt.userIDHeader {
					t.Errorf("expected UserID %s, got %s", tt.userIDHeader, capturedActor.UserID)
				}
				if capturedActor.OrgID != tt.orgIDHeader {
					t.Errorf("expected OrgID %s, got %s", tt.orgIDHeader, capturedActor.OrgID)
				}
			} else {
				if actorCaptured {
					t.Errorf("expected no actor to be captured")
				}
			}
		})
	}
}
