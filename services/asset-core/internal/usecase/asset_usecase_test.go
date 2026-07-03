package usecase_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

// fakeAssetRepo captures use-case delegation so validation tests can assert normalized inputs and blocked writes.
type fakeAssetRepo struct {
	createCalled        bool
	updateCalled        bool
	metadataCreateInput domain.CreateMetadataInput
	metadataUpdateInput domain.UpdateMetadataInput
}

func (f *fakeAssetRepo) GetFolderTree(_ context.Context, _ string, _ string) ([]domain.Folder, error) {
	return nil, nil
}
func (f *fakeAssetRepo) GetFolderByID(_ context.Context, _ string, _ string) (domain.Folder, error) {
	return domain.Folder{}, nil
}
func (f *fakeAssetRepo) GetFolderChildren(_ context.Context, _ string, _ string) ([]domain.Folder, error) {
	return nil, nil
}
func (f *fakeAssetRepo) GetRootFolders(_ context.Context, _ string) ([]domain.Folder, error) {
	return nil, nil
}
func (f *fakeAssetRepo) EnsureRefs(_ context.Context, _, _ string) error {
	return nil
}
func (f *fakeAssetRepo) CreateFolder(_ context.Context, _, _ string, _ domain.CreateFolderInput) (domain.Folder, error) {
	f.createCalled = true
	return domain.Folder{}, nil
}
func (f *fakeAssetRepo) UpdateFolder(_ context.Context, _, _, _ string, _ domain.UpdateFolderInput) (domain.Folder, error) {
	f.updateCalled = true
	return domain.Folder{}, nil
}
func (f *fakeAssetRepo) MoveFolder(_ context.Context, _, _, _ string, _ domain.MoveFolderInput) (domain.Folder, error) {
	return domain.Folder{}, nil
}
func (f *fakeAssetRepo) DeleteFolder(_ context.Context, _, _, _ string) error {
	return nil
}

// GetMetadataItemsByFolder satisfies the metadata list repository contract for use-case tests.
func (f *fakeAssetRepo) GetMetadataItemsByFolder(ctx context.Context, orgID, folderID string) ([]domain.MetadataItem, error) {
	return nil, nil
}

// GetMetadataItemByID satisfies the metadata detail repository contract for use-case tests.
func (f *fakeAssetRepo) GetMetadataItemByID(ctx context.Context, orgID, id string) (domain.MetadataItem, error) {
	return domain.MetadataItem{}, nil
}

// CreateMetadataItem captures the normalized create input passed by the use case.
func (f *fakeAssetRepo) CreateMetadataItem(ctx context.Context, orgID, userID string, input domain.CreateMetadataInput) (domain.MetadataItem, error) {
	f.metadataCreateInput = input
	return domain.MetadataItem{}, nil
}

// UpdateMetadataItem captures the normalized sparse update passed by the use case.
func (f *fakeAssetRepo) UpdateMetadataItem(ctx context.Context, orgID, userID, id string, input domain.UpdateMetadataInput) (domain.MetadataItem, error) {
	f.metadataUpdateInput = input
	return domain.MetadataItem{}, nil
}

// DeleteMetadataItem satisfies the metadata delete repository contract for use-case tests.
func (f *fakeAssetRepo) DeleteMetadataItem(ctx context.Context, orgID, userID, id string) error {
	return nil
}

// SearchMetadataItems satisfies the metadata search repository contract for use-case tests.
func (f *fakeAssetRepo) SearchMetadataItems(ctx context.Context, orgID string, filter domain.MetadataSearchFilter) ([]domain.MetadataItem, error) {
	return nil, nil
}

func TestAssetUsecase_CreateFolder_Validation(t *testing.T) {
	repo := &fakeAssetRepo{}
	uc := usecase.NewAssetUsecase(repo)

	tests := []struct {
		name        string
		input       domain.CreateFolderInput
		expectedErr error
	}{
		{
			name:        "Empty Name",
			input:       domain.CreateFolderInput{Name: "   "},
			expectedErr: domain.ErrInvalidInput,
		},
		{
			name:        "Valid Name",
			input:       domain.CreateFolderInput{Name: "Valid Name"},
			expectedErr: nil,
		},
		{
			name: "Invalid Parent Path",
			input: domain.CreateFolderInput{
				Name:       "Valid Name",
				ParentPath: stringPointer("root..animals"),
			},
			expectedErr: domain.ErrInvalidInput,
		},
		{
			name:        "Name Longer Than 255 Characters",
			input:       domain.CreateFolderInput{Name: strings.Repeat("a", 256)},
			expectedErr: domain.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.CreateFolder(context.Background(), "org1", "user1", tt.input)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("expected %v, got %v", tt.expectedErr, err)
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func TestAssetUsecase_UpdateFolder_Validation(t *testing.T) {
	repo := &fakeAssetRepo{}
	uc := usecase.NewAssetUsecase(repo)

	emptyStr := "   "
	validStr := "Valid"

	tests := []struct {
		name        string
		input       domain.UpdateFolderInput
		expectedErr error
	}{
		{
			name:        "Empty Name",
			input:       domain.UpdateFolderInput{Name: &emptyStr, NameSet: true},
			expectedErr: domain.ErrInvalidInput,
		},
		{
			name:        "Valid Name",
			input:       domain.UpdateFolderInput{Name: &validStr, NameSet: true},
			expectedErr: nil,
		},
		{
			name:        "Null Name",
			input:       domain.UpdateFolderInput{NameSet: true},
			expectedErr: domain.ErrInvalidInput,
		},
		{
			name:        "Clear Description",
			input:       domain.UpdateFolderInput{DescriptionSet: true},
			expectedErr: nil,
		},
		{
			name:        "Missing both",
			input:       domain.UpdateFolderInput{},
			expectedErr: domain.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := uc.UpdateFolder(context.Background(), "org1", "user1", "f1", tt.input)
			if !errors.Is(err, tt.expectedErr) {
				t.Errorf("expected %v, got %v", tt.expectedErr, err)
			}
		})
	}
}
