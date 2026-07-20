package http_test

import (
	"encoding/json"
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

func TestRequestCorrelationWrapsAuthenticationFailuresInSafeEnvelope(t *testing.T) {
	handler := assetHttp.WithRequestCorrelation(assetHttp.RequireInternalAPI("token", http.NotFoundHandler()))
	traceID := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	req := httptest.NewRequest(http.MethodGet, "/internal/api/v1/folders", nil)
	req.Header.Set("traceparent", "00-"+traceID+"-0123456789abcdef-01")
	req.Header.Set("X-Request-Id", "kan-57-middleware-test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, req)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("expected JSON response, got %q", got)
	}
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Number  int    `json:"number"`
			TraceID string `json:"traceId"`
			Service string `json:"service"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode safe error response: %v", err)
	}
	if body.Error.Code != "UNAUTHENTICATED" || body.Error.Number != 2001 {
		t.Fatalf("unexpected error identity: %#v", body.Error)
	}
	if body.Error.TraceID != traceID || body.Error.Service != "asset-core" {
		t.Fatalf("correlation was not preserved: %#v", body.Error)
	}
}

func TestRequireInternalAPI(t *testing.T) {
	const token = "test-internal-token"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := assetHttp.RequireInternalAPI(token, next)

	tests := []struct {
		name           string
		path           string
		authorization  string
		expectedStatus int
	}{
		{"public health remains available", "/healthz", "", http.StatusNoContent},
		{"missing credential is denied", "/internal/api/v1/folders", "", http.StatusUnauthorized},
		{"wrong credential is denied", "/internal/api/v1/folders", "Bearer wrong", http.StatusUnauthorized},
		{"valid credential is accepted", "/internal/api/v1/folders", "Bearer " + token, http.StatusNoContent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tt.expectedStatus {
				t.Fatalf("expected status %d, got %d", tt.expectedStatus, rr.Code)
			}
		})
	}
}
