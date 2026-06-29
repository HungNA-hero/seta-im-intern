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
