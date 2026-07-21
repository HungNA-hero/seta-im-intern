package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/jackc/pgx/v5"
)

const loadtestOrgID = "00000000-0000-0000-0000-0000000000f0"
const loadtestUserID = "00000000-0000-0000-0000-0000000000f1"

// leafFolderIDs returns the pre-seeded leaf folders (infra/db/asset/loadtest/seed_folders.sql)
// that metadata rows get spread across.
func leafFolderIDs(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx,
		`SELECT id FROM folders WHERE org_id = $1 AND deleted_at IS NULL AND nlevel(path) = 3`,
		loadtestOrgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no leaf folders found for org %s; run infra/db/asset/loadtest/seed_folders.sql first", loadtestOrgID)
	}
	return ids, nil
}

func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// varchar column limits from infra/db/asset/migrations/V1__asset_initial_schema.sql.
const (
	maxTitleLen    = 255
	maxCategoryLen = 100
	maxLicenseLen  = 255
	maxAuthorLen   = 255
)

// filterFittingRecords drops any record that would overflow a varchar column
// (source data isn't bounded to the schema's column widths) instead of
// silently truncating it. Returns the kept records and how many were skipped.
func filterFittingRecords(records []*imageRecord) (kept []*imageRecord, skipped int) {
	kept = make([]*imageRecord, 0, len(records))
	for _, rec := range records {
		title := rec.Title
		if title == "" {
			title = fmt.Sprintf("Open Images %s", rec.ImageID)
		}
		if len(title) > maxTitleLen || len(rec.Category) > maxCategoryLen ||
			len(rec.License) > maxLicenseLen || len(rec.Author) > maxAuthorLen {
			skipped++
			continue
		}
		kept = append(kept, rec)
	}
	return kept, skipped
}

type provenance struct {
	Source   string `json:"source"`
	Split    string `json:"split"`
	Rotation string `json:"rotation,omitempty"`
}

// copyMetadataItems streams records into metadata_items via COPY, chunked so
// progress can be reported and memory stays bounded. Returns total rows
// written.
func copyMetadataItems(ctx context.Context, conn *pgx.Conn, records []*imageRecord, folderIDs []string, batchSize int) (int64, error) {
	columns := []string{
		"folder_id", "title", "description", "labels", "category",
		"external_source", "external_id", "source_url", "thumbnail_url",
		"license", "author", "metadata_json", "created_by", "updated_by",
	}

	rng := rand.New(rand.NewSource(42))
	var total int64

	for start := 0; start < len(records); start += batchSize {
		end := start + batchSize
		if end > len(records) {
			end = len(records)
		}
		batch := records[start:end]

		n, err := conn.CopyFrom(ctx, pgx.Identifier{"metadata_items"}, columns,
			pgx.CopyFromSlice(len(batch), func(i int) ([]any, error) {
				rec := batch[i]
				folderID := folderIDs[rng.Intn(len(folderIDs))]

				prov := provenance{Source: "open_images_v7", Split: "train", Rotation: rec.Rotation}
				metadataJSON, err := json.Marshal(prov)
				if err != nil {
					return nil, err
				}

				title := rec.Title
				if title == "" {
					title = fmt.Sprintf("Open Images %s", rec.ImageID)
				}

				return []any{
					folderID,
					title,
					nil, // description
					rec.Labels,
					rec.Category,
					"open_images_v7_train",
					rec.ImageID,
					nullableString(rec.OriginalLandingURL),
					nullableString(rec.Thumbnail300KURL),
					nullableString(rec.License),
					nullableString(rec.Author),
					string(metadataJSON),
					loadtestUserID,
					loadtestUserID,
				}, nil
			}),
		)
		if err != nil {
			return total, fmt.Errorf("copy batch [%d:%d): %w", start, end, err)
		}
		total += n
		fmt.Printf("loaded %d/%d metadata_items rows\n", total, len(records))
	}

	return total, nil
}
