// Command loadgen-metadata bulk-loads real Open Images V7 (train split)
// records into asset_db.metadata_items for index/load testing. Not part of
// the production import pipeline (see internal/openimages) — see
// infra/db/asset/loadtest/README.md for usage.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen-metadata failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var dir string
	var target int
	var batchSize int

	flag.StringVar(&dir, "cache-dir", "", "directory to download/cache the Open Images CSVs")
	flag.IntVar(&target, "target", 1_000_000, "number of distinct metadata_items rows to load")
	flag.IntVar(&batchSize, "batch-size", 50_000, "rows per COPY batch")
	flag.Parse()

	if dir == "" {
		return fmt.Errorf("--cache-dir is required")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	client := newHTTPClient()

	artifacts := []trainArtifact{
		{URL: "https://storage.googleapis.com/openimages/v7/oidv7-class-descriptions.csv", Filename: "oidv7-class-descriptions.csv"},
		{URL: "https://storage.googleapis.com/openimages/v7/oidv7-train-annotations-human-imagelabels.csv", Filename: "oidv7-train-annotations-human-imagelabels.csv"},
		{URL: "https://storage.googleapis.com/openimages/2018_04/train/train-images-boxable-with-rotation.csv", Filename: "train-images-boxable-with-rotation.csv"},
	}

	paths := make(map[string]string, len(artifacts))
	for _, a := range artifacts {
		log.Printf("fetching %s...", a.Filename)
		p, err := fetchPinned(ctx, client, a, dir)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", a.Filename, err)
		}
		paths[a.Filename] = p
	}

	log.Printf("loading class descriptions...")
	classMap, err := loadClassMap(paths["oidv7-class-descriptions.csv"])
	if err != nil {
		return fmt.Errorf("load class descriptions: %w", err)
	}
	log.Printf("%d classes loaded", len(classMap))

	log.Printf("streaming up to %d distinct labeled images from train annotations...", target)
	records, err := streamTargetImages(paths["oidv7-train-annotations-human-imagelabels.csv"], classMap, target)
	if err != nil {
		return fmt.Errorf("stream target images: %w", err)
	}
	log.Printf("collected %d distinct images", len(records))

	targetMap := make(map[string]*imageRecord, len(records))
	for _, r := range records {
		targetMap[r.ImageID] = r
	}

	log.Printf("joining image URL/title/license/author metadata...")
	if err := joinImageMetadata(paths["train-images-boxable-with-rotation.csv"], targetMap); err != nil {
		return fmt.Errorf("join image metadata: %w", err)
	}

	records, skipped := filterFittingRecords(records)
	if skipped > 0 {
		log.Printf("skipped %d records that overflow a varchar column (title/category/license/author)", skipped)
	}

	dsn := assetDSNFromEnv()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect to asset_db: %w", err)
	}
	defer conn.Close(ctx)

	folderIDs, err := leafFolderIDs(ctx, conn)
	if err != nil {
		return err
	}
	log.Printf("spreading rows across %d leaf folders", len(folderIDs))

	log.Printf("copying %d rows into metadata_items...", len(records))
	n, err := copyMetadataItems(ctx, conn, records, folderIDs, batchSize)
	if err != nil {
		return fmt.Errorf("copy metadata_items: %w", err)
	}
	log.Printf("done: loaded %d rows", n)

	return nil
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

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
