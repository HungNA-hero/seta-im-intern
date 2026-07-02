package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestEnsureRefs_Success_RepeatedCall(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: db,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	userID := "00000000-0000-0000-0000-000000000001"
	orgID := "00000000-0000-0000-0000-000000000002"

	// First call inserts both references.
	mock.ExpectExec("INSERT INTO user_ref \\(user_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(user_id\\) DO NOTHING").
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	mock.ExpectExec("INSERT INTO organization_ref \\(org_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(org_id\\) DO NOTHING").
		WithArgs(orgID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = repo.EnsureRefs(context.Background(), userID, orgID)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	// Second call simulates ON CONFLICT DO NOTHING for existing references.
	mock.ExpectExec("INSERT INTO user_ref \\(user_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(user_id\\) DO NOTHING").
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	mock.ExpectExec("INSERT INTO organization_ref \\(org_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(org_id\\) DO NOTHING").
		WithArgs(orgID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := repo.EnsureRefs(context.Background(), userID, orgID); err != nil {
		t.Errorf("expected repeated call to succeed, got %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestEnsureRefs_OrganizationFailurePropagated(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: db,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)
	userID := "00000000-0000-0000-0000-000000000001"
	orgID := "00000000-0000-0000-0000-000000000002"
	expectedErr := errors.New("organization insert failed")

	mock.ExpectExec("INSERT INTO user_ref \\(user_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(user_id\\) DO NOTHING").
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO organization_ref \\(org_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(org_id\\) DO NOTHING").
		WithArgs(orgID).
		WillReturnError(expectedErr)

	err = repo.EnsureRefs(context.Background(), userID, orgID)
	if !errors.Is(err, expectedErr) {
		t.Errorf("expected %v, got %v", expectedErr, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestEnsureRefs_DBFailurePropagated(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn: db,
	}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	userID := "00000000-0000-0000-0000-000000000001"
	orgID := "00000000-0000-0000-0000-000000000002"
	expectedErr := errors.New("db connection lost")

	mock.ExpectExec("INSERT INTO user_ref \\(user_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(user_id\\) DO NOTHING").
		WithArgs(userID).
		WillReturnError(expectedErr)

	err = repo.EnsureRefs(context.Background(), userID, orgID)
	if err == nil {
		t.Errorf("expected error, got nil")
	} else if err != expectedErr {
		t.Errorf("expected %v, got %v", expectedErr, err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderByID_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	folderID := "folder-1"

	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"}).
		AddRow(folderID, orgID, "root", "Root Folder", "user-1")

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(id = \\$1 AND org_id = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY \"folders\".\"id\" LIMIT \\$3$").
		WithArgs(folderID, orgID, 1).
		WillReturnRows(rows)

	folder, err := repo.GetFolderByID(context.Background(), orgID, folderID)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if folder.ID != folderID {
		t.Errorf("expected folder ID %s, got %s", folderID, folder.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderByID_NotFoundOrOrgMismatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	folderID := "folder-1"

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(id = \\$1 AND org_id = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY \"folders\".\"id\" LIMIT \\$3$").
		WithArgs(folderID, orgID, 1).
		WillReturnError(gorm.ErrRecordNotFound)

	_, err = repo.GetFolderByID(context.Background(), orgID, folderID)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderByID_SoftDeleted(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	folderID := "folder-1"

	// If soft-deleted, GORM won't find it due to IS NULL clause
	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(id = \\$1 AND org_id = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY \"folders\".\"id\" LIMIT \\$3$").
		WithArgs(folderID, orgID, 1).
		WillReturnError(gorm.ErrRecordNotFound)

	_, err = repo.GetFolderByID(context.Background(), orgID, folderID)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Errorf("expected ErrRecordNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderChildren_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	parentPath := "root"

	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"}).
		AddRow("folder-2", orgID, "root.folder_2", "Folder 2", "user-1").
		AddRow("folder-3", orgID, "root.folder_3", "Folder 3", "user-1")

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(org_id = \\$1 AND path <@ \\$2 AND nlevel\\(path\\) = nlevel\\(\\$3::ltree\\) \\+ 1\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY name ASC$").
		WithArgs(orgID, parentPath, parentPath).
		WillReturnRows(rows)

	folders, err := repo.GetFolderChildren(context.Background(), orgID, parentPath)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(folders) != 2 {
		t.Errorf("expected 2 folders, got %d", len(folders))
	}
	if folders[0].Name != "Folder 2" || folders[1].Name != "Folder 3" {
		t.Errorf("expected sorted folders")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetRootFolders_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"

	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"}).
		AddRow("folder-1", orgID, "folder_1", "Folder 1", "user-1").
		AddRow("folder-2", orgID, "folder_2", "Folder 2", "user-1")

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(org_id = \\$1 AND nlevel\\(path\\) = 1\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY name ASC$").
		WithArgs(orgID).
		WillReturnRows(rows)

	folders, err := repo.GetRootFolders(context.Background(), orgID)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(folders) != 2 {
		t.Errorf("expected 2 folders, got %d", len(folders))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderChildren_Empty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	parentPath := "root"

	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"})

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(org_id = \\$1 AND path <@ \\$2 AND nlevel\\(path\\) = nlevel\\(\\$3::ltree\\) \\+ 1\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY name ASC$").
		WithArgs(orgID, parentPath, parentPath).
		WillReturnRows(rows)

	folders, err := repo.GetFolderChildren(context.Background(), orgID, parentPath)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(folders) != 0 {
		t.Errorf("expected 0 folders, got %d", len(folders))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderTree_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	repo := repository.NewAssetRepository(gormDB)

	orgID := "org-1"
	rootPath := "root"

	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"}).
		AddRow("folder-1", orgID, "root", "Root", "user-1").
		AddRow("folder-2", orgID, "root.folder_2", "Folder 2", "user-1")

	// Verify ordering assertion for folderTree
	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE org_id = \\$1 AND path <@ \\$2 AND \"folders\".\"deleted_at\" IS NULL ORDER BY path ASC$").
		WithArgs(orgID, rootPath).
		WillReturnRows(rows)

	folders, err := repo.GetFolderTree(context.Background(), orgID, rootPath)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if len(folders) != 2 {
		t.Errorf("expected 2 folders, got %d", len(folders))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unfulfilled expectations: %s", err)
	}
}

func TestGetFolderTree_EmptyRootReturnsOrganizationForest(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}
	repo := repository.NewAssetRepository(gormDB)
	orgID := "org-1"
	rows := sqlmock.NewRows([]string{"id", "org_id", "path", "name", "created_by"}).
		AddRow("root", orgID, "root", "Root", "user-1").
		AddRow("child", orgID, "root.child", "Child", "user-1")

	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE org_id = \\$1 AND \"folders\"\\.\"deleted_at\" IS NULL ORDER BY path ASC$").
		WithArgs(orgID).
		WillReturnRows(rows)

	folders, err := repo.GetFolderTree(context.Background(), orgID, "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(folders) != 2 {
		t.Fatalf("expected full two-node forest, got %d folders", len(folders))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %s", err)
	}
}
