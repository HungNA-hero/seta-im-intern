package domain

import (
	"encoding/json"
	"fmt"
)

// ImportDataset represents the schema of a valid text metadata import file (version 1).
type ImportDataset struct {
	Version        int                  `json:"version"`
	ExternalSource string               `json:"external_source"`
	Folders        []ImportFolder       `json:"folders"`
	Metadata       []ImportMetadataItem `json:"metadata"`
}

// ImportFolder defines a folder to be created or reused during an import.
type ImportFolder struct {
	Key         string  `json:"key"`
	ParentKey   *string `json:"parent_key"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// ImportMetadataItem defines a metadata item to be created or updated during an import.
type ImportMetadataItem struct {
	FolderKey    string          `json:"folder_key"`
	ExternalID   string          `json:"external_id"`
	Title        string          `json:"title"`
	Description  *string         `json:"description"`
	Labels       []string        `json:"labels"`
	Category     *string         `json:"category"`
	SourceURL    *string         `json:"source_url"`
	ThumbnailURL *string         `json:"thumbnail_url"`
	License      *string         `json:"license"`
	Author       *string         `json:"author"`
	MetadataJSON json.RawMessage `json:"metadata_json"`
	Notes        *string         `json:"notes"`
}

// ImportSummary provides deterministic totals for the completed import operation.
type ImportSummary struct {
	Status            string `json:"status"`
	DryRun            bool   `json:"dry_run"`
	FoldersCreated    int    `json:"folders_created"`
	FoldersReused     int    `json:"folders_reused"`
	MetadataCreated   int    `json:"metadata_created"`
	MetadataUpdated   int    `json:"metadata_updated"`
	MetadataUnchanged int    `json:"metadata_unchanged"`
}

// ImportError represents a validation or operational error during import with context.
type ImportError struct {
	Type    string
	Message string
	Key     string
	Index   int
}

// Error implements the error interface to provide a formatted description of the import error.
func (e *ImportError) Error() string {
	if e.Key != "" {
		return fmt.Sprintf("%s at key '%s': %s", e.Type, e.Key, e.Message)
	}
	if e.Index >= 0 {
		return fmt.Sprintf("%s at index %d: %s", e.Type, e.Index, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}
