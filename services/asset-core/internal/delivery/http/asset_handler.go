package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

type optionalString struct {
	Value *string
	Set   bool
}

func (value *optionalString) UnmarshalJSON(data []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.Value = nil
		return nil
	}

	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

type updateFolderRequest struct {
	Name        optionalString `json:"name"`
	Description optionalString `json:"description"`
}

// NewAssetHandler creates a new instance of AssetHandler.
func NewAssetHandler(mux *http.ServeMux, usecase domain.AssetUsecase, db *gorm.DB) {
	handler := &AssetHandler{
		usecase: usecase,
		db:      db,
	}

	mux.HandleFunc("/healthz", handler.HandleHealth)
	mux.HandleFunc("/internal/api/v1/folders", RequireActor(handler.HandleFolders))
	mux.HandleFunc("/internal/api/v1/facts/folders", RequireActor(handler.HandleFolderFacts))
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

// HandleFolders handles folder read and write operations.
// Query params:
//   - id (optional): fetches a single folder by ID.
//   - orgId (optional if id is present, required otherwise): organization scope.
//   - rootPath (optional): ltree root path; defaults to listing root-level folders if id is not present.
//   - children (optional): "true" to return only direct children of rootPath.
func (h *AssetHandler) HandleFolders(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		http.Error(w, "Missing actor context", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetFolders(w, r, actor)
	case http.MethodPost:
		h.handleCreateFolder(w, r, actor)
	case http.MethodPatch:
		h.handleUpdateFolder(w, r, actor)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AssetHandler) handleGetFolders(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {

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
	var err error

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

func (h *AssetHandler) handleCreateFolder(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		http.Error(w, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		http.Error(w, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var input domain.CreateFolderInput
	if err := decodeJSONBody(r, &input); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	folder, err := h.usecase.CreateFolder(r.Context(), orgID, actor.UserID, input)
	if err != nil {
		h.mapDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "success",
		"folder": folder,
	})
}

func (h *AssetHandler) handleUpdateFolder(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if folderID == "" || orgID == "" {
		http.Error(w, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		http.Error(w, "Invalid folder id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		http.Error(w, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var request updateFolderRequest
	if err := decodeJSONBody(r, &request); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	input := domain.UpdateFolderInput{
		Name:           request.Name.Value,
		NameSet:        request.Name.Set,
		Description:    request.Description.Value,
		DescriptionSet: request.Description.Set,
	}

	folder, err := h.usecase.UpdateFolder(r.Context(), orgID, actor.UserID, folderID, input)
	if err != nil {
		h.mapDomainError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status": "success",
		"folder": folder,
	})
}

func (h *AssetHandler) mapDomainError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrFolderNotFound) {
		http.Error(w, "Folder not found", http.StatusNotFound)
	} else if errors.Is(err, domain.ErrFolderConflict) {
		http.Error(w, "Conflict: sibling name or path already exists", http.StatusConflict)
	} else if errors.Is(err, domain.ErrInvalidInput) {
		http.Error(w, "Invalid input", http.StatusBadRequest)
	} else {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}

func decodeJSONBody(r *http.Request, destination any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
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

// HandleFolderFacts returns lightweight authorization facts for a folder.
// Query params:
//   - orgId (required and must match the actor organization)
//   - id (required)
func (h *AssetHandler) HandleFolderFacts(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		http.Error(w, "Missing actor context", http.StatusInternalServerError)
		return
	}

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		http.Error(w, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		http.Error(w, "Organization context mismatch", http.StatusForbidden)
		return
	}

	folderID := r.URL.Query().Get("id")
	if folderID == "" {
		http.Error(w, "Missing id", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		http.Error(w, "Invalid folder id format", http.StatusBadRequest)
		return
	}

	folder, err := h.usecase.GetFolderByID(r.Context(), actor.OrgID, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, domain.ErrFolderNotFound) {
			http.Error(w, "Folder not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"resource_type": "folder",
		"id":            folder.ID,
		"org_id":        folder.OrgID,
		"active":        true,
	})
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "Failed to encode response", http.StatusInternalServerError)
	}
}
