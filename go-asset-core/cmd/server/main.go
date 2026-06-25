package main

import (
	"log"
	"net/http"
	"os"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/api"
	"seta-im-intern/go-asset-core/models"
)

func main() {
	// 1. Setup Database Connection
	// In production, use environment variables. For now, hardcode to match Docker Compose defaults.
	dsn := "host=localhost user=asset_user password=asset_pass dbname=asset_db port=5433 sslmode=disable"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		log.Printf("Failed to connect to database: %v. Server will start but DB queries will fail.", err)
	} else {
		log.Println("Connected to Asset DB successfully")
		// Optional: AutoMigrate if not using Flyway. We use Flyway, so we skip AutoMigrate.
		_ = db.AutoMigrate(&models.OrganizationRef{}, &models.Folder{}, &models.MetadataItem{})
	}

	// 2. Initialize API Server
	server := api.NewServer(db)

	// 3. Setup Routes
	mux := http.NewServeMux()
	
	// Example endpoint for GetFolderTree
	// Route: GET /internal/api/v1/folders
	// Requires query params: orgId and rootPath
	mux.HandleFunc("/internal/api/v1/folders", func(w http.ResponseWriter, r *http.Request) {
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

		folders, err := server.GetFolderTree(orgID, rootPath)
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		// Simple JSON response (in a real app, use json.NewEncoder)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "success", "count": ` + string(len(folders)) + `}`))
	})

	// 4. Start Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Go Asset Core Internal API listening on port %s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}
