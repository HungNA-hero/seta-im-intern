package http

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

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
			http.Error(w, "invalid internal service credential", http.StatusUnauthorized)
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
			http.Error(w, "missing X-User-Id or X-Org-Id header", http.StatusUnauthorized)
			return
		}

		if err := uuid.Validate(userIDStr); err != nil {
			http.Error(w, "malformed X-User-Id", http.StatusBadRequest)
			return
		}

		if err := uuid.Validate(orgIDStr); err != nil {
			http.Error(w, "malformed X-Org-Id", http.StatusBadRequest)
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
