package repository_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

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
