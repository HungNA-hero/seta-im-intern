package usecase_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type mutableMediaStatusClock struct{ at time.Time }

func (clock *mutableMediaStatusClock) Now() time.Time { return clock.at }

func TestMediaStatus_CompletedSignsOnlyItsTwoRecordedOutputsForFifteenMinutes(t *testing.T) {
	now := time.Date(2026, time.August, 19, 11, 0, 0, 0, time.UTC)
	startedAt := now.Add(-30 * time.Second)
	completedAt := now.Add(-5 * time.Second)
	detected := domain.MediaContentTypePNG
	repositoryFake := &mediaRepositoryFake{statusRecord: repository.MediaStatusRecord{
		Job: domain.MediaProcessingJob{
			ID: "job-1", OrgID: "org-1", AssetID: "asset-1", VersionID: "version-1",
			Status: domain.ProcessingJobCompleted, AttemptCount: 2,
			QueuedAt: now.Add(-time.Minute), StartedAt: &startedAt, CompletedAt: &completedAt,
		},
		Version: domain.AssetMediaVersion{
			ID: "version-1", OrgID: "org-1", AssetID: "asset-1", UploadID: "upload-1",
			Status: domain.MediaVersionCompleted, RawObjectKey: "raw/org-1/asset-1/upload-1/original.png",
			DeclaredContentType: domain.MediaContentTypePNG, DetectedContentType: &detected,
			OriginalSizeBytes: 2048, SHA256: bytes.Repeat([]byte{0x2a}, 32),
		},
		Session: domain.MediaUploadSession{ID: "upload-1", OriginalFilename: "photo.png"},
		Outputs: []domain.MediaOutput{
			{Kind: domain.MediaOutputThumbnail, ObjectKey: "processed/org-1/asset-1/version-1/thumbnail-256.png", ContentType: domain.MediaContentTypePNG, Width: 256, Height: 128, SizeBytes: 900},
			{Kind: domain.MediaOutputWeb, ObjectKey: "processed/org-1/asset-1/version-1/web-1080.png", ContentType: domain.MediaContentTypePNG, Width: 1080, Height: 540, SizeBytes: 1800},
		},
	}}
	storage := &mediaStorageFake{presignGetNow: now}
	service := usecase.NewMediaUsecase(repositoryFake, storage, mediaUsecaseClock{at: now}, &sequenceIDs{}, mediaPolicy())

	result, err := service.GetMediaStatus(context.Background(), repository.MediaStatusScope{OrgID: "org-1", AssetID: "asset-1"})
	if err != nil {
		t.Fatalf("GetMediaStatus: %v", err)
	}
	if repositoryFake.statusCalls != 1 || repositoryFake.statusScope.OrgID != "org-1" || repositoryFake.statusScope.AssetID != "asset-1" {
		t.Fatalf("status scope = %#v calls=%d", repositoryFake.statusScope, repositoryFake.statusCalls)
	}
	if result.Status != domain.ProcessingJobCompleted || result.AttemptCount != 2 || result.Outputs == nil {
		t.Fatalf("completed result = %#v", result)
	}
	if result.Outputs.Thumbnail.SizeBytes != 900 || result.Outputs.Web.SizeBytes != 1800 ||
		result.Outputs.Thumbnail.Width != 256 || result.Outputs.Web.Height != 540 {
		t.Fatalf("trusted outputs = %#v", result.Outputs)
	}
	if !result.Outputs.ExpiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("outputs expiry = %s, want %s", result.Outputs.ExpiresAt, now.Add(15*time.Minute))
	}
	if len(storage.presignGetCalls) != 2 {
		t.Fatalf("PresignGet calls = %#v, want exactly thumbnail and web", storage.presignGetCalls)
	}
	for _, call := range storage.presignGetCalls {
		if call.TTL != 15*time.Minute || call.Key.IsRaw() {
			t.Fatalf("unsafe PresignGet call = %#v", call)
		}
	}
}

