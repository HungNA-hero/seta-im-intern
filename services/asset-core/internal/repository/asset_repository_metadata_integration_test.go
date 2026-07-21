package repository_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

// TestMetadataRepository_PostgresRoundTrip verifies text[], jsonb, nullable fields, and sparse updates on PostgreSQL.
func TestMetadataRepository_PostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && err != gorm.ErrInvalidTransaction {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	ctx := context.Background()
	orgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()
	path := strings.ReplaceAll(folderID, "-", "")

	// Seed only transaction-local shadow refs and a parent folder; cleanup always rolls them back.
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization_ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user_ref: %v", err)
	}
	if err := tx.Exec(
		"INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
		folderID, orgID, path, "Metadata Integration", userID,
	).Error; err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	repo := repository.NewAssetRepository(tx)
	description := "nullable description"
	created, err := repo.CreateMetadataItem(ctx, orgID, userID, domain.CreateMetadataInput{
		FolderID:     folderID,
		Title:        "PostgreSQL round trip",
		Description:  &description,
		Labels:       pq.StringArray{"dog", "outdoor"},
		MetadataJSON: json.RawMessage(`{"verified":true}`),
	})
	if err != nil {
		t.Fatalf("create metadata: %v", err)
	}
	created, err = repo.GetMetadataItemByID(ctx, orgID, created.ID)
	if err != nil {
		t.Fatalf("read created metadata: %v", err)
	}
	if created.Description == nil || *created.Description != description {
		t.Fatalf("unexpected created description: %#v", created.Description)
	}
	var createdJSON map[string]bool
	if err := json.Unmarshal(created.MetadataJSON, &createdJSON); err != nil {
		t.Fatalf("decode created metadata_json: %v", err)
	}
	if len(created.Labels) != 2 || !createdJSON["verified"] {
		t.Fatalf("unexpected array/json round trip: labels=%#v json=%s", created.Labels, created.MetadataJSON)
	}

	emptyLabels := pq.StringArray{}
	defaultJSON := json.RawMessage(`{}`)
	updated, err := repo.UpdateMetadataItem(ctx, orgID, userID, created.ID, domain.UpdateMetadataInput{
		DescriptionSet:  true,
		Labels:          &emptyLabels,
		LabelsSet:       true,
		MetadataJSON:    &defaultJSON,
		MetadataJSONSet: true,
	})
	if err != nil {
		t.Fatalf("update metadata: %v", err)
	}
	updated, err = repo.GetMetadataItemByID(ctx, orgID, updated.ID)
	if err != nil {
		t.Fatalf("read updated metadata: %v", err)
	}
	if updated.Description != nil {
		t.Fatalf("expected SQL NULL description, got %#v", updated.Description)
	}
	if updated.Labels == nil || len(updated.Labels) != 0 {
		t.Fatalf("expected non-nil empty labels array, got %#v", updated.Labels)
	}
	if string(updated.MetadataJSON) != `{}` {
		t.Fatalf("expected metadata_json {}, got %s", updated.MetadataJSON)
	}
}

