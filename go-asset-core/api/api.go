package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/models"
)

// Server holds the dependencies for the API.
type Server struct {
	DB *gorm.DB
}

// NewServer creates a new API server instance.
func NewServer(db *gorm.DB) *Server {
	return &Server{DB: db}
}

// RegisterRoutes attaches the internal HTTP/JSON API skeleton routes.
func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/internal/api/v1/folders", s.handleFolders)
	mux.HandleFunc("/internal/api/v1/metadata-items", s.handleMetadataItems)
}

// GetFolderTree example endpoint logic to get a subtree for a specific org.
func (s *Server) GetFolderTree(orgID string, rootPath string) ([]models.Folder, error) {
	if s.DB == nil {
		return nil, errors.New("database is not connected")
	}

	var folders []models.Folder

	// Using PostgreSQL ltree <@ operator to find all descendants of rootPath.
	// We MUST filter by org_id as requested by the Mentor Feedback.
	err := s.DB.Where("org_id = ? AND path <@ ? AND deleted_at IS NULL", orgID, rootPath).Find(&folders).Error
	return folders, err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	s.RegisterRoutes(mux)
	mux.ServeHTTP(w, r)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := "ok"
	dbConnected := s.DB != nil
	if !dbConnected {
		status = "degraded"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"db_connected": dbConnected,
	})
}

func (s *Server) handleFolders(w http.ResponseWriter, r *http.Request) {
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

	folders, err := s.GetFolderTree(orgID, rootPath)
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

func (s *Server) handleMetadataItems(w http.ResponseWriter, r *http.Request) {
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