func TestMediaStatus_MapsTerminalFailuresToStableSafeCategories(t *testing.T) {
	now := time.Date(2026, time.August, 19, 11, 0, 0, 0, time.UTC)
	failedAt := now.Add(-time.Second)
	for name, testCase := range map[string]struct {
		internalCode string
		wantCode     string
		wantMessage  string
	}{
		"invalid image": {
			internalCode: "INVALID_IMAGE",
			wantCode:     "INVALID_IMAGE",
			wantMessage:  "File is not a valid image",
		},
		"processor timeout": {
			internalCode: "MEDIA_PROCESSING_TIMEOUT",
			wantCode:     "PROCESSING_TIMEOUT",
			wantMessage:  "Media processing exceeded its time limit",
		},
		"notification isolation": {
			internalCode: "MEDIA_NOTIFICATION_ISOLATED",
			wantCode:     "MEDIA_PROCESSING_FAILED",
			wantMessage:  "Media processing failed",
		},
		"unknown internal detail": {
			internalCode: "parser /tmp/private/photo.png stack line 42",
			wantCode:     "MEDIA_PROCESSING_FAILED",
			wantMessage:  "Media processing failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			internalCode := testCase.internalCode
			repositoryFake := &mediaRepositoryFake{statusRecord: repository.MediaStatusRecord{
				Job: domain.MediaProcessingJob{
					ID: "job-1", AssetID: "asset-1", Status: domain.ProcessingJobFailed,
					AttemptCount: 3, LastErrorCode: &internalCode, QueuedAt: now.Add(-time.Minute), FailedAt: &failedAt,
				},
				Version: domain.AssetMediaVersion{
					UploadID: "upload-1", DeclaredContentType: domain.MediaContentTypeJPEG, OriginalSizeBytes: 7,
				},
				Session: domain.MediaUploadSession{OriginalFilename: "photo.jpg"},
			}}
			storage := &mediaStorageFake{presignGetNow: now}
			service := usecase.NewMediaUsecase(repositoryFake, storage, mediaUsecaseClock{at: now}, &sequenceIDs{}, mediaPolicy())

			result, err := service.GetMediaStatus(context.Background(), repository.MediaStatusScope{OrgID: "org-1", AssetID: "asset-1"})
			if err != nil {
				t.Fatalf("GetMediaStatus: %v", err)
			}
			if result.Error == nil || result.Error.Code != testCase.wantCode || result.Error.Message != testCase.wantMessage {
				t.Fatalf("safe error = %#v, want %s %q", result.Error, testCase.wantCode, testCase.wantMessage)
			}
			if result.Outputs != nil || len(storage.presignGetCalls) != 0 || result.FailedAt == nil || !result.FailedAt.Equal(failedAt) {
				t.Fatalf("failed result exposed outputs or lost timestamp: %#v calls=%v", result, storage.presignGetCalls)
			}
		})
	}
}

func TestMediaStatus_PreservesAuthoritativeQueuedAndProcessingStagesWithoutSigning(t *testing.T) {
	now := time.Date(2026, time.August, 19, 11, 0, 0, 0, time.UTC)
	validating := domain.ProcessingStageValidating
	transforming := domain.ProcessingStageTransforming
	for name, job := range map[string]domain.MediaProcessingJob{
		"queued": {
			ID: "queued-job", AssetID: "asset-1", Status: domain.ProcessingJobQueued,
			AttemptCount: 0, QueuedAt: now.Add(-time.Minute),
		},
		"validating": {
			ID: "validating-job", AssetID: "asset-1", Status: domain.ProcessingJobProcessing,
			AttemptCount: 1, Stage: &validating, QueuedAt: now.Add(-time.Minute), StartedAt: &now,
		},
		"transforming": {
			ID: "transforming-job", AssetID: "asset-1", Status: domain.ProcessingJobProcessing,
			AttemptCount: 2, Stage: &transforming, QueuedAt: now.Add(-time.Minute), StartedAt: &now,
		},
	} {
		t.Run(name, func(t *testing.T) {
			repositoryFake := &mediaRepositoryFake{statusRecord: repository.MediaStatusRecord{
				Job: job,
				Version: domain.AssetMediaVersion{
					UploadID: "upload-1", DeclaredContentType: domain.MediaContentTypePNG, OriginalSizeBytes: 7,
				},
				Session: domain.MediaUploadSession{OriginalFilename: "photo.png"},
			}}
			storage := &mediaStorageFake{presignGetNow: now}
			service := usecase.NewMediaUsecase(repositoryFake, storage, mediaUsecaseClock{at: now}, &sequenceIDs{}, mediaPolicy())

			result, err := service.GetMediaStatus(context.Background(), repository.MediaStatusScope{OrgID: "org-1", AssetID: "asset-1"})
			if err != nil {
				t.Fatalf("GetMediaStatus: %v", err)
			}
			if result.Status != job.Status || result.AttemptCount != job.AttemptCount || result.Stage != job.Stage {
				t.Fatalf("result = %#v, want job %#v", result, job)
			}
			if result.Outputs != nil || result.Error != nil || len(storage.presignGetCalls) != 0 {
				t.Fatalf("nonterminal status exposed terminal data: %#v calls=%v", result, storage.presignGetCalls)
			}
		})
	}
}

