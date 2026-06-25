package api

import (
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

// GetFolderTree example endpoint logic to get a subtree for a specific org.
// In a real implementation, this would be wrapped in HTTP handlers or gRPC methods.
func (s *Server) GetFolderTree(orgID string, rootPath string) ([]models.Folder, error) {
	var folders []models.Folder

	// Using PostgreSQL ltree <@ operator to find all descendants of rootPath.
	// We MUST filter by org_id as requested by the Mentor Feedback.
	err := s.DB.Where("org_id = ? AND path <@ ?", orgID, rootPath).Find(&folders).Error
	return folders, err
}

// Internal HTTP Handler Skeleton

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ... Setup chi/mux router here ...
	// Example route: GET /internal/orgs/{orgId}/folders/{rootPath}
}
