package usecase

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lib/pq"

	"seta-im-intern/go-asset-core/internal/domain"
)

var ltreePathPattern = regexp.MustCompile(`^[A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*$`)

type assetUsecase struct {
	repo domain.AssetRepository
}

// NewAssetUsecase creates a new instance of AssetUsecase.
func NewAssetUsecase(repo domain.AssetRepository) domain.AssetUsecase {
	return &assetUsecase{repo: repo}
}

func (u *assetUsecase) GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]domain.Folder, error) {
	// Here we could add business logic, permission checks, etc.
	// For now, it simply delegates to the repository.
	return u.repo.GetFolderTree(ctx, orgID, rootPath)
}

func (u *assetUsecase) GetFolderByID(ctx context.Context, orgID string, folderID string) (domain.Folder, error) {
	return u.repo.GetFolderByID(ctx, orgID, folderID)
}

func (u *assetUsecase) GetFolderChildren(ctx context.Context, orgID string, parentPath string) ([]domain.Folder, error) {
	return u.repo.GetFolderChildren(ctx, orgID, parentPath)
}

func (u *assetUsecase) GetRootFolders(ctx context.Context, orgID string) ([]domain.Folder, error) {
	return u.repo.GetRootFolders(ctx, orgID)
}

func (u *assetUsecase) EnsureRefs(ctx context.Context, userID, orgID string) error {
	return u.repo.EnsureRefs(ctx, userID, orgID)
}

