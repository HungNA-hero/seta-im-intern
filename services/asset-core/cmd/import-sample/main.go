package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

// main maps command failures to a non-zero process exit after run releases resources.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Import failed: %v\n", err)
		os.Exit(1)
	}
}

// run validates command input, owns resource cleanup, and emits the import summary to stdout.
func run() error {
	var (
		filePath    string
		orgID       string
		userID      string
		dryRun      bool
		databaseURL string
	)

	flag.StringVar(&filePath, "file", "", "Absolute or repo-relative JSON dataset file")
	flag.StringVar(&orgID, "org-id", "", "Trusted Developer Organization ID")
	flag.StringVar(&userID, "user-id", "", "Trusted Developer User ID")
	flag.BoolVar(&dryRun, "dry-run", false, "Validate and plan everything, rollback at the end")
	flag.StringVar(&databaseURL, "database-url", "", "Optional explicit database URL")
	flag.Parse()

	if filePath == "" || orgID == "" || userID == "" {
		flag.Usage()
		return fmt.Errorf("--file, --org-id, and --user-id are required")
	}

	if _, err := uuid.Parse(orgID); err != nil {
		return fmt.Errorf("invalid org-id format (must be UUID)")
	}
	if _, err := uuid.Parse(userID); err != nil {
		return fmt.Errorf("invalid user-id format (must be UUID)")
	}

	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	limitReader := io.LimitReader(file, 10*1024*1024)
	payload, err := io.ReadAll(limitReader)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}
	extra := make([]byte, 1)
	if n, _ := file.Read(extra); n > 0 {
		return fmt.Errorf("file size exceeds 10 MiB limit")
	}

	for _, p := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(p)
	}

	dsn := databaseURL
	if dsn == "" {
		dsn = os.Getenv("ASSET_DATABASE_URL")
	}
	if dsn == "" {
		return fmt.Errorf("database URL not provided via --database-url or ASSET_DATABASE_URL")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("failed to get sql.DB: %w", err)
	}
	defer sqlDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	// Cancellation propagates process termination to the active database transaction.
	go func() {
		<-sigs
		log.Println("Received termination signal, cancelling import...")
		cancel()
	}()

	repo := repository.NewAssetRepository(db)
	uc := usecase.NewAssetUsecase(repo)

	summary, err := uc.ImportSample(ctx, orgID, userID, payload, dryRun)
	if err != nil {
		return err
	}

	summaryBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode summary: %w", err)
	}
	fmt.Println(string(summaryBytes))
	return nil
}
