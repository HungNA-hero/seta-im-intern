package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
)

// ImportSampleTransaction executes the all-or-nothing database transaction for sample imports.
// It handles shadow refs, topological folder upsert, and metadata identity upsert.
func (r *assetRepository) ImportSampleTransaction(ctx context.Context, orgID, userID string, dataset domain.ImportDataset, dryRun bool) (domain.ImportSummary, error) {
	summary := domain.ImportSummary{Status: "success", DryRun: dryRun}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Ensure shadow refs
		if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?) ON CONFLICT (user_id) DO NOTHING", userID).Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?) ON CONFLICT (org_id) DO NOTHING", orgID).Error; err != nil {
			return err
		}

		// 2. Upsert Folders
		folderKeyToPath := make(map[string]string)
		folderKeyToID := make(map[string]string)

		for _, f := range dataset.Folders {
			var parentPath string
			if f.ParentKey != nil {
				parentPath = folderKeyToPath[*f.ParentKey]
			}

			var existing domain.Folder
			query := tx.Where("org_id = ? AND name = ? AND deleted_at IS NULL", orgID, f.Name)
			if parentPath == "" {
				query = query.Where("nlevel(path) = 1")
			} else {
				// Exact parent matching: path ~ parentPath.*{1}
				query = query.Where("path ~ ?", parentPath+".*{1}")
			}

			err := query.First(&existing).Error
			if err == nil {
				// Reuse
				folderKeyToPath[f.Key] = existing.Path
				folderKeyToID[f.Key] = existing.ID
				summary.FoldersReused++
				// (No description overwrite to keep existing business truth)
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				// Create
				folderID := uuid.New().String()
				segment := strings.ReplaceAll(folderID, "-", "")

				path := segment
				if parentPath != "" {
					path = parentPath + "." + segment
				}

				newFolder := domain.Folder{
					ID:          folderID,
					OrgID:       orgID,
					Path:        path,
					Name:        f.Name,
					Description: f.Description,
					CreatedBy:   userID,
					UpdatedBy:   &userID,
				}

				if err := tx.Create(&newFolder).Error; err != nil {
					return fmt.Errorf("failed to create folder %s: %w", f.Key, err)
				}
				folderKeyToPath[f.Key] = path
				folderKeyToID[f.Key] = folderID
				summary.FoldersCreated++
			} else {
				return err
			}
		}

		// 3. Upsert Metadata
		for _, m := range dataset.Metadata {
			var existing struct {
				domain.MetadataItem
				OrgID string `gorm:"column:org_id"`
			}
			// Must check global identity across orgs (Unscoped because soft-deleted items count)
			err := tx.Table("metadata_items").
				Select("metadata_items.*, folders.org_id").
				Joins("JOIN folders ON folders.id = metadata_items.folder_id").
				Unscoped().
				Where("metadata_items.external_source = ? AND metadata_items.external_id = ?", dataset.ExternalSource, m.ExternalID).
				First(&existing).Error

			folderID := folderKeyToID[m.FolderKey]

			if err == nil {
				// Same-org Global Identity Guard
				if existing.OrgID != orgID {
					return &domain.ImportError{
						Type:    "ConflictError",
						Key:     m.ExternalID,
						Message: "identity belongs to another org",
					}
				}

				reactivating := existing.DeletedAt.Valid
				unchanged := true
				if existing.FolderID != folderID || existing.Title != m.Title {
					unchanged = false
				} else if ptrStringEqual(existing.Description, m.Description) == false {
					unchanged = false
				} else if ptrStringEqual(existing.Category, m.Category) == false {
					unchanged = false
				} else if ptrStringEqual(existing.SourceURL, m.SourceURL) == false {
					unchanged = false
				} else if ptrStringEqual(existing.ThumbnailURL, m.ThumbnailURL) == false {
					unchanged = false
				} else if ptrStringEqual(existing.License, m.License) == false {
					unchanged = false
				} else if ptrStringEqual(existing.Author, m.Author) == false {
					unchanged = false
				} else if ptrStringEqual(existing.Notes, m.Notes) == false {
					unchanged = false
				} else if !sliceEqual(existing.Labels, m.Labels) {
					unchanged = false
				} else if !jsonObjectsEqual(existing.MetadataJSON, m.MetadataJSON) {
					unchanged = false
				}

				if !reactivating && unchanged {
					summary.MetadataUnchanged++
					continue // skip updates
				}

				updates := map[string]interface{}{
					"folder_id":     folderID,
					"title":         m.Title,
					"description":   m.Description,
					"labels":        pq.StringArray(m.Labels),
					"category":      m.Category,
					"source_url":    m.SourceURL,
					"thumbnail_url": m.ThumbnailURL,
					"license":       m.License,
					"author":        m.Author,
					"metadata_json": m.MetadataJSON,
					"notes":         m.Notes,
					"updated_by":    userID,
					"deleted_at":    nil, // reactivate soft-deleted
				}

				summary.MetadataUpdated++

				if err := tx.Unscoped().Model(&existing.MetadataItem).Updates(updates).Error; err != nil {
					return fmt.Errorf("failed to update metadata %s: %w", m.ExternalID, err)
				}
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				// Create new
				newM := domain.MetadataItem{
					ID:             uuid.New().String(),
					FolderID:       folderID,
					ExternalSource: &dataset.ExternalSource,
					ExternalID:     &m.ExternalID,
					Title:          m.Title,
					Description:    m.Description,
					Labels:         pq.StringArray(m.Labels),
					Category:       m.Category,
					SourceURL:      m.SourceURL,
					ThumbnailURL:   m.ThumbnailURL,
					License:        m.License,
					Author:         m.Author,
					MetadataJSON:   m.MetadataJSON,
					Notes:          m.Notes,
					CreatedBy:      userID,
					UpdatedBy:      &userID,
				}
				if err := tx.Create(&newM).Error; err != nil {
					return fmt.Errorf("failed to create metadata %s: %w", m.ExternalID, err)
				}
				summary.MetadataCreated++
			} else {
				return err
			}
		}

		if dryRun {
			return errors.New("DRY_RUN_ROLLBACK")
		}
		return nil
	})

	if err != nil {
		if err.Error() == "DRY_RUN_ROLLBACK" {
			return summary, nil
		}
		summary.Status = "error"
		return summary, err
	}

	return summary, nil
}

// jsonObjectsEqual compares JSON objects without converting numbers to float64.
func jsonObjectsEqual(a, b []byte) bool {
	decode := func(data []byte) (map[string]json.RawMessage, error) {
		var value map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		var trailing json.RawMessage
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, errors.New("metadata_json must contain exactly one JSON object")
		}
		return value, nil
	}

	left, leftErr := decode(a)
	right, rightErr := decode(b)
	if leftErr != nil || rightErr != nil {
		return bytes.Equal(a, b)
	}
	leftCanonical, leftErr := json.Marshal(left)
	rightCanonical, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

// ptrStringEqual compares nullable import fields without collapsing nil into an empty string.
func ptrStringEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// sliceEqual compares normalized labels in their deterministic stored order.
func sliceEqual(a pq.StringArray, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
