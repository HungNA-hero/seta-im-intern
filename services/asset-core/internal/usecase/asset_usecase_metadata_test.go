package usecase_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lib/pq"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/usecase"
)

// TestAssetUsecase_CreateMetadata_Validation covers create invariants before repository delegation.
func TestAssetUsecase_CreateMetadata_Validation(t *testing.T) {
	tests := []struct {
		name    string
		input   domain.CreateMetadataInput
		wantErr error
	}{
		{
			name: "valid labels and JSON object",
			input: domain.CreateMetadataInput{
				FolderID:     "folder-1",
				Title:        "Title",
				Labels:       pq.StringArray{"label1"},
				MetadataJSON: json.RawMessage(`{"key":"value"}`),
			},
		},
		{name: "missing folder", input: domain.CreateMetadataInput{Title: "Title"}, wantErr: domain.ErrInvalidInput},
		{name: "blank title", input: domain.CreateMetadataInput{FolderID: "folder-1", Title: "  "}, wantErr: domain.ErrInvalidInput},
		{name: "invalid JSON", input: domain.CreateMetadataInput{FolderID: "folder-1", Title: "Title", MetadataJSON: json.RawMessage(`{bad}`)}, wantErr: domain.ErrInvalidInput},
		{name: "JSON array", input: domain.CreateMetadataInput{FolderID: "folder-1", Title: "Title", MetadataJSON: json.RawMessage(`[]`)}, wantErr: domain.ErrInvalidInput},
		{name: "blank label", input: domain.CreateMetadataInput{FolderID: "folder-1", Title: "Title", Labels: pq.StringArray{"ok", "  "}}, wantErr: domain.ErrInvalidInput},
		{
			name:    "incomplete external pair",
			input:   domain.CreateMetadataInput{FolderID: "folder-1", Title: "Title", ExternalSource: stringPointer("source")},
			wantErr: domain.ErrInvalidInput,
		},
		{
			name:    "blank external identity",
			input:   domain.CreateMetadataInput{FolderID: "folder-1", Title: "Title", ExternalSource: stringPointer("  "), ExternalID: stringPointer("id")},
			wantErr: domain.ErrInvalidInput,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &fakeAssetRepo{}
			uc := usecase.NewAssetUsecase(repo)
			_, err := uc.CreateMetadataItem(context.Background(), "org-1", "user-1", testCase.input)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
		})
	}
}

// TestAssetUsecase_CreateMetadata_NormalizesDefaults verifies values passed into the write transaction.
func TestAssetUsecase_CreateMetadata_NormalizesDefaults(t *testing.T) {
	repo := &fakeAssetRepo{}
	uc := usecase.NewAssetUsecase(repo)

	_, err := uc.CreateMetadataItem(context.Background(), "org-1", "user-1", domain.CreateMetadataInput{
		FolderID:       " folder-1 ",
		Title:          " Title ",
		Labels:         pq.StringArray{" dog ", "dog", "animal"},
		ExternalSource: stringPointer(" source "),
		ExternalID:     stringPointer(" id "),
		MetadataJSON:   json.RawMessage("null"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.metadataCreateInput.FolderID != "folder-1" || repo.metadataCreateInput.Title != "Title" {
		t.Fatalf("expected trimmed folder/title, got %#v", repo.metadataCreateInput)
	}
	if !reflect.DeepEqual(repo.metadataCreateInput.Labels, pq.StringArray{"dog", "animal"}) {
		t.Fatalf("unexpected normalized labels: %#v", repo.metadataCreateInput.Labels)
	}
	if string(repo.metadataCreateInput.MetadataJSON) != "{}" {
		t.Fatalf("expected metadata_json default {}, got %s", repo.metadataCreateInput.MetadataJSON)
	}
	if *repo.metadataCreateInput.ExternalSource != "source" || *repo.metadataCreateInput.ExternalID != "id" {
		t.Fatalf("expected trimmed external identity")
	}
}

// TestAssetUsecase_UpdateMetadata_Validation covers sparse-field validation and explicit-null normalization.
func TestAssetUsecase_UpdateMetadata_Validation(t *testing.T) {
	tests := []struct {
		name    string
		input   domain.UpdateMetadataInput
		wantErr error
	}{
		{name: "valid title", input: domain.UpdateMetadataInput{Title: stringPointer("New Title"), TitleSet: true}},
		{name: "empty update", input: domain.UpdateMetadataInput{}, wantErr: domain.ErrInvalidInput},
		{name: "explicit null title", input: domain.UpdateMetadataInput{TitleSet: true}, wantErr: domain.ErrInvalidInput},
		{name: "blank label", input: domain.UpdateMetadataInput{Labels: stringArrayPointer(" "), LabelsSet: true}, wantErr: domain.ErrInvalidInput},
		{name: "JSON array", input: domain.UpdateMetadataInput{MetadataJSON: rawMessagePointer(`[]`), MetadataJSONSet: true}, wantErr: domain.ErrInvalidInput},
		{name: "JSON null value", input: domain.UpdateMetadataInput{MetadataJSON: rawMessagePointer(`null`), MetadataJSONSet: true}, wantErr: domain.ErrInvalidInput},
		{name: "blank external source", input: domain.UpdateMetadataInput{ExternalSource: stringPointer(" "), ExternalSourceSet: true}, wantErr: domain.ErrInvalidInput},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &fakeAssetRepo{}
			uc := usecase.NewAssetUsecase(repo)
			_, err := uc.UpdateMetadataItem(context.Background(), "org-1", "user-1", "id-1", testCase.input)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("expected %v, got %v", testCase.wantErr, err)
			}
		})
	}
}

// TestAssetUsecase_UpdateMetadata_ExplicitNullDefaults verifies database-safe clear values for non-null columns.
func TestAssetUsecase_UpdateMetadata_ExplicitNullDefaults(t *testing.T) {
	repo := &fakeAssetRepo{}
	uc := usecase.NewAssetUsecase(repo)

	_, err := uc.UpdateMetadataItem(context.Background(), "org-1", "user-1", "id-1", domain.UpdateMetadataInput{
		LabelsSet:       true,
		MetadataJSONSet: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.metadataUpdateInput.Labels == nil || len(*repo.metadataUpdateInput.Labels) != 0 {
		t.Fatalf("expected explicit null labels to become an empty array")
	}
	if repo.metadataUpdateInput.MetadataJSON == nil || string(*repo.metadataUpdateInput.MetadataJSON) != "{}" {
		t.Fatalf("expected explicit null metadata_json to become {}")
	}
}

// stringArrayPointer creates a metadata label array pointer for sparse-update tests.
func stringArrayPointer(values ...string) *pq.StringArray {
	labels := pq.StringArray(values)
	return &labels
}

// rawMessagePointer creates a JSON pointer for sparse-update tests.
func rawMessagePointer(value string) *json.RawMessage {
	message := json.RawMessage(value)
	return &message
}
