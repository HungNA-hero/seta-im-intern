package http

import (
	"net/http"

	"github.com/google/uuid"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

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