// TestMetadataRepository_SearchAndDelete_Integration verifies search filtering and deletion on PostgreSQL.
func TestMetadataRepository_SearchAndDelete_Integration(t *testing.T) {
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && err != gorm.ErrInvalidTransaction {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})

	ctx := context.Background()
	orgID := uuid.NewString()
	otherOrgID := uuid.NewString()
	userID := uuid.NewString()
	folderID := uuid.NewString()
	deletedFolderID := uuid.NewString()
	otherFolderID := uuid.NewString()

	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", orgID).Error; err != nil {
		t.Fatalf("seed organization_ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO organization_ref (org_id) VALUES (?)", otherOrgID).Error; err != nil {
		t.Fatalf("seed other organization_ref: %v", err)
	}
	if err := tx.Exec("INSERT INTO user_ref (user_id) VALUES (?)", userID).Error; err != nil {
		t.Fatalf("seed user_ref: %v", err)
	}
	for _, folder := range []struct {
		id    string
		orgID string
		name  string
	}{
		{id: folderID, orgID: orgID, name: "Search Integration Folder"},
		{id: deletedFolderID, orgID: orgID, name: "Deleted Search Folder"},
		{id: otherFolderID, orgID: otherOrgID, name: "Other Organization Folder"},
	} {
		if err := tx.Exec(
			"INSERT INTO folders (id, org_id, path, name, created_by) VALUES (?, ?, ?::ltree, ?, ?)",
			folder.id, folder.orgID, strings.ReplaceAll(folder.id, "-", ""), folder.name, userID,
		).Error; err != nil {
			t.Fatalf("seed folder %s: %v", folder.name, err)
		}
	}

	repo := repository.NewAssetRepository(tx)
	createItem := func(targetOrgID, targetFolderID, title string, labels pq.StringArray, category, externalSource *string) domain.MetadataItem {
		t.Helper()
		var externalID *string
		if externalSource != nil {
			value := "fixture-" + uuid.NewString()
			externalID = &value
		}
		item, err := repo.CreateMetadataItem(ctx, targetOrgID, userID, domain.CreateMetadataInput{
			FolderID:       targetFolderID,
			Title:          title,
			Labels:         labels,
			Category:       category,
			ExternalSource: externalSource,
			ExternalID:     externalID,
			MetadataJSON:   json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatalf("create metadata %q: %v", title, err)
		}
		return item
	}

	assertSingleSearchResult := func(query string, expectedID string) {
		t.Helper()
		items, err := repo.SearchMetadataItems(ctx, orgID, domain.MetadataSearchFilter{
			Query: &query,
			Limit: 100,
		})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(items) != 1 || items[0].ID != expectedID {
			t.Fatalf("search %q: expected only %s, got %#v", query, expectedID, items)
		}
	}

	percentItem := createItem(orgID, folderID, "Literal 100% asset", pq.StringArray{"literal"}, nil, nil)
	underscoreItem := createItem(orgID, folderID, "Literal under_score asset", pq.StringArray{"literal"}, nil, nil)
	backslashItem := createItem(orgID, folderID, `Literal back\slash asset`, pq.StringArray{"literal"}, nil, nil)
	assertSingleSearchResult("100%", percentItem.ID)
	assertSingleSearchResult("under_score", underscoreItem.ID)
	assertSingleSearchResult(`back\slash`, backslashItem.ID)

	category := "photo"
	externalSource := "dam"
	combinedItem := createItem(
		orgID,
		folderID,
		"Combined Search Target",
		pq.StringArray{"alpha", "beta"},
		&category,
		&externalSource,
	)
	combinedQuery := "Combined"
	combinedItems, err := repo.SearchMetadataItems(ctx, orgID, domain.MetadataSearchFilter{
		FolderID:       &folderID,
		Query:          &combinedQuery,
		Labels:         []string{"alpha", "beta"},
		Category:       &category,
		ExternalSource: &externalSource,
		Limit:          100,
	})
	if err != nil {
		t.Fatalf("combined search: %v", err)
	}
	if len(combinedItems) != 1 || combinedItems[0].ID != combinedItem.ID {
		t.Fatalf("expected combined search item %s, got %#v", combinedItem.ID, combinedItems)
	}

	pageItems := []domain.MetadataItem{
		createItem(orgID, folderID, "Page Stable One", nil, nil, nil),
		createItem(orgID, folderID, "Page Stable Two", nil, nil, nil),
		createItem(orgID, folderID, "Page Stable Three", nil, nil, nil),
	}
	stableTime := time.Date(2026, time.July, 3, 0, 0, 0, 0, time.UTC)
	pageIDs := make([]string, 0, len(pageItems))
	for _, item := range pageItems {
		pageIDs = append(pageIDs, item.ID)
		if err := tx.Model(&domain.MetadataItem{}).Where("id = ?", item.ID).Update("updated_at", stableTime).Error; err != nil {
			t.Fatalf("set stable pagination timestamp: %v", err)
		}
	}
	sort.Strings(pageIDs)
	pageQuery := "Page Stable"
	firstPage, err := repo.SearchMetadataItems(ctx, orgID, domain.MetadataSearchFilter{Query: &pageQuery, Limit: 2})
	if err != nil {
		t.Fatalf("search first page: %v", err)
	}
	secondPage, err := repo.SearchMetadataItems(ctx, orgID, domain.MetadataSearchFilter{Query: &pageQuery, Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("search second page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != pageIDs[0] || firstPage[1].ID != pageIDs[1] {
		t.Fatalf("unexpected first stable page: %#v", firstPage)
	}
	if len(secondPage) != 1 || secondPage[0].ID != pageIDs[2] {
		t.Fatalf("unexpected second stable page: %#v", secondPage)
	}

	deletedItem := createItem(orgID, folderID, "Hidden Deleted Metadata", nil, nil, nil)
	createItem(orgID, deletedFolderID, "Hidden Deleted Folder Metadata", nil, nil, nil)
	createItem(otherOrgID, otherFolderID, "Hidden Other Organization Metadata", nil, nil, nil)
	if err := tx.Model(&domain.MetadataItem{}).Where("id = ?", deletedItem.ID).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatalf("soft-delete metadata fixture: %v", err)
	}
	if err := tx.Model(&domain.Folder{}).Where("id = ?", deletedFolderID).Update("deleted_at", time.Now()).Error; err != nil {
		t.Fatalf("soft-delete folder fixture: %v", err)
	}
	hiddenQuery := "Hidden"
	hiddenItems, err := repo.SearchMetadataItems(ctx, orgID, domain.MetadataSearchFilter{Query: &hiddenQuery, Limit: 100})
	if err != nil {
		t.Fatalf("search hidden fixtures: %v", err)
	}
	if len(hiddenItems) != 0 {
		t.Fatalf("expected deleted and cross-org rows to be excluded, got %#v", hiddenItems)
	}

	deleteItem := createItem(orgID, folderID, "Hard Delete Target", nil, nil, nil)
	if err := repo.DeleteMetadataItem(ctx, orgID, userID, deleteItem.ID); err != nil {
		t.Fatalf("delete metadata: %v", err)
	}
	var deletedCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM metadata_items WHERE id = ?", deleteItem.ID).Scan(&deletedCount).Error; err != nil {
		t.Fatalf("count hard-deleted metadata: %v", err)
	}
	if deletedCount != 0 {
		t.Fatalf("expected hard-deleted metadata row to be absent, got %d row", deletedCount)
	}
	if err := repo.DeleteMetadataItem(ctx, orgID, userID, deleteItem.ID); !errors.Is(err, domain.ErrMetadataNotFound) {
		t.Fatalf("expected ErrMetadataNotFound on double delete, got %v", err)
	}
	if err := repo.DeleteMetadataItem(ctx, otherOrgID, userID, combinedItem.ID); !errors.Is(err, domain.ErrMetadataNotFound) {
		t.Fatalf("expected ErrMetadataNotFound for wrong org, got %v", err)
	}
	if err := repo.DeleteMetadataItem(ctx, orgID, userID, uuid.NewString()); !errors.Is(err, domain.ErrMetadataNotFound) {
		t.Fatalf("expected ErrMetadataNotFound for missing item, got %v", err)
	}

	rollbackItem := createItem(orgID, folderID, "Hard Delete Rollback Target", nil, nil, nil)
	if err := tx.Exec(`
		CREATE FUNCTION kan37_reject_metadata_delete() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'forced delete failure';
			RETURN OLD;
		END;
		$$ LANGUAGE plpgsql;
		CREATE TRIGGER kan37_reject_metadata_delete
		BEFORE DELETE ON metadata_items
		FOR EACH ROW EXECUTE FUNCTION kan37_reject_metadata_delete();
	`).Error; err != nil {
		t.Fatalf("create rollback trigger: %v", err)
	}
	if err := repo.DeleteMetadataItem(ctx, orgID, userID, rollbackItem.ID); err == nil {
		t.Fatal("expected forced delete failure")
	}
	var rollbackCount int64
	if err := tx.Raw("SELECT COUNT(*) FROM metadata_items WHERE id = ?", rollbackItem.ID).Scan(&rollbackCount).Error; err != nil {
		t.Fatalf("read rollback item: %v", err)
	}
	if rollbackCount != 1 {
		t.Fatalf("expected rollback item to remain after failed hard delete, got %d row", rollbackCount)
	}
	if err := tx.Exec("DROP TRIGGER kan37_reject_metadata_delete ON metadata_items; DROP FUNCTION kan37_reject_metadata_delete();").Error; err != nil {
		t.Fatalf("drop rollback trigger: %v", err)
	}
}