func TestMediaStatus_EachAuthorizedPollRenewsExpiredDerivativeLinks(t *testing.T) {
	now := time.Date(2026, time.August, 19, 11, 0, 0, 0, time.UTC)
	repositoryFake := &mediaRepositoryFake{statusRecord: repository.MediaStatusRecord{
		Job: domain.MediaProcessingJob{
			ID: "job-1", AssetID: "asset-1", Status: domain.ProcessingJobCompleted,
			AttemptCount: 1, QueuedAt: now.Add(-time.Minute), CompletedAt: &now,
		},
		Version: domain.AssetMediaVersion{
			UploadID: "upload-1", DeclaredContentType: domain.MediaContentTypePNG, OriginalSizeBytes: 7,
		},
		Session: domain.MediaUploadSession{OriginalFilename: "photo.png"},
		Outputs: []domain.MediaOutput{
			{Kind: domain.MediaOutputThumbnail, ObjectKey: "processed/org/asset/version/thumbnail.png", ContentType: domain.MediaContentTypePNG, Width: 64, Height: 64, SizeBytes: 100},
			{Kind: domain.MediaOutputWeb, ObjectKey: "processed/org/asset/version/web.png", ContentType: domain.MediaContentTypePNG, Width: 64, Height: 64, SizeBytes: 100},
		},
	}}
	clock := &mutableMediaStatusClock{at: now}
	storage := &mediaStorageFake{presignGetNow: now}
	service := usecase.NewMediaUsecase(repositoryFake, storage, clock, &sequenceIDs{}, mediaPolicy())

	first, err := service.GetMediaStatus(context.Background(), repository.MediaStatusScope{OrgID: "org-1", AssetID: "asset-1"})
	if err != nil {
		t.Fatalf("first GetMediaStatus: %v", err)
	}
	clock.at = now.Add(16 * time.Minute)
	storage.presignGetNow = clock.at
	second, err := service.GetMediaStatus(context.Background(), repository.MediaStatusScope{OrgID: "org-1", AssetID: "asset-1"})
	if err != nil {
		t.Fatalf("second GetMediaStatus: %v", err)
	}

	if len(storage.presignGetCalls) != 4 {
		t.Fatalf("PresignGet calls = %d, want two fresh links per poll", len(storage.presignGetCalls))
	}
	if first.Outputs == nil || second.Outputs == nil ||
		!first.Outputs.ExpiresAt.Equal(now.Add(15*time.Minute)) ||
		!second.Outputs.ExpiresAt.Equal(now.Add(31*time.Minute)) {
		t.Fatalf("link expiries = first %#v second %#v", first.Outputs, second.Outputs)
	}
	if first.Outputs.Thumbnail.URL == second.Outputs.Thumbnail.URL || first.Outputs.Web.URL == second.Outputs.Web.URL {
		t.Fatalf("expired URLs were reused: first=%#v second=%#v", first.Outputs, second.Outputs)
	}
}