func (u *assetUsecase) CreateFolder(ctx context.Context, orgID, userID string, input domain.CreateFolderInput) (domain.Folder, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	if utf8.RuneCountInString(input.Name) > 255 {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	if input.ParentPath != nil {
		trimmed := strings.TrimSpace(*input.ParentPath)
		if trimmed != "" && !ltreePathPattern.MatchString(trimmed) {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		input.ParentPath = &trimmed
	}
	return u.repo.CreateFolder(ctx, orgID, userID, input)
}

func (u *assetUsecase) UpdateFolder(ctx context.Context, orgID, userID, folderID string, input domain.UpdateFolderInput) (domain.Folder, error) {
	if input.NameSet {
		if input.Name == nil {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		trimmed := strings.TrimSpace(*input.Name)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > 255 {
			return domain.Folder{}, domain.ErrInvalidInput
		}
		input.Name = &trimmed
	}
	if !input.NameSet && !input.DescriptionSet {
		return domain.Folder{}, domain.ErrInvalidInput
	}
	return u.repo.UpdateFolder(ctx, orgID, userID, folderID, input)
}

// MoveFolder delegates moving a folder to a new parent to the repository.
func (u *assetUsecase) MoveFolder(ctx context.Context, orgID, userID, folderID string, input domain.MoveFolderInput) (domain.Folder, error) {
	return u.repo.MoveFolder(ctx, orgID, userID, folderID, input)
}

// DeleteFolder delegates soft-deleting a folder to the repository.
func (u *assetUsecase) DeleteFolder(ctx context.Context, orgID, userID, folderID string) error {
	return u.repo.DeleteFolder(ctx, orgID, userID, folderID)
}

// GetMetadataItemsByFolder delegates an org-scoped active-folder metadata query to the repository.
func (u *assetUsecase) GetMetadataItemsByFolder(ctx context.Context, orgID, folderID string) ([]domain.MetadataItem, error) {
	return u.repo.GetMetadataItemsByFolder(ctx, orgID, folderID)
}

// GetMetadataItemByID delegates an org-scoped active metadata lookup to the repository.
func (u *assetUsecase) GetMetadataItemByID(ctx context.Context, orgID, id string) (domain.MetadataItem, error) {
	return u.repo.GetMetadataItemByID(ctx, orgID, id)
}

// normalizeLabels trims labels, rejects blank entries, and preserves first-seen order while deduplicating.
func normalizeLabels(labels []string) (pq.StringArray, error) {
	result := make(pq.StringArray, 0, len(labels))
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		normalized := strings.TrimSpace(label)
		if normalized == "" {
			return nil, domain.ErrInvalidInput
		}
		if _, exists := seen[normalized]; !exists {
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	return result, nil
}

// validateJSONObject accepts only a non-null JSON object so Asset DB never stores arrays, scalars, or JSON null.
func validateJSONObject(data []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return domain.ErrInvalidInput
	}
	return nil
}

// normalizeExternalIdentity trims a present external identity component and rejects blank values.
func normalizeExternalIdentity(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil, domain.ErrInvalidInput
	}
	return &normalized, nil
}

// exceedsRuneLimit reports whether a present string exceeds the supplied rune limit.
func exceedsRuneLimit(value *string, limit int) bool {
	return value != nil && utf8.RuneCountInString(*value) > limit
}

// validateMetadataItemTextLimits enforces the canonical text limits shared by metadata writes and imports.
func validateMetadataItemTextLimits(description, category, sourceURL, thumbnailURL, license, author, notes *string) error {
	if exceedsRuneLimit(description, 5000) || exceedsRuneLimit(category, 100) ||
		exceedsRuneLimit(sourceURL, 2048) || exceedsRuneLimit(thumbnailURL, 2048) ||
		exceedsRuneLimit(license, 255) || exceedsRuneLimit(author, 255) ||
		exceedsRuneLimit(notes, 5000) {
		return domain.ErrInvalidInput
	}
	return nil
}

// valueWhenSet excludes omitted sparse-update fields from canonical validation.
func valueWhenSet(isSet bool, value *string) *string {
	if !isSet {
		return nil
	}
	return value
}

// CreateMetadataItem normalizes and validates create input before crossing the repository transaction boundary.
func (u *assetUsecase) CreateMetadataItem(ctx context.Context, orgID, userID string, input domain.CreateMetadataInput) (domain.MetadataItem, error) {
	input.FolderID = strings.TrimSpace(input.FolderID)
	if input.FolderID == "" {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 255 {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	if exceedsRuneLimit(input.ExternalSource, 100) {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	if exceedsRuneLimit(input.ExternalID, 255) {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	if err := validateMetadataItemTextLimits(
		input.Description,
		input.Category,
		input.SourceURL,
		input.ThumbnailURL,
		input.License,
		input.Author,
		input.Notes,
	); err != nil {
		return domain.MetadataItem{}, err
	}

	var err error
	input.ExternalSource, err = normalizeExternalIdentity(input.ExternalSource)
	if err != nil {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	input.ExternalID, err = normalizeExternalIdentity(input.ExternalID)
	if err != nil || (input.ExternalSource == nil) != (input.ExternalID == nil) {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}

	input.Labels, err = normalizeLabels(input.Labels)
	if err != nil {
		return domain.MetadataItem{}, err
	}

	trimmedJSON := bytes.TrimSpace(input.MetadataJSON)
	if len(trimmedJSON) == 0 || bytes.Equal(trimmedJSON, []byte("null")) {
		input.MetadataJSON = []byte("{}")
	} else if err := validateJSONObject(trimmedJSON); err != nil {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	} else {
		input.MetadataJSON = append(json.RawMessage(nil), trimmedJSON...)
	}

	return u.repo.CreateMetadataItem(ctx, orgID, userID, input)
}

// UpdateMetadataItem validates sparse update semantics before the repository locks and updates the final row state.
func (u *assetUsecase) UpdateMetadataItem(ctx context.Context, orgID, userID, id string, input domain.UpdateMetadataInput) (domain.MetadataItem, error) {
	if !input.TitleSet && !input.DescriptionSet && !input.LabelsSet && !input.CategorySet &&
		!input.ExternalSourceSet && !input.ExternalIDSet && !input.SourceURLSet &&
		!input.ThumbnailURLSet && !input.LicenseSet && !input.AuthorSet &&
		!input.MetadataJSONSet && !input.NotesSet {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}

	if input.TitleSet {
		if input.Title == nil {
			return domain.MetadataItem{}, domain.ErrInvalidInput
		}
		trimmed := strings.TrimSpace(*input.Title)
		if trimmed == "" || utf8.RuneCountInString(trimmed) > 255 {
			return domain.MetadataItem{}, domain.ErrInvalidInput
		}
		input.Title = &trimmed
	}

	if input.ExternalSourceSet && exceedsRuneLimit(input.ExternalSource, 100) {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	if input.ExternalIDSet && exceedsRuneLimit(input.ExternalID, 255) {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}
	if err := validateMetadataItemTextLimits(
		valueWhenSet(input.DescriptionSet, input.Description),
		valueWhenSet(input.CategorySet, input.Category),
		valueWhenSet(input.SourceURLSet, input.SourceURL),
		valueWhenSet(input.ThumbnailURLSet, input.ThumbnailURL),
		valueWhenSet(input.LicenseSet, input.License),
		valueWhenSet(input.AuthorSet, input.Author),
		valueWhenSet(input.NotesSet, input.Notes),
	); err != nil {
		return domain.MetadataItem{}, domain.ErrInvalidInput
	}

	if input.LabelsSet {
		labels := pq.StringArray{}
		if input.Labels != nil {
			normalized, err := normalizeLabels(*input.Labels)
			if err != nil {
				return domain.MetadataItem{}, err
			}
			labels = normalized
		}
		// Both explicit null and an empty array clear labels to the database's required empty array value.
		input.Labels = &labels
	}

	if input.MetadataJSONSet {
		metadataJSON := json.RawMessage("{}")
		if input.MetadataJSON != nil {
			trimmedJSON := bytes.TrimSpace(*input.MetadataJSON)
			if bytes.Equal(trimmedJSON, []byte("null")) || validateJSONObject(trimmedJSON) != nil {
				return domain.MetadataItem{}, domain.ErrInvalidInput
			}
			metadataJSON = append(json.RawMessage(nil), trimmedJSON...)
		}
		// Explicit null resets metadata_json to the contract default instead of storing JSON null.
		input.MetadataJSON = &metadataJSON
	}

	if input.ExternalSourceSet {
		normalized, err := normalizeExternalIdentity(input.ExternalSource)
		if err != nil {
			return domain.MetadataItem{}, domain.ErrInvalidInput
		}
		input.ExternalSource = normalized
	}
	if input.ExternalIDSet {
		normalized, err := normalizeExternalIdentity(input.ExternalID)
		if err != nil {
			return domain.MetadataItem{}, domain.ErrInvalidInput
		}
		input.ExternalID = normalized
	}

	return u.repo.UpdateMetadataItem(ctx, orgID, userID, id, input)
}

// DeleteMetadataItem soft-deletes an org-scoped metadata item.
func (u *assetUsecase) DeleteMetadataItem(ctx context.Context, orgID, userID, id string) error {
	return u.repo.DeleteMetadataItem(ctx, orgID, userID, id)
}

// SearchMetadataItems searches for metadata items based on the provided filter within the organization.
func (u *assetUsecase) SearchMetadataItems(ctx context.Context, orgID string, filter domain.MetadataSearchFilter) ([]domain.MetadataItem, error) {
	if filter.Limit <= 0 || filter.Limit > 100 {
		return nil, domain.ErrInvalidInput
	}
	if filter.Offset < 0 {
		return nil, domain.ErrInvalidInput
	}

	hasSubstantiveFilter := false

	if filter.FolderID != nil {
		trimmed := strings.TrimSpace(*filter.FolderID)
		if trimmed != "" {
			hasSubstantiveFilter = true
			filter.FolderID = &trimmed
		} else {
			filter.FolderID = nil
		}
	}

	if filter.Query != nil {
		trimmed := strings.TrimSpace(*filter.Query)
		if utf8.RuneCountInString(trimmed) < 2 || utf8.RuneCountInString(trimmed) > 200 {
			return nil, domain.ErrInvalidInput
		}
		hasSubstantiveFilter = true
		filter.Query = &trimmed
	}

	if len(filter.Labels) > 0 {
		normalized, err := normalizeLabels(filter.Labels)
		if err != nil {
			return nil, err
		}
		if len(normalized) > 0 {
			hasSubstantiveFilter = true
			filter.Labels = normalized
		} else {
			filter.Labels = nil
		}
	}

	if filter.Category != nil {
		trimmed := strings.TrimSpace(*filter.Category)
		if trimmed != "" {
			hasSubstantiveFilter = true
			filter.Category = &trimmed
		} else {
			filter.Category = nil
		}
	}

	if filter.ExternalSource != nil {
		trimmed := strings.TrimSpace(*filter.ExternalSource)
		if trimmed != "" {
			hasSubstantiveFilter = true
			filter.ExternalSource = &trimmed
		} else {
			filter.ExternalSource = nil
		}
	}

	if !hasSubstantiveFilter {
		return nil, domain.ErrInvalidInput
	}

	return u.repo.SearchMetadataItems(ctx, orgID, filter)
}
