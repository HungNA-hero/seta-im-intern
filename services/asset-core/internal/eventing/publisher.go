package eventing

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	assetEventsStream = "stream:asset-events"
	publishTimeout    = 200 * time.Millisecond
)

// Envelope is the compact Redis Stream event shape shared with access-core's
// cache-invalidator consumer (see specs/003-cando-cache/contracts/invalidation-events.md).
// Payloads never carry subtree id lists — a subtree can hold ~1M items.
type Envelope struct {
	EventID       string          `json:"eventId"`
	EventType     string          `json:"eventType"`
	SchemaVersion int             `json:"schemaVersion"`
	Source        string          `json:"source"`
	OccurredAt    time.Time       `json:"occurredAt"`
	AggregateType string          `json:"aggregateType"`
	AggregateID   string          `json:"aggregateId"`
	OrgID         string          `json:"orgId"`
	Data          json.RawMessage `json:"data"`
}

// FolderMovedData is the `folder.moved` event payload.
type FolderMovedData struct {
	FolderID string `json:"folderId"`
	OldPath  string `json:"oldPath"`
	NewPath  string `json:"newPath"`
}

// FolderDeletedData is the `folder.deleted` event payload. JobID is present
// when the event originates from the async job's root tombstone transition,
// not from a synchronous single-folder delete.
type FolderDeletedData struct {
	FolderID string `json:"folderId"`
	RootPath string `json:"rootPath"`
	JobID    string `json:"jobId,omitempty"`
}

// MetadataLifecycleData is the compact payload for metadata visibility changes.
type MetadataLifecycleData struct {
	MetadataID string `json:"metadataId"`
}

func newEnvelope(orgID, aggregateType, aggregateID, eventType string, data any) (Envelope, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		EventID:       uuid.NewString(),
		EventType:     eventType,
		SchemaVersion: 1,
		Source:        "asset-core",
		OccurredAt:    time.Now().UTC(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		OrgID:         orgID,
		Data:          payload,
	}, nil
}

// PublishFolderMoved directly XADDs a `folder.moved` event after MoveFolder's
// commit. Best-effort: a failure here does not roll back the commit and is
// bounded by the receiving cache's hard TTL, not retried.
func PublishFolderMoved(ctx context.Context, orgID, folderID, oldPath, newPath string) {
	envelope, err := newEnvelope(orgID, "folder", folderID, "folder.moved", FolderMovedData{
		FolderID: folderID,
		OldPath:  oldPath,
		NewPath:  newPath,
	})
	if err != nil {
		slog.Default().Error("failed to build folder.moved event", "error", err, "folderId", folderID)
		return
	}
	publish(ctx, envelope)
}

// PublishFolderDeleted directly XADDs a `folder.deleted` event after either a
// synchronous folder delete commits or an async job tombstones its root.
// jobID is empty for the synchronous path.
func PublishFolderDeleted(ctx context.Context, orgID, folderID, rootPath, jobID string) {
	envelope, err := newEnvelope(orgID, "folder", folderID, "folder.deleted", FolderDeletedData{
		FolderID: folderID,
		RootPath: rootPath,
		JobID:    jobID,
	})
	if err != nil {
		slog.Default().Error("failed to build folder.deleted event", "error", err, "folderId", folderID)
		return
	}
	publish(ctx, envelope)
}

// PublishFolderRestored directly XADDs a visibility-change event after a
// parent-first restore commits.
func PublishFolderRestored(ctx context.Context, orgID, folderID, rootPath string) {
	envelope, err := newEnvelope(orgID, "folder", folderID, "folder.restored", FolderDeletedData{
		FolderID: folderID,
		RootPath: rootPath,
	})
	if err != nil {
		slog.Default().Error("failed to build folder.restored event", "error", err, "folderId", folderID)
		return
	}
	publish(ctx, envelope)
}

func publishMetadataLifecycle(ctx context.Context, orgID, metadataID, eventType string) {
	envelope, err := newEnvelope(orgID, "metadata_item", metadataID, eventType, MetadataLifecycleData{
		MetadataID: metadataID,
	})
	if err != nil {
		slog.Default().Error("failed to build metadata lifecycle event", "error", err, "eventType", eventType, "metadataId", metadataID)
		return
	}
	publish(ctx, envelope)
}

// PublishMetadataDeleted emits a cache-invalidation event after a metadata tombstone commits.
func PublishMetadataDeleted(ctx context.Context, orgID, metadataID string) {
	publishMetadataLifecycle(ctx, orgID, metadataID, "metadata.deleted")
}

// PublishMetadataRestored emits a cache-invalidation event after a metadata restore commits.
func PublishMetadataRestored(ctx context.Context, orgID, metadataID string) {
	publishMetadataLifecycle(ctx, orgID, metadataID, "metadata.restored")
}

// publish uses a fresh background context with its own short timeout rather
// than the caller's request context, so a post-commit publish is not cut
// short by the caller's request already finishing.
func publish(_ context.Context, envelope Envelope) {
	payload, err := json.Marshal(envelope)
	if err != nil {
		slog.Default().Error("failed to marshal event envelope", "error", err, "eventType", envelope.EventType, "eventId", envelope.EventID)
		return
	}

	publishCtx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()

	_, err = RedisClient().XAdd(publishCtx, &redis.XAddArgs{
		Stream: assetEventsStream,
		Values: map[string]any{"payload": string(payload)},
	}).Result()
	if err != nil {
		recordLostPublish()
		slog.Default().Error(
			"failed to publish lifecycle event; bounded by cache TTL backstop",
			"error", err,
			"eventType", envelope.EventType,
			"eventId", envelope.EventID,
			"orgId", envelope.OrgID,
		)
		return
	}
	recordPublishSuccess()
}
