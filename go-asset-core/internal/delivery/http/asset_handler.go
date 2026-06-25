package http

import (
	"encoding/json"
	"net/http"

	"gorm.io/gorm"
	"seta-im-intern/go-asset-core/internal/domain"
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
	mux.HandleFunc("/internal/api/v1/folders", handler.HandleFolders)
	mux.HandleFunc("/internal/api/v1/metadata-items", handler.HandleMetadataItems)
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

func (h *AssetHandler) HandleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orgID := r.URL.Query().Get("orgId")
	rootPath := r.URL.Query().Get("rootPath")
	if orgID == "" || rootPath == "" {
		http.Error(w, "Missing orgId or rootPath", http.StatusBadRequest)
		return
	}

	folders, err := h.usecase.GetFolderTree(r.Context(), orgID, rootPath)
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
