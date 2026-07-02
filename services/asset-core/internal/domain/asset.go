package domain

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

var (
	ErrFolderNotFound   = errors.New("folder not found")
	ErrFolderConflict   = errors.New("folder conflict: sibling name or path already exists")
	ErrMetadataNotFound = errors.New("metadata not found")
	ErrMetadataConflict = errors.New("metadata conflict: external identity already exists")
	ErrInvalidInput     = errors.New("invalid input")
)

// CreateFolderInput holds the data required to create a folder.
type CreateFolderInput struct {
	ParentPath  *string `json:"parent_path"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

// UpdateFolderInput holds the data required to update a folder.
type UpdateFolderInput struct {
	Name           *string
	NameSet        bool
	Description    *string
	DescriptionSet bool
}

// OrganizationRef acts as a shadow reference to Access DB organizations.
type OrganizationRef struct {
	OrgID string `gorm:"type:uuid;primaryKey;column:org_id" json:"org_id"`
}

func (OrganizationRef) TableName() string {
	return "organization_ref"
}

// UserRef acts as a shadow reference to Access DB users.
type UserRef struct {
	UserID string `gorm:"type:uuid;primaryKey;column:user_id" json:"user_id"`
}

func (UserRef) TableName() string {
	return "user_ref"
}

// Folder represents a node in the asset tree, identified by ltree path.
type Folder struct {
	ID          string         `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID       string         `gorm:"type:uuid;not null;index:uq_folders_active_path,unique" json:"org_id"`
	Path        string         `gorm:"type:ltree;not null;index:uq_folders_active_path,unique;index:idx_folders_path_gist,type:gist" json:"path"`
	Name        string         `gorm:"type:varchar(255);not null" json:"name"`
	Description *string        `gorm:"type:text" json:"description"`
	CreatedBy   string         `gorm:"type:uuid;not null" json:"created_by"`
	UpdatedBy   *string        `gorm:"type:uuid" json:"updated_by"`
	CreatedAt   time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-"`
}

func (Folder) TableName() string {
	return "folders"
}

