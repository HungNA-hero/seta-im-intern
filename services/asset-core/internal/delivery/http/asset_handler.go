package http

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

// AssetHandler handles HTTP requests for assets.
type AssetHandler struct {
	usecase domain.AssetUsecase
	db      *gorm.DB // Kept for healthcheck
}

// NewAssetHandler creates a new instance of AssetHandler.
func NewAssetHandler(mux *http.ServeMux, usecase domain.AssetUsecase, db *gorm.DB) {
	handler := &AssetHandler{
		usecase: usecase,
		db:      db,
	}

	mux.HandleFunc("/healthz", handler.HandleHealth)
	mux.HandleFunc("/internal/api/v1/folders", RequireActor(handler.HandleFolders))
	mux.HandleFunc("/internal/api/v1/metadata-items", RequireActor(handler.HandleMetadataItems))
}

func (h *AssetHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := "ok"
	dbConnected := h.db != nil
	if !dbConnected {
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"db_connected": dbConnected,
	})
}

// HandleFolders handles GET /internal/api/v1/folders
// Query params:
//   - id (optional): fetches a single folder by ID.
//   - orgId (optional if id is present, required otherwise): organization scope.
//   - rootPath (optional): ltree root path; defaults to listing root-level folders if id is not present.
//   - children (optional): "true" to return only direct children of rootPath.
func (h *AssetHandler) HandleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		http.Error(w, "Missing actor context", http.StatusInternalServerError)
		return
	}

	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")

	// If orgId is provided, it MUST match the actor's org.
	if orgID != "" && orgID != actor.OrgID {
		http.Error(w, "Organization context mismatch", http.StatusForbidden)
		return
	}

	// 1. Detail request (by ID)
	if folderID != "" {
		if err := uuid.Validate(folderID); err != nil {
			http.Error(w, "Invalid folder id format", http.StatusBadRequest)
			return
		}

		folder, err := h.usecase.GetFolderByID(r.Context(), actor.OrgID, folderID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				http.Error(w, "Folder not found", http.StatusNotFound)
				return
			}
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status": "success",
			"folder": folder,
		})
		return
	}

	// 2. List request
	if orgID == "" {
		http.Error(w, "Missing orgId", http.StatusBadRequest)
		return
	}

	rootPath := r.URL.Query().Get("rootPath")
	childrenOnly := r.URL.Query().Get("children") == "true"

	var folders []domain.Folder

	if childrenOnly {
		if rootPath == "" {
			http.Error(w, "rootPath is required when children=true", http.StatusBadRequest)
			return
		}
		folders, err = h.usecase.GetFolderChildren(r.Context(), orgID, rootPath)
	} else if rootPath != "" {
		folders, err = h.usecase.GetFolderTree(r.Context(), orgID, rootPath)
	} else {
		folders, err = h.usecase.GetRootFolders(r.Context(), orgID)
	}

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "success",
		"count":   len(folders),
		"folders": folders,
	})
}

func (h *AssetHandler) HandleMetadataItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"status":  "not_implemented",
		"message": "Metadata item routes are reserved for the next Asset Core iteration.",
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
