package repository_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestCreateFolder_Root_Success(t *testing.T) {
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
	userID := "user-1"
	input := domain.CreateFolderInput{
		Name: "New Folder",
	}

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref \\(user_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(user_id\\) DO NOTHING$").
		WithArgs(userID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref \\(org_id\\) VALUES \\(\\$1\\) ON CONFLICT \\(org_id\\) DO NOTHING$").
		WithArgs(orgID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	// Root uniqueness check
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\" WHERE \\(org_id = \\$1 AND nlevel\\(path\\) = 1 AND name = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL$").
		WithArgs(orgID, input.Name).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Insert
	mock.ExpectQuery("^INSERT INTO \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("uuid"))

	mock.ExpectCommit()

	_, err = repo.CreateFolder(context.Background(), orgID, userID, input)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestCreateFolder_Child_Success(t *testing.T) {
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
	userID := "user-1"
	parentPath := "parent_seg"
	input := domain.CreateFolderInput{
		Name:       "New Child",
		ParentPath: &parentPath,
	}

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))

	// Parent load
	mock.ExpectQuery("^SELECT \\* FROM \"folders\" WHERE \\(org_id = \\$1 AND path = \\$2\\) AND \"folders\".\"deleted_at\" IS NULL ORDER BY \"folders\".\"id\" LIMIT \\$3 FOR UPDATE$").
		WithArgs(orgID, parentPath, 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "path"}).AddRow("parent-id", parentPath))

	// Child uniqueness check
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\" WHERE \\(org_id = \\$1 AND path <@ \\$2::ltree AND nlevel\\(path\\) = nlevel\\(\\$3::ltree\\) \\+ 1 AND name = \\$4\\) AND \"folders\".\"deleted_at\" IS NULL$").
		WithArgs(orgID, parentPath, parentPath, input.Name).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))

	// Insert
	mock.ExpectQuery("^INSERT INTO \"folders\"").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("uuid"))

	mock.ExpectCommit()

	folder, err := repo.CreateFolder(context.Background(), orgID, userID, input)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if folder.Path == "" {
		t.Errorf("expected path to be set")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestCreateFolder_DuplicateSibling(t *testing.T) {
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
	userID := "user-1"
	input := domain.CreateFolderInput{
		Name: "New Folder",
	}

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))

	// Root uniqueness check
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\"").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	mock.ExpectRollback()

	_, err = repo.CreateFolder(context.Background(), orgID, userID, input)
	if !errors.Is(err, domain.ErrFolderConflict) {
		t.Errorf("expected ErrFolderConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestCreateFolder_SiblingCheckFailureRollsBack(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: db}), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open gorm db: %v", err)
	}

	expectedErr := errors.New("sibling lookup failed")
	repo := repository.NewAssetRepository(gormDB)

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("^SELECT count\\(\\*\\) FROM \"folders\"").
		WillReturnError(expectedErr)
	mock.ExpectRollback()

	_, err = repo.CreateFolder(context.Background(), "org-1", "user-1", domain.CreateFolderInput{Name: "Folder"})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected sibling lookup error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}

func TestCreateFolder_ParentNotFoundRollsBack(t *testing.T) {
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
	parentPath := "root.missing"

	mock.ExpectBegin()
	mock.ExpectExec("^INSERT INTO user_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("^INSERT INTO organization_ref").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("^SELECT \\* FROM \"folders\"").
		WillReturnError(gorm.ErrRecordNotFound)
	mock.ExpectRollback()

	_, err = repo.CreateFolder(
		context.Background(),
		"org-1",
		"user-1",
		domain.CreateFolderInput{Name: "Folder", ParentPath: &parentPath},
	)
	if !errors.Is(err, domain.ErrFolderNotFound) {
		t.Fatalf("expected ErrFolderNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled expectations: %v", err)
	}
}
