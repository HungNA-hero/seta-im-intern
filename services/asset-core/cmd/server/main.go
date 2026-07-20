package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	httpDelivery "seta-im-intern/go-asset-core/internal/delivery/http"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	for _, p := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(p)
	}
	internalAPIToken := strings.TrimSpace(os.Getenv("ASSET_INTERNAL_API_TOKEN"))
	if internalAPIToken == "" {
		slog.Error("ASSET_INTERNAL_API_TOKEN must be configured before Asset Core starts")
		os.Exit(1)
	}

	// 1. Setup Database Connection
	db, err := openAssetDB(assetDSNFromEnv())
	if err != nil {
		slog.Error("database connection failed; server will start but DB queries will fail", "error", err.Error())
	} else {
		slog.Info("connected to Asset DB successfully")
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

	slog.Info("Go Asset Core Internal API listening", "port", port)
	handler := httpDelivery.WithRequestCorrelation(httpDelivery.RequireInternalAPI(internalAPIToken, muxPtr))
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.ListenAndServe()
	}()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case sig := <-signals:
		slog.Info("shutdown requested", "signal", sig.String())
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "error", err.Error())
			_ = server.Close()
		}
		if db != nil {
			if sqlDB, err := db.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		slog.Info("graceful shutdown complete")
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			slog.Error("server failed", "error", err.Error())
			os.Exit(1)
		}
	}
}

func openAssetDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
