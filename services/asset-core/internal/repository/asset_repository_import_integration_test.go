package repository_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

// TestImportSampleTransaction_Integration verifies atomic import behavior against PostgreSQL.
func TestImportSampleTransaction_Integration(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("Skipping integration test; ASSET_TEST_DATABASE_URL not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	// Use transaction for cleanup (note: ImportSampleTransaction opens its own transaction,
	// so rollback logic might be complex if nested transactions aren't supported.
	// We will let ImportSampleTransaction write to the DB and assume tests isolate by OrgID).
	db := database
	repo := repository.NewAssetRepository(db)
	ctx := context.Background()

	orgID1 := "00000000-0000-0000-0000-000000000001"
	orgID2 := "00000000-0000-0000-0000-000000000002"
	userID := "00000000-0000-0000-0000-000000000003"

	// Use setup context
	require.NoError(t, repo.EnsureRefs(ctx, userID, orgID1))
	require.NoError(t, repo.EnsureRefs(ctx, userID, orgID2))

	dataset := domain.ImportDataset{
		Version:        1,
		ExternalSource: "open_images_v7",
		Folders: []domain.ImportFolder{
			{Key: "root", Name: "Root Folder"},
			{Key: "child", ParentKey: ptr("root"), Name: "Child Folder"},
		},
		Metadata: []domain.ImportMetadataItem{
			{
				FolderKey:    "child",
				ExternalID:   "ext-1",
				Title:        "Item 1",
				MetadataJSON: json.RawMessage(`{}`),
				Labels:       []string{"l1"},
			},
		},
	}

	t.Run("Dry Run Leaves DB Unchanged", func(t *testing.T) {
		dryRunOrg := "00000000-0000-0000-0000-000000000099"
		dryRunUser := "00000000-0000-0000-0000-000000000098"

		summary, err := repo.ImportSampleTransaction(ctx, dryRunOrg, dryRunUser, dataset, true)
		require.NoError(t, err)
		assert.True(t, summary.DryRun)
		assert.Equal(t, 2, summary.FoldersCreated)
		assert.Equal(t, 1, summary.MetadataCreated)

		// Verify folders were not inserted
		folders, _ := repo.GetRootFolders(ctx, dryRunOrg)
		assert.Empty(t, folders)

		// Verify shadow refs were rolled back
		var userCount int64
		db.Table("user_ref").Where("user_id = ?", dryRunUser).Count(&userCount)
		assert.Equal(t, int64(0), userCount)

		var orgCount int64
		db.Table("organization_ref").Where("org_id = ?", dryRunOrg).Count(&orgCount)
		assert.Equal(t, int64(0), orgCount)
	})

	t.Run("First Run Creates", func(t *testing.T) {
		summary, err := repo.ImportSampleTransaction(ctx, orgID1, userID, dataset, false)
		require.NoError(t, err)
		assert.Equal(t, 2, summary.FoldersCreated)
		assert.Equal(t, 0, summary.FoldersReused)
		assert.Equal(t, 1, summary.MetadataCreated)

		folders, _ := repo.GetRootFolders(ctx, orgID1)
		assert.Len(t, folders, 1)
		assert.Equal(t, "Root Folder", folders[0].Name)
	})

	t.Run("Second Run Reuses and Updates", func(t *testing.T) {
		// Modify dataset slightly to ensure update happens
		dataset.Metadata[0].Title = "Item 1 Updated"

		summary, err := repo.ImportSampleTransaction(ctx, orgID1, userID, dataset, false)
		require.NoError(t, err)
		assert.Equal(t, 0, summary.FoldersCreated)
		assert.Equal(t, 2, summary.FoldersReused)
		assert.Equal(t, 0, summary.MetadataCreated)
		assert.Equal(t, 1, summary.MetadataUpdated)

		// Verify update
		items, err := repo.SearchMetadataItems(ctx, orgID1, domain.MetadataSearchFilter{
			ExternalSource: ptr("open_images_v7"),
			Limit:          100,
		})
		require.NoError(t, err)
		require.Len(t, items, 1)
		assert.Equal(t, "Item 1 Updated", items[0].Title)
	})

	t.Run("Third Run Identical Data", func(t *testing.T) {
		// Run with dataset so it matches exactly what's in DB
		summary, err := repo.ImportSampleTransaction(ctx, orgID1, userID, dataset, false)
		require.NoError(t, err)
		assert.Equal(t, 0, summary.FoldersCreated)
		assert.Equal(t, 2, summary.FoldersReused)
		assert.Equal(t, 0, summary.MetadataCreated)
		assert.Equal(t, 0, summary.MetadataUpdated)
		assert.Equal(t, 1, summary.MetadataUnchanged)
	})

	t.Run("Hard Deleted Item Reimports", func(t *testing.T) {
		// KAN-59 physically deletes the item, so importing the same external
		// identity creates a new active Asset row instead of reactivating a tombstone.
		items, _ := repo.SearchMetadataItems(ctx, orgID1, domain.MetadataSearchFilter{
			ExternalSource: ptr("open_images_v7"),
			Limit:          100,
		})
		require.Len(t, items, 1)
		err := repo.DeleteMetadataItem(ctx, orgID1, userID, items[0].ID)
		require.NoError(t, err)

		// Re-run import
		summary, err := repo.ImportSampleTransaction(ctx, orgID1, userID, dataset, false)
		require.NoError(t, err)
		assert.Equal(t, 1, summary.MetadataCreated)
		assert.Equal(t, 0, summary.MetadataUpdated)
		assert.Equal(t, 0, summary.MetadataUnchanged)

		// Verify a new active row is present. Import records the actor in both
		// create and update audit fields for its upsert-compatible insert path.
		itemsAfter, err := repo.SearchMetadataItems(ctx, orgID1, domain.MetadataSearchFilter{
			ExternalSource: ptr("open_images_v7"),
			Limit:          100,
		})
		require.NoError(t, err)
		require.Len(t, itemsAfter, 1)
		assert.False(t, itemsAfter[0].DeletedAt.Valid)
		assert.Equal(t, userID, itemsAfter[0].CreatedBy)
		require.NotNil(t, itemsAfter[0].UpdatedBy)
		assert.Equal(t, userID, *itemsAfter[0].UpdatedBy)
	})

	t.Run("Same Identity Another Org Conflicts", func(t *testing.T) {
		// Attempt to import the same dataset into orgID2
		_, err := repo.ImportSampleTransaction(ctx, orgID2, userID, dataset, false)
		require.Error(t, err)

		var importErr *domain.ImportError
		require.ErrorAs(t, err, &importErr)
		assert.Equal(t, "ConflictError", importErr.Type)
		assert.Contains(t, importErr.Message, "belongs to another org")

		// Prove isolation
		items, _ := repo.SearchMetadataItems(ctx, orgID2, domain.MetadataSearchFilter{
			ExternalSource: ptr("open_images_v7"),
			Limit:          100,
		})
		assert.Empty(t, items)

		// Prove folders were rolled back
		var folderCount int64
		db.Table("folders").Where("org_id = ?", orgID2).Count(&folderCount)
		assert.Equal(t, int64(0), folderCount)

		// Prove org1 was untouched
		itemsOrg1, _ := repo.SearchMetadataItems(ctx, orgID1, domain.MetadataSearchFilter{
			ExternalSource: ptr("open_images_v7"),
			Limit:          100,
		})
		assert.Len(t, itemsOrg1, 1)
	})

	t.Run("Late Failure Rolls Back", func(t *testing.T) {
		lateFailOrg := "00000000-0000-0000-0000-000000000097"
		lateFailUser := "00000000-0000-0000-0000-000000000096"

		badDataset := dataset
		badDataset.Folders = []domain.ImportFolder{
			{Key: "root3", Name: "Root 3"},
		}
		badDataset.Metadata = []domain.ImportMetadataItem{
			{
				FolderKey:    "root3",
				ExternalID:   "ext-bad",
				Title:        "Bad",
				MetadataJSON: json.RawMessage(`bad json`), // Postgres will reject invalid jsonb
			},
		}

		_, err := repo.ImportSampleTransaction(ctx, lateFailOrg, lateFailUser, badDataset, false)
		require.Error(t, err)

		// Verify folder "Root 3" was not persisted due to rollback
		var count int64
		db.Table("folders").Where("name = ? AND org_id = ?", "Root 3", lateFailOrg).Count(&count)
		assert.Equal(t, int64(0), count)

		// Verify shadow refs were rolled back
		var userCount int64
		db.Table("user_ref").Where("user_id = ?", lateFailUser).Count(&userCount)
		assert.Equal(t, int64(0), userCount)

		var orgCount int64
		db.Table("organization_ref").Where("org_id = ?", lateFailOrg).Count(&orgCount)
		assert.Equal(t, int64(0), orgCount)
	})
}

// ptr creates nullable string fixtures without mutable package-level state.
func ptr(s string) *string {
	return &s
}
