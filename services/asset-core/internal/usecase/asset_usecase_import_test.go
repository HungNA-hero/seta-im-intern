package usecase_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

// MockAssetRepository is needed to satisfy the repository interface.
// We can use a simple mock that just returns a success summary.
type mockImportRepo struct {
	mock.Mock
	domain.AssetRepository
}

// ImportSampleTransaction records the normalized dataset passed across the repository boundary.
func (m *mockImportRepo) ImportSampleTransaction(ctx context.Context, orgID, userID string, dataset domain.ImportDataset, dryRun bool) (domain.ImportSummary, error) {
	args := m.Called(ctx, orgID, userID, dataset, dryRun)
	return args.Get(0).(domain.ImportSummary), args.Error(1)
}

// TestImportSample_Validation covers strict parsing, normalization, graph ordering, and cancellation.
func TestImportSample_Validation(t *testing.T) {
	mockRepo := new(mockImportRepo)
	uc := usecase.NewAssetUsecase(mockRepo)

	ctx := context.Background()
	orgID := "org-1"
	userID := "user-1"

	t.Run("Valid Dataset", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [
				{"key": "animals", "name": "Animals"}
			],
			"metadata": [
				{"folder_key": "animals", "external_id": "item1", "title": "Dog", "labels": ["dog"], "metadata_json": {}}
			]
		}`

		// Mock the transaction call
		mockRepo.On("ImportSampleTransaction", mock.Anything, orgID, userID, mock.AnythingOfType("domain.ImportDataset"), false).
			Return(domain.ImportSummary{Status: "success"}, nil).Once()

		summary, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.NoError(t, err)
		assert.Equal(t, "success", summary.Status)
		mockRepo.AssertExpectations(t)
	})

	t.Run("Invalid Version", func(t *testing.T) {
		payload := `{"version": 2, "external_source": "open_images_v7", "folders": [], "metadata": []}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "InvalidVersion")
	})

	t.Run("Unknown Field (Strict Decode)", func(t *testing.T) {
		payload := `{"version": 1, "external_source": "open_images_v7", "folders": [], "metadata": [], "unknown_field": 1}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "InvalidSchema")
		assert.Contains(t, err.Error(), "unknown_field")
	})

	t.Run("Unsupported External Source", func(t *testing.T) {
		payload := `{"version": 1, "external_source": "invalid_source", "folders": [], "metadata": []}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "InvalidSource")
	})

	t.Run("Duplicate Folder Key", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [
				{"key": "dup", "name": "A"},
				{"key": "dup", "name": "B"}
			],
			"metadata": []
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DuplicateFolderKey")
	})

	t.Run("Cyclic Graph", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [
				{"key": "a", "name": "A", "parent_key": "b"},
				{"key": "b", "name": "B", "parent_key": "a"}
			],
			"metadata": []
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "CyclicGraph")
	})

	t.Run("Unknown Folder in Metadata", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [],
			"metadata": [
				{"folder_key": "unknown", "external_id": "1", "title": "Title", "metadata_json": {}}
			]
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "InvalidMetadata")
		assert.Contains(t, err.Error(), "unknown folder_key")
	})

	t.Run("Duplicate External ID", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [{"key": "f1", "name": "F1"}],
			"metadata": [
				{"folder_key": "f1", "external_id": "id1", "title": "T1", "metadata_json": {}},
				{"folder_key": "f1", "external_id": "id1", "title": "T2", "metadata_json": {}}
			]
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DuplicateExternalID")
	})

	t.Run("Limits Check", func(t *testing.T) {
		// Test too many folders
		var folders []string
		for i := 0; i < 1001; i++ {
			folders = append(folders, `{"key": "f`+strconv.Itoa(i)+`", "name": "F"}`)
		}
		payload := `{"version": 1, "external_source": "open_images_v7", "folders": [` + strings.Join(folders, ",") + `], "metadata": []}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max 1000 folders")

		// Test too many metadata items
		var meta []string
		for i := 0; i < 10001; i++ {
			meta = append(meta, `{"folder_key": "f0", "external_id": "i`+strconv.Itoa(i)+`", "title": "T", "metadata_json": {}}`)
		}
		payloadMeta := `{"version": 1, "external_source": "open_images_v7", "folders": [{"key": "f0", "name": "F"}], "metadata": [` + strings.Join(meta, ",") + `]}`
		_, err = uc.ImportSample(ctx, orgID, userID, []byte(payloadMeta), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "max 10000 metadata items")
	})

	t.Run("Missing Parent Key", func(t *testing.T) {
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [
				{"key": "a", "name": "A", "parent_key": "missing"}
			],
			"metadata": []
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown parent_key")
	})

	t.Run("Invalid Text/URL Fields", func(t *testing.T) {
		longTitle := strings.Repeat("A", 256)
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [{"key": "f1", "name": "F1"}],
			"metadata": [
				{"folder_key": "f1", "external_id": "id1", "title": "` + longTitle + `", "metadata_json": {}}
			]
		}`
		_, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "title must be non-blank and <= 255 runes")

		longLicense := strings.Repeat("L", 256)
		payload2 := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [{"key": "f1", "name": "F1"}],
			"metadata": [
				{"folder_key": "f1", "external_id": "id1", "title": "T", "license": "` + longLicense + `", "metadata_json": {}}
			]
		}`
		_, err = uc.ImportSample(ctx, orgID, userID, []byte(payload2), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "field length limit exceeded")
	})

	t.Run("Deterministic Topological Order", func(t *testing.T) {
		// D depends on C, C depends on B, B depends on A
		payload := `{
			"version": 1,
			"external_source": "open_images_v7",
			"folders": [
				{"key": "d", "name": "D", "parent_key": "c"},
				{"key": "a", "name": "A"},
				{"key": "c", "name": "C", "parent_key": "b"},
				{"key": "b", "name": "B", "parent_key": "a"}
			],
			"metadata": []
		}`

		mockRepo.On("ImportSampleTransaction", mock.Anything, orgID, userID, mock.AnythingOfType("domain.ImportDataset"), false).
			Run(func(args mock.Arguments) {
				ds := args.Get(3).(domain.ImportDataset)
				require.Len(t, ds.Folders, 4)
				assert.Equal(t, "a", ds.Folders[0].Key)
				assert.Equal(t, "b", ds.Folders[1].Key)
				assert.Equal(t, "c", ds.Folders[2].Key)
				assert.Equal(t, "d", ds.Folders[3].Key)
			}).
			Return(domain.ImportSummary{Status: "success"}, nil).Once()

		summary, err := uc.ImportSample(ctx, orgID, userID, []byte(payload), false)
		require.NoError(t, err)
		assert.Equal(t, "success", summary.Status)
		mockRepo.AssertExpectations(t)
	})

	t.Run("Cancellation", func(t *testing.T) {
		payload := `{"version": 1, "external_source": "open_images_v7", "folders": [], "metadata": []}`
		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		mockRepo.On("ImportSampleTransaction", canceledCtx, orgID, userID, mock.AnythingOfType("domain.ImportDataset"), false).
			Return(domain.ImportSummary{}, context.Canceled).Once()

		_, err := uc.ImportSample(canceledCtx, orgID, userID, []byte(payload), false)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "canceled")
	})
}