// MetadataItem holds textual metadata for assets.
type MetadataItem struct {
	ID             string          `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	FolderID       string          `gorm:"type:uuid;not null;index" json:"folder_id"`
	Title          string          `gorm:"type:varchar(255);not null" json:"title"`
	Description    *string         `gorm:"type:text" json:"description"`
	Labels         pq.StringArray  `gorm:"type:text[];not null;default:'{}'" json:"labels"`
	Category       *string         `gorm:"type:varchar(100)" json:"category"`
	ExternalSource *string         `gorm:"type:varchar(100);uniqueIndex:uq_metadata_items_external_identity" json:"external_source"`
	ExternalID     *string         `gorm:"type:varchar(255);uniqueIndex:uq_metadata_items_external_identity" json:"external_id"`
	SourceURL      *string         `gorm:"type:text" json:"source_url"`
	ThumbnailURL   *string         `gorm:"type:text" json:"thumbnail_url"`
	License        *string         `gorm:"type:varchar(255)" json:"license"`
	Author         *string         `gorm:"type:varchar(255)" json:"author"`
	MetadataJSON   json.RawMessage `gorm:"type:jsonb;not null;default:'{}'" json:"metadata_json"`
	Notes          *string         `gorm:"type:text" json:"notes"`
	CreatedBy      string          `gorm:"type:uuid;not null" json:"created_by"`
	UpdatedBy      *string         `gorm:"type:uuid" json:"updated_by"`
	CreatedAt      time.Time       `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt      time.Time       `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt      gorm.DeletedAt  `gorm:"index:idx_metadata_items_active_folder_id" json:"-"`
}

// TableName maps MetadataItem to the Asset DB metadata_items table.
func (MetadataItem) TableName() string {
	return "metadata_items"
}

// CreateMetadataInput holds the data required to create a metadata item.
type CreateMetadataInput struct {
	FolderID       string          `json:"folder_id"`
	Title          string          `json:"title"`
	Description    *string         `json:"description"`
	Labels         pq.StringArray  `json:"labels"`
	Category       *string         `json:"category"`
	ExternalSource *string         `json:"external_source"`
	ExternalID     *string         `json:"external_id"`
	SourceURL      *string         `json:"source_url"`
	ThumbnailURL   *string         `json:"thumbnail_url"`
	License        *string         `json:"license"`
	Author         *string         `json:"author"`
	MetadataJSON   json.RawMessage `json:"metadata_json"`
	Notes          *string         `json:"notes"`
}

// UpdateMetadataInput holds the data required to update a metadata item.
// It uses pointer fields and presence flags for differentiating between omitted and explicit null values.
type UpdateMetadataInput struct {
	Title             *string
	TitleSet          bool
	Description       *string
	DescriptionSet    bool
	Labels            *pq.StringArray
	LabelsSet         bool
	Category          *string
	CategorySet       bool
	ExternalSource    *string
	ExternalSourceSet bool
	ExternalID        *string
	ExternalIDSet     bool
	SourceURL         *string
	SourceURLSet      bool
	ThumbnailURL      *string
	ThumbnailURLSet   bool
	License           *string
	LicenseSet        bool
	Author            *string
	AuthorSet         bool
	MetadataJSON      *json.RawMessage
	MetadataJSONSet   bool
	Notes             *string
	NotesSet          bool
}

// AssetRepository defines the contract for database operations related to assets.
type AssetRepository interface {
	GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]Folder, error)
	GetFolderByID(ctx context.Context, orgID string, folderID string) (Folder, error)
	GetFolderChildren(ctx context.Context, orgID string, parentPath string) ([]Folder, error)
	GetRootFolders(ctx context.Context, orgID string) ([]Folder, error)
	CreateFolder(ctx context.Context, orgID, userID string, input CreateFolderInput) (Folder, error)
	UpdateFolder(ctx context.Context, orgID, userID, folderID string, input UpdateFolderInput) (Folder, error)
	EnsureRefs(ctx context.Context, userID, orgID string) error

	// GetMetadataItemsByFolder returns active metadata only when the containing folder is active and org-scoped.
	GetMetadataItemsByFolder(ctx context.Context, orgID, folderID string) ([]MetadataItem, error)
	// GetMetadataItemByID returns one active metadata item through its org-scoped containing folder.
	GetMetadataItemByID(ctx context.Context, orgID, id string) (MetadataItem, error)
	// CreateMetadataItem persists normalized metadata and audit shadow references atomically.
	CreateMetadataItem(ctx context.Context, orgID, userID string, input CreateMetadataInput) (MetadataItem, error)
	// UpdateMetadataItem applies sparse fields to a locked metadata row and preserves cross-field invariants.
	UpdateMetadataItem(ctx context.Context, orgID, userID, id string, input UpdateMetadataInput) (MetadataItem, error)
}

// AssetUsecase defines the contract for business logic operations related to assets.
type AssetUsecase interface {
	GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]Folder, error)
	GetFolderByID(ctx context.Context, orgID string, folderID string) (Folder, error)
	GetFolderChildren(ctx context.Context, orgID string, parentPath string) ([]Folder, error)
	GetRootFolders(ctx context.Context, orgID string) ([]Folder, error)
	CreateFolder(ctx context.Context, orgID, userID string, input CreateFolderInput) (Folder, error)
	UpdateFolder(ctx context.Context, orgID, userID, folderID string, input UpdateFolderInput) (Folder, error)
	EnsureRefs(ctx context.Context, userID, orgID string) error

	// GetMetadataItemsByFolder lists active metadata in an active org-scoped folder.
	GetMetadataItemsByFolder(ctx context.Context, orgID, folderID string) ([]MetadataItem, error)
	// GetMetadataItemByID loads one active org-scoped metadata item.
	GetMetadataItemByID(ctx context.Context, orgID, id string) (MetadataItem, error)
	// CreateMetadataItem validates and creates text-only metadata.
	CreateMetadataItem(ctx context.Context, orgID, userID string, input CreateMetadataInput) (MetadataItem, error)
	// UpdateMetadataItem validates and applies a sparse metadata update.
	UpdateMetadataItem(ctx context.Context, orgID, userID, id string, input UpdateMetadataInput) (MetadataItem, error)
}
