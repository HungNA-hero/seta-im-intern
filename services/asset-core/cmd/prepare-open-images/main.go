package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"seta-im-intern/go-asset-core/internal/openimages"
)

// main orchestrates Open Images data preparation.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Preparation failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var split string
	var maxItems int
	var outputDir string

	flag.StringVar(&split, "split", "validation", "Dataset split (validation)")
	flag.IntVar(&maxItems, "max-items", 25, "Maximum output items")
	flag.StringVar(&outputDir, "output-dir", "", "Output directory")
	flag.Parse()

	if outputDir == "" {
		return fmt.Errorf("--output-dir is required")
	}

	if split != "validation" {
		return fmt.Errorf("only validation split is supported")
	}

	downloader := openimages.NewDownloader(outputDir)

	artifacts := []openimages.Artifact{
		{
			ID:             "OI-01",
			URL:            "https://storage.googleapis.com/openimages/v7/oidv7-val-annotations-human-imagelabels.csv",
			Filename:       "oidv7-val-annotations-human-imagelabels.csv",
			ExpectedSHA256: "92ddbdfceb3626e044df5e89100b24f6c22a79c1888a4bddd00a6f231d86d56a",
			MaxBytes:       100 * 1024 * 1024,
		},
		{
			ID:             "OI-02",
			URL:            "https://storage.googleapis.com/openimages/v7/oidv7-class-descriptions.csv",
			Filename:       "oidv7-class-descriptions.csv",
			ExpectedSHA256: "84a4373a0efb7fd6d93fe19b0e7ceb6c1b855c233d13b9b78a9a33655c9fdce3",
			MaxBytes:       10 * 1024 * 1024,
		},
		{
			ID:             "OI-03",
			URL:            "https://storage.googleapis.com/openimages/2018_04/validation/validation-images-with-rotation.csv",
			Filename:       "validation-images-with-rotation.csv",
			ExpectedSHA256: "ed93a0e121fe345effdfc7359b848dbc64a1ff6778c8c73563157cb500b33a17",
			MaxBytes:       200 * 1024 * 1024,
		},
	}

	ctx := context.Background()
	var results []openimages.DownloadResult

	for _, a := range artifacts {
		log.Printf("Fetching %s...", a.Filename)
		res, err := downloader.Fetch(ctx, a)
		if err != nil {
			return fmt.Errorf("failed to fetch %s: %w", a.Filename, err)
		}
		log.Printf("Verified %s (SHA256: %s)", res.Filename, res.SHA256)
		results = append(results, res)
	}

	log.Printf("Transforming metadata...")
	transformer := &openimages.Transformer{
		Dir:      outputDir,
		MaxItems: maxItems,
	}

	manifest, err := transformer.Transform(results)
	if err != nil {
		return fmt.Errorf("failed to transform metadata: %w", err)
	}

	log.Printf("Manifest written. Output checksum: %s", manifest.OutputChecksum)
	return nil
}
