package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	httpDelivery "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

func main() {
	for _, p := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(p)
	}

	// 1. Setup Database Connection
	db, err := openAssetDB(assetDSNFromEnv())
	if err != nil {
		log.Printf("Failed to connect to database: %v. Server will start but DB queries will fail.", err)
	} else {
		log.Println("Connected to Asset DB successfully")
	}

	// 2. Setup Clean Architecture Layers
	assetRepo := repository.NewAssetRepository(db)
	assetUsecase := usecase.NewAssetUsecase(assetRepo)

	// 3. Setup Routes and Handlers
	muxPtr := http.NewServeMux()

	httpDelivery.NewAssetHandler(muxPtr, assetUsecase, db)

	// 4. Start Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Go Asset Core Internal API listening on port %s", port)
	if err := http.ListenAndServe(":"+port, muxPtr); err != nil {
		log.Fatalf("Server failed to start: %v", err)
	}
}

func openAssetDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}

	return db, nil
}

func assetDSNFromEnv() string {
	return fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		getenv("ASSET_DB_HOST", "localhost"),
		getenv("ASSET_DB_USER", "asset_user"),
		getenv("ASSET_DB_PASSWORD", "asset_password"),
		getenv("ASSET_DB_NAME", "asset_db"),
		getenv("ASSET_DB_PORT", "5433"),
		getenv("ASSET_DB_SSLMODE", "disable"),
	)
}

func getenv(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
