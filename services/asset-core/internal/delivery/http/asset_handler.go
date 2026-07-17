package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/requestcontext"
)

// AssetHandler handles HTTP requests for assets.
type AssetHandler struct {
	usecase domain.AssetUsecase
	db      *gorm.DB // Kept for healthcheck
}

// optionalString preserves whether a nullable JSON string was omitted or explicitly provided.
type optionalString struct {
	Value *string
	Set   bool
}

// UnmarshalJSON records field presence while preserving explicit null as a nil value.
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

// optionalStringArray preserves omitted, explicit-null, and provided array PATCH states.
type optionalStringArray struct {
	Value *[]string
	Set   bool
}

// UnmarshalJSON records array field presence and leaves explicit null distinguishable from omission.
func (value *optionalStringArray) UnmarshalJSON(data []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded []string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.Value = &decoded
	return nil
}

// optionalRawMessage preserves metadata_json PATCH presence without weakening JSON syntax validation.
type optionalRawMessage struct {
	Value *json.RawMessage
	Set   bool
}

// UnmarshalJSON records metadata_json presence and copies a syntactically valid raw JSON value.
func (value *optionalRawMessage) UnmarshalJSON(data []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded json.RawMessage
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

// updateMetadataRequest mirrors every sparse metadata field so omitted and explicit-null values remain distinct.
type updateMetadataRequest struct {
	Title          optionalString      `json:"title"`
	Description    optionalString      `json:"description"`
	Labels         optionalStringArray `json:"labels"`
	Category       optionalString      `json:"category"`
	ExternalSource optionalString      `json:"external_source"`
	ExternalID     optionalString      `json:"external_id"`
	SourceURL      optionalString      `json:"source_url"`
	ThumbnailURL   optionalString      `json:"thumbnail_url"`
	License        optionalString      `json:"license"`
	Author         optionalString      `json:"author"`
	MetadataJSON   optionalRawMessage  `json:"metadata_json"`
	Notes          optionalString      `json:"notes"`
}

// NewAssetHandler creates a new instance of AssetHandler.
func NewAssetHandler(mux *http.ServeMux, usecase domain.AssetUsecase, db *gorm.DB) {
	handler := &AssetHandler{
		usecase: usecase,
		db:      db,
	}

	mux.HandleFunc("/healthz", handler.HandleHealth)
	mux.HandleFunc("/internal/api/v1/folders", RequireActor(handler.HandleFolders))
	mux.HandleFunc("/internal/api/v1/folders/move", RequireActor(handler.HandleMoveFolder))
	mux.HandleFunc("/internal/api/v1/facts/folders", RequireActor(handler.HandleFolderFacts))
	mux.HandleFunc("/internal/api/v1/metadata-items", RequireActor(handler.HandleMetadataItems))
	mux.HandleFunc("/internal/api/v1/metadata-items/search", RequireActor(handler.HandleSearchMetadataItems))
}

func (h *AssetHandler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := "ok"
	dbConnected := h.db != nil
	if !dbConnected {
		status = "degraded"
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
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
		writeLegacyError(w, r, "Missing actor context", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetFolders(w, r, actor)
	case http.MethodPost:
		h.handleCreateFolder(w, r, actor)
	case http.MethodPatch:
		h.handleUpdateFolder(w, r, actor)
	case http.MethodDelete:
		h.handleDeleteFolder(w, r, actor)
	default:
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *AssetHandler) handleGetFolders(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {

	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")

	// If orgId is provided, it MUST match the actor's org.
	if orgID != "" && orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	// 1. Detail request (by ID)
	if folderID != "" {
		if err := uuid.Validate(folderID); err != nil {
			writeLegacyError(w, r, "Invalid folder id format", http.StatusBadRequest)
			return
		}

		folder, err := h.usecase.GetFolderByID(r.Context(), actor.OrgID, folderID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				writeLegacyError(w, r, "Folder not found", http.StatusNotFound)
				return
			}
			writeLegacyError(w, r, "Database error", http.StatusInternalServerError)
			return
		}

		writeJSON(w, r, http.StatusOK, map[string]any{
			"status": "success",
			"folder": folder,
		})
		return
	}

	// 2. List request
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}

	rootPath := r.URL.Query().Get("rootPath")
	childrenOnly := r.URL.Query().Get("children") == "true"
	fullTree := r.URL.Query().Get("tree") == "true"

	var folders []domain.Folder
	var err error

	if childrenOnly {
		if rootPath == "" {
			writeLegacyError(w, r, "rootPath is required when children=true", http.StatusBadRequest)
			return
		}
		folders, err = h.usecase.GetFolderChildren(r.Context(), orgID, rootPath)
	} else if rootPath != "" || fullTree {
		folders, err = h.usecase.GetFolderTree(r.Context(), orgID, rootPath)
	} else {
		folders, err = h.usecase.GetRootFolders(r.Context(), orgID)
	}

	if err != nil {
		writeLegacyError(w, r, "Database error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status":  "success",
		"count":   len(folders),
		"folders": folders,
	})
}

func (h *AssetHandler) handleCreateFolder(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var input domain.CreateFolderInput
	if err := decodeJSONBody(r, &input); err != nil {
		writeLegacyError(w, r, "Invalid body", http.StatusBadRequest)
		return
	}

	folder, err := h.usecase.CreateFolder(r.Context(), orgID, actor.UserID, input)
	if err != nil {
		h.mapDomainError(w, r, err, "BAD_REQUEST")
		return
	}

	writeJSON(w, r, http.StatusCreated, map[string]any{
		"status": "success",
		"folder": folder,
	})
}

func (h *AssetHandler) handleUpdateFolder(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if folderID == "" || orgID == "" {
		writeLegacyError(w, r, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		writeLegacyError(w, r, "Invalid folder id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var request updateFolderRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeLegacyError(w, r, "Invalid body", http.StatusBadRequest)
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
		h.mapDomainError(w, r, err, "BAD_REQUEST")
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status": "success",
		"folder": folder,
	})
}

// handleDeleteFolder processes DELETE requests to soft-delete a specific folder.
func (h *AssetHandler) handleDeleteFolder(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if folderID == "" || orgID == "" {
		writeLegacyError(w, r, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		writeLegacyError(w, r, "Invalid folder id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	if err := h.usecase.DeleteFolder(r.Context(), orgID, actor.UserID, folderID); err != nil {
		h.mapDomainError(w, r, err, "BAD_REQUEST")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleMoveFolder processes PATCH requests to move a folder to a new parent or to the organization root.
func (h *AssetHandler) HandleMoveFolder(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		writeLegacyError(w, r, "Missing actor context", http.StatusInternalServerError)
		return
	}

	if r.Method != http.MethodPatch {
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	folderID := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if folderID == "" || orgID == "" {
		writeLegacyError(w, r, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		writeLegacyError(w, r, "Invalid folder id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var input domain.MoveFolderInput
	if err := decodeJSONBody(r, &input); err != nil {
		writeLegacyError(w, r, "Invalid body", http.StatusBadRequest)
		return
	}

	if input.DestinationParentID != nil {
		if *input.DestinationParentID == "" {
			writeLegacyError(w, r, "Invalid destination parent id format", http.StatusBadRequest)
			return
		}
		if err := uuid.Validate(*input.DestinationParentID); err != nil {
			writeLegacyError(w, r, "Invalid destination parent id format", http.StatusBadRequest)
			return
		}
	}

	folder, err := h.usecase.MoveFolder(r.Context(), orgID, actor.UserID, folderID, input)
	if err != nil {
		h.mapDomainError(w, r, err, "BAD_REQUEST")
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status": "success",
		"folder": folder,
	})
}

// mapDomainError converts typed domain failures into stable internal REST status codes.
// The caller supplies the resource-specific invalid-input code because the shared domain
// sentinel is intentionally used by both folder and metadata validation paths.
func (h *AssetHandler) mapDomainError(w http.ResponseWriter, r *http.Request, err error, invalidInputCode string) {
	switch {
	case errors.Is(err, domain.ErrFolderNotFound):
		writeError(w, r, http.StatusNotFound, "FOLDER_NOT_FOUND")
	case errors.Is(err, domain.ErrMetadataNotFound):
		writeError(w, r, http.StatusNotFound, "METADATA_NOT_FOUND")
	case errors.Is(err, domain.ErrFolderConflict):
		writeError(w, r, http.StatusConflict, "FOLDER_NAME_CONFLICT")
	case errors.Is(err, domain.ErrFolderNotEmpty):
		writeError(w, r, http.StatusConflict, "FOLDER_NOT_EMPTY")
	case errors.Is(err, domain.ErrCycleDetected):
		writeError(w, r, http.StatusConflict, "FOLDER_CYCLE_DETECTED")
	case errors.Is(err, domain.ErrMetadataConflict):
		writeError(w, r, http.StatusConflict, "METADATA_IDENTITY_CONFLICT")
	case errors.Is(err, domain.ErrCursorInvalid):
		writeError(w, r, http.StatusBadRequest, "CURSOR_INVALID")
	case errors.Is(err, domain.ErrInvalidInput):
		writeError(w, r, http.StatusBadRequest, invalidInputCode)
	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR")
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

// HandleMetadataItems dispatches org-scoped metadata list, detail, create, and update requests.
func (h *AssetHandler) HandleMetadataItems(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		writeLegacyError(w, r, "Missing actor context", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleGetMetadataItems(w, r, actor)
	case http.MethodPost:
		h.handleCreateMetadataItem(w, r, actor)
	case http.MethodPatch:
		h.handleUpdateMetadataItem(w, r, actor)
	case http.MethodDelete:
		h.handleDeleteMetadataItem(w, r, actor)
	default:
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleGetMetadataItems serves either one metadata detail or an active-folder metadata list.
func (h *AssetHandler) handleGetMetadataItems(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	id := r.URL.Query().Get("id")
	folderID := r.URL.Query().Get("folderId")
	if id != "" && folderID != "" {
		writeLegacyError(w, r, "Provide either id or folderId, not both", http.StatusBadRequest)
		return
	}
	if id != "" {
		if err := uuid.Validate(id); err != nil {
			writeLegacyError(w, r, "Invalid id format", http.StatusBadRequest)
			return
		}
		item, err := h.usecase.GetMetadataItemByID(r.Context(), orgID, id)
		if err != nil {
			h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
			return
		}
		writeJSON(w, r, http.StatusOK, map[string]any{
			"status": "success",
			"item":   item,
		})
		return
	}

	if folderID != "" {
		if err := uuid.Validate(folderID); err != nil {
			writeLegacyError(w, r, "Invalid folderId format", http.StatusBadRequest)
			return
		}
		items, err := h.usecase.GetMetadataItemsByFolder(r.Context(), orgID, folderID)
		if err != nil {
			h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
			return
		}
		if items == nil {
			items = []domain.MetadataItem{}
		}
		writeJSON(w, r, http.StatusOK, map[string]any{
			"status": "success",
			"count":  len(items),
			"items":  items,
		})
		return
	}

	writeLegacyError(w, r, "Missing id or folderId", http.StatusBadRequest)
}

// handleCreateMetadataItem validates transport context before delegating transactional creation to the use case.
func (h *AssetHandler) handleCreateMetadataItem(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var input domain.CreateMetadataInput
	if err := decodeJSONBody(r, &input); err != nil {
		writeLegacyError(w, r, "Invalid body", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(input.FolderID); err != nil {
		writeLegacyError(w, r, "Invalid folder_id format", http.StatusBadRequest)
		return
	}

	item, err := h.usecase.CreateMetadataItem(r.Context(), orgID, actor.UserID, input)
	if err != nil {
		h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
		return
	}

	writeJSON(w, r, http.StatusCreated, map[string]any{
		"status": "success",
		"item":   item,
	})
}

// handleUpdateMetadataItem translates PATCH presence semantics into the domain update contract.
func (h *AssetHandler) handleUpdateMetadataItem(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	id := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if id == "" || orgID == "" {
		writeLegacyError(w, r, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(id); err != nil {
		writeLegacyError(w, r, "Invalid metadata id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	var request updateMetadataRequest
	if err := decodeJSONBody(r, &request); err != nil {
		writeLegacyError(w, r, "Invalid body", http.StatusBadRequest)
		return
	}

	var pqLabels *pq.StringArray
	if request.Labels.Set {
		labels := pq.StringArray{}
		if request.Labels.Value != nil {
			labels = pq.StringArray(*request.Labels.Value)
		}
		// Both labels:null and labels:[] intentionally clear to the required empty PostgreSQL array.
		pqLabels = &labels
	}

	input := domain.UpdateMetadataInput{
		Title:             request.Title.Value,
		TitleSet:          request.Title.Set,
		Description:       request.Description.Value,
		DescriptionSet:    request.Description.Set,
		Labels:            pqLabels,
		LabelsSet:         request.Labels.Set,
		Category:          request.Category.Value,
		CategorySet:       request.Category.Set,
		ExternalSource:    request.ExternalSource.Value,
		ExternalSourceSet: request.ExternalSource.Set,
		ExternalID:        request.ExternalID.Value,
		ExternalIDSet:     request.ExternalID.Set,
		SourceURL:         request.SourceURL.Value,
		SourceURLSet:      request.SourceURL.Set,
		ThumbnailURL:      request.ThumbnailURL.Value,
		ThumbnailURLSet:   request.ThumbnailURL.Set,
		License:           request.License.Value,
		LicenseSet:        request.License.Set,
		Author:            request.Author.Value,
		AuthorSet:         request.Author.Set,
		MetadataJSON:      request.MetadataJSON.Value,
		MetadataJSONSet:   request.MetadataJSON.Set,
		Notes:             request.Notes.Value,
		NotesSet:          request.Notes.Set,
	}

	item, err := h.usecase.UpdateMetadataItem(r.Context(), orgID, actor.UserID, id, input)
	if err != nil {
		h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
		return
	}

	writeJSON(w, r, http.StatusOK, map[string]any{
		"status": "success",
		"item":   item,
	})
}

// handleDeleteMetadataItem processes DELETE requests to soft-delete a specific metadata item.
func (h *AssetHandler) handleDeleteMetadataItem(w http.ResponseWriter, r *http.Request, actor requestcontext.Actor) {
	id := r.URL.Query().Get("id")
	orgID := r.URL.Query().Get("orgId")
	if id == "" || orgID == "" {
		writeLegacyError(w, r, "Missing id or orgId", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(id); err != nil {
		writeLegacyError(w, r, "Invalid metadata id format", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	if err := h.usecase.DeleteMetadataItem(r.Context(), orgID, actor.UserID, id); err != nil {
		h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// HandleSearchMetadataItems processes metadata search queries.
func (h *AssetHandler) HandleSearchMetadataItems(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		writeLegacyError(w, r, "Missing actor context", http.StatusInternalServerError)
		return
	}

	if r.Method != http.MethodGet {
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	queryValues := r.URL.Query()
	keyset := queryValues.Get("cursor") == "true"
	filter := domain.MetadataSearchFilter{
		Labels: queryValues["label"],
		Keyset: keyset,
	}

	if queryValues.Has("folderId") {
		fID := queryValues.Get("folderId")
		if fID != "" {
			if err := uuid.Validate(fID); err != nil {
				writeLegacyError(w, r, "Invalid folderId format", http.StatusBadRequest)
				return
			}
		}
		filter.FolderID = &fID
	}
	if queryValues.Has("query") {
		q := queryValues.Get("query")
		filter.Query = &q
	}
	if queryValues.Has("category") {
		c := queryValues.Get("category")
		filter.Category = &c
	}
	if queryValues.Has("externalSource") {
		es := queryValues.Get("externalSource")
		filter.ExternalSource = &es
	}

	limitStr := queryValues.Get("limit")
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil {
			filter.Limit = l
		} else {
			writeLegacyError(w, r, "Invalid limit format", http.StatusBadRequest)
			return
		}
	} else {
		filter.Limit = 50
	}

	offsetStr := queryValues.Get("offset")
	if offsetStr != "" {
		if o, err := strconv.Atoi(offsetStr); err == nil {
			filter.Offset = o
		} else {
			writeLegacyError(w, r, "Invalid offset format", http.StatusBadRequest)
			return
		}
	}

	if keyset {
		if filter.FolderID == nil || *filter.FolderID == "" || filter.Offset != 0 || filter.Limit < 1 || filter.Limit > 101 {
			writeError(w, r, http.StatusBadRequest, "CURSOR_INVALID")
			return
		}

		hasAfterUpdatedAt := queryValues.Has("afterUpdatedAt")
		hasAfterID := queryValues.Has("afterId")
		if hasAfterUpdatedAt != hasAfterID {
			writeError(w, r, http.StatusBadRequest, "CURSOR_INVALID")
			return
		}
		if hasAfterUpdatedAt {
			updatedAt, parseErr := time.Parse(time.RFC3339Nano, queryValues.Get("afterUpdatedAt"))
			if parseErr != nil || uuid.Validate(queryValues.Get("afterId")) != nil {
				writeError(w, r, http.StatusBadRequest, "CURSOR_INVALID")
				return
			}
			afterID := queryValues.Get("afterId")
			filter.AfterUpdatedAt = &updatedAt
			filter.AfterID = &afterID
		}

		// Fetch one physical row beyond the requested candidate batch so Access
		// Core can continue authorization traversal without using deep offsets.
		filter.Limit++
	}

	items, err := h.usecase.SearchMetadataItems(r.Context(), orgID, filter)
	if err != nil {
		h.mapDomainError(w, r, err, "METADATA_VALIDATION_ERROR")
		return
	}

	if items == nil {
		items = []domain.MetadataItem{}
	}
	hasMore := false
	if keyset && len(items) > filter.Limit-1 {
		hasMore = true
		items = items[:filter.Limit-1]
	}

	response := map[string]any{
		"status": "success",
		"count":  len(items),
		"items":  items,
	}
	if keyset {
		response["hasMore"] = hasMore
	}
	writeJSON(w, r, http.StatusOK, response)
}

// HandleFolderFacts returns lightweight authorization facts for a folder.
// Query params:
//   - orgId (required and must match the actor organization)
//   - id (required)
func (h *AssetHandler) HandleFolderFacts(w http.ResponseWriter, r *http.Request) {
	actor, err := requestcontext.GetActor(r.Context())
	if err != nil {
		writeLegacyError(w, r, "Missing actor context", http.StatusInternalServerError)
		return
	}

	if r.Method != http.MethodGet {
		writeLegacyError(w, r, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orgID := r.URL.Query().Get("orgId")
	if orgID == "" {
		writeLegacyError(w, r, "Missing orgId", http.StatusBadRequest)
		return
	}
	if orgID != actor.OrgID {
		writeLegacyError(w, r, "Organization context mismatch", http.StatusForbidden)
		return
	}

	folderID := r.URL.Query().Get("id")
	if folderID == "" {
		writeLegacyError(w, r, "Missing id", http.StatusBadRequest)
		return
	}
	if err := uuid.Validate(folderID); err != nil {
		writeLegacyError(w, r, "Invalid folder id format", http.StatusBadRequest)
		return
	}

	folder, err := h.usecase.GetFolderByID(r.Context(), actor.OrgID, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, domain.ErrFolderNotFound) {
			writeLegacyError(w, r, "Folder not found", http.StatusNotFound)
			return
		}
		writeLegacyError(w, r, "Database error", http.StatusInternalServerError)
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

func writeJSON(w http.ResponseWriter, r *http.Request, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The response is already committed, so only record the serialization failure.
		requestcontext.RecordError(r.Context(), "INTERNAL_ERROR", 1000)
	}
}
