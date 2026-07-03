package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"seta-im-intern/go-asset-core/internal/domain"
)

const (
	MaxImportMetadataCount = 10000
	MaxImportFolderCount   = 1000
)

var allowedExternalSources = map[string]bool{
	"open_images_v7":     true,
	"kaggle_open_images": true,
}

// ImportSample reads, validates, and imports a dataset of folders and metadata items.
func (u *assetUsecase) ImportSample(ctx context.Context, orgID, userID string, payload []byte, dryRun bool) (domain.ImportSummary, error) {
	if orgID == "" {
		return domain.ImportSummary{}, errors.New("orgID is required")
	}
	if userID == "" {
		return domain.ImportSummary{}, errors.New("userID is required")
	}

	// 1. Validate payload
	var dataset domain.ImportDataset
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields() // Strict mode: unknown fields are rejected
	if err := dec.Decode(&dataset); err != nil {
		return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidSchema", Message: err.Error()}
	}
	if dec.More() {
		return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidSchema", Message: "trailing garbage found after JSON document"}
	}

	// 2. Validate version, source, and limits
	if dataset.Version != 1 {
		return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidVersion", Message: "only version 1 is supported"}
	}
	dataset.ExternalSource = strings.TrimSpace(dataset.ExternalSource)
	if !allowedExternalSources[dataset.ExternalSource] {
		return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidSource", Message: "unsupported or blank external_source"}
	}
	if len(dataset.Folders) > MaxImportFolderCount {
		return domain.ImportSummary{}, &domain.ImportError{Type: "LimitExceeded", Message: fmt.Sprintf("max %d folders allowed", MaxImportFolderCount)}
	}
	if len(dataset.Metadata) > MaxImportMetadataCount {
		return domain.ImportSummary{}, &domain.ImportError{Type: "LimitExceeded", Message: fmt.Sprintf("max %d metadata items allowed", MaxImportMetadataCount)}
	}

	// 3. Validate Folders (Unique Keys, Acyclic Graph, Canonical Names)
	folderKeyMap := make(map[string]bool)
	for i, f := range dataset.Folders {
		if f.Key == "" {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidFolder", Index: i, Message: "folder key cannot be blank"}
		}
		if folderKeyMap[f.Key] {
			return domain.ImportSummary{}, &domain.ImportError{Type: "DuplicateFolderKey", Key: f.Key, Message: "folder keys must be unique"}
		}

		// KAN-29 Rules
		dataset.Folders[i].Name = strings.TrimSpace(f.Name)
		if dataset.Folders[i].Name == "" || len([]rune(dataset.Folders[i].Name)) > 255 {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidFolder", Key: f.Key, Message: "name must be non-blank and <= 255 runes"}
		}
		if f.Description != nil {
			trimmed := strings.TrimSpace(*f.Description)
			if trimmed == "" {
				dataset.Folders[i].Description = nil
			} else {
				dataset.Folders[i].Description = &trimmed
			}
		}

		folderKeyMap[f.Key] = true
	}

	// Check parents and topological sort
	sortedFolders, err := topologicalSortFolders(dataset.Folders, folderKeyMap)
	if err != nil {
		return domain.ImportSummary{}, err
	}
	dataset.Folders = sortedFolders // Replace with deterministic top-down ordered list

	// 4. Validate Metadata
	metaExternalIDMap := make(map[string]bool)
	for i, m := range dataset.Metadata {
		if !folderKeyMap[m.FolderKey] {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: fmt.Sprintf("unknown folder_key '%s'", m.FolderKey)}
		}
		extID := strings.TrimSpace(m.ExternalID)
		if extID == "" || len([]rune(extID)) > 255 {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: "external_id must be non-blank and max 255 chars"}
		}
		if metaExternalIDMap[extID] {
			return domain.ImportSummary{}, &domain.ImportError{Type: "DuplicateExternalID", Index: i, Message: fmt.Sprintf("external_id '%s' is duplicated within file", extID)}
		}
		dataset.Metadata[i].ExternalID = extID
		metaExternalIDMap[extID] = true

		// KAN-30/33 Rules
		dataset.Metadata[i].Title = strings.TrimSpace(m.Title)
		if dataset.Metadata[i].Title == "" || len([]rune(dataset.Metadata[i].Title)) > 255 {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: "title must be non-blank and <= 255 runes"}
		}

		normalized, err := normalizeLabels(m.Labels)
		if err != nil {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: err.Error()}
		}
		dataset.Metadata[i].Labels = normalized
		trimmedJSON := bytes.TrimSpace(m.MetadataJSON)
		if len(trimmedJSON) == 0 || bytes.Equal(trimmedJSON, []byte("null")) {
			dataset.Metadata[i].MetadataJSON = []byte("{}")
		} else if err := validateJSONObject(trimmedJSON); err != nil {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: err.Error()}
		} else {
			dataset.Metadata[i].MetadataJSON = append(json.RawMessage(nil), trimmedJSON...)
		}

		// Basic limits for URL/Text fields
		if err := validateMetadataItemTextLimits(m.Description, m.Category, m.SourceURL, m.ThumbnailURL, m.License, m.Author, m.Notes); err != nil {
			return domain.ImportSummary{}, &domain.ImportError{Type: "InvalidMetadata", Index: i, Message: "field length limit exceeded"}
		}
	}

	// 5. Transactional processing
	return u.repo.ImportSampleTransaction(ctx, orgID, userID, dataset, dryRun)
}

// topologicalSortFolders sorts the folders from root to leaves and detects cycles.
func topologicalSortFolders(folders []domain.ImportFolder, validKeys map[string]bool) ([]domain.ImportFolder, error) {
	// Build graph
	childrenMap := make(map[string][]string)
	inDegree := make(map[string]int)
	folderMap := make(map[string]domain.ImportFolder)

	for _, f := range folders {
		folderMap[f.Key] = f
		inDegree[f.Key] = 0 // initialize
	}

	for _, f := range folders {
		if f.ParentKey != nil {
			parent := *f.ParentKey
			if !validKeys[parent] {
				return nil, &domain.ImportError{Type: "InvalidFolder", Key: f.Key, Message: fmt.Sprintf("unknown parent_key '%s'", parent)}
			}
			childrenMap[parent] = append(childrenMap[parent], f.Key)
			inDegree[f.Key]++
		}
	}

	// Kahn's algorithm preserves the relative input order of roots and siblings.
	var queue []string
	for _, f := range folders {
		if inDegree[f.Key] == 0 {
			queue = append(queue, f.Key)
		}
	}

	var sorted []domain.ImportFolder
	for len(queue) > 0 {
		// Pop front
		curr := queue[0]
		queue = queue[1:]

		sorted = append(sorted, folderMap[curr])

		for _, child := range childrenMap[curr] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}

	if len(sorted) != len(folders) {
		return nil, &domain.ImportError{Type: "CyclicGraph", Message: "folder graph contains cycles"}
	}

	return sorted, nil
}
