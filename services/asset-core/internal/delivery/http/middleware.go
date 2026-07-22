package http

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type correlationResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *correlationResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *correlationResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// WithRequestCorrelation establishes a trace boundary and emits one safe
// structured completion record for every Asset Core request.
func WithRequestCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID, validTraceparent := requestcontext.ParseTraceparent(r.Header.Get("traceparent"))
		if !validTraceparent {
			traceID = requestcontext.TraceID(r.Context())
		}
		requestID := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if !requestIDPattern.MatchString(requestID) {
			requestID = requestcontext.TraceID(r.Context())
		}

		startedAt := time.Now()
		correlation := &requestcontext.Correlation{
			TraceID:   traceID,
			RequestID: requestID,
			StartedAt: startedAt,
		}
		response := &correlationResponseWriter{ResponseWriter: w}
		next.ServeHTTP(response, r.WithContext(requestcontext.WithCorrelation(r.Context(), correlation)))

		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		result := "success"
		if correlation.ErrorCode == "INTERNAL_ERROR" || status >= http.StatusInternalServerError {
			result = "failure"
		} else if correlation.ErrorCode != "" || status >= http.StatusBadRequest {
			result = "denied"
		}
		slog.Default().Info("request completed",
			"service", ServiceNameAssetCore,
			"traceId", correlation.TraceID,
			"requestId", correlation.RequestID,
			"operation", r.Method+" "+r.URL.Path,
			"durationMs", time.Since(startedAt).Milliseconds(),
			"result", result,
			"errorCode", correlation.ErrorCode,
			"errorNumber", correlation.ErrorNumber,
			"http", map[string]any{"method": r.Method, "route": r.URL.Path, "status": status},
		)
	})
}

// RequireInternalAPI authenticates calls to the internal API with a shared
// service credential. Health remains public so orchestration can probe it.
func RequireInternalAPI(expectedToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/api/") {
			next.ServeHTTP(w, r)
			return
		}

		authorization := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(authorization, "Bearer ")
		if !ok || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) != 1 {
			writeLegacyError(w, r, "invalid internal service credential", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RequireActor ensures that X-User-Id and X-Org-Id are present and valid UUIDs.
func RequireActor(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr := r.Header.Get("X-User-Id")
		orgIDStr := r.Header.Get("X-Org-Id")

		if userIDStr == "" || orgIDStr == "" {
			writeLegacyError(w, r, "missing X-User-Id or X-Org-Id header", http.StatusUnauthorized)
			return
		}

		if err := uuid.Validate(userIDStr); err != nil {
			writeLegacyError(w, r, "malformed X-User-Id", http.StatusBadRequest)
			return
		}

		if err := uuid.Validate(orgIDStr); err != nil {
			writeLegacyError(w, r, "malformed X-Org-Id", http.StatusBadRequest)
			return
		}

		actor := requestcontext.Actor{
			UserID: userIDStr,
			OrgID:  orgIDStr,
		}

		ctx := requestcontext.WithActor(r.Context(), actor)
		next(w, r.WithContext(ctx))
	}
}
