package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// mediaRepository owns every durable media table. Media state is only ever read
// or written through here, so the locking order below is the single place that
// decides how concurrent uploads serialize.
type mediaRepository struct {
	db    *gorm.DB
	clock domain.Clock
}

func NewMediaRepository(db *gorm.DB, clock domain.Clock) *mediaRepository {
	return &mediaRepository{db: db, clock: clock}
}

var (
	ErrQuotaExceeded        = errors.New("organization raw media quota exceeded")
	ErrUploadInProgress     = errors.New("asset already has an open upload or pending version")
	ErrUploadNotFound       = errors.New("upload session not found in scope")
	ErrIdempotencyConflict  = errors.New("idempotency key reused with different request data")
	ErrUploadStateConflict  = errors.New("operation is not valid for the current session state")
	ErrUploadSessionExpired = errors.New("upload session has expired")
	ErrAssetNotAvailable    = errors.New("asset is missing, deleted, or in another organization")
	ErrMediaObjectMismatch  = errors.New("stored media object does not match the upload session")
)

type CreateUploadSessionOptions struct {
	UploadID            string
	RawObjectKey        domain.ObjectKey
	DefaultQuotaBytes   int64
	CredentialExpiresAt time.Time
	SessionExpiresAt    time.Time
}

type CommitUploadIDs struct {
	VersionID string
	JobID     string
	OutboxID  string
}

// Lock order, applied by every media transaction that touches more than one of
// these rows: organization_media_usage, then metadata_items, then
// media_upload_sessions. Taking them in this order everywhere is what prevents
// a deadlock between a session creation and a concurrent commit.

// withTransaction runs the unit of work inside one transaction. Every media
// mutation goes through this so no caller can half-commit a version, job, and
// outbox row.
func (repository *mediaRepository) withTransaction(ctx context.Context, work func(tx *gorm.DB) error) error {
	return repository.db.WithContext(ctx).Transaction(work)
}

// lockOrganizationUsageRow locks the quota ledger, inserting it from the
// configured default when the organization has never uploaded. It is the first
// lock any admission transaction takes.
func lockOrganizationUsageRow(tx *gorm.DB, orgID string, defaultQuotaBytes int64) (domain.OrganizationMediaUsage, error) {
	usage := domain.OrganizationMediaUsage{
		OrgID:         orgID,
		RawQuotaBytes: defaultQuotaBytes,
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&usage).Error; err != nil {
		return domain.OrganizationMediaUsage{}, err
	}

	var locked domain.OrganizationMediaUsage
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("org_id = ?", orgID).
		Take(&locked).Error
	if err != nil {
		return domain.OrganizationMediaUsage{}, err
	}
	return locked, nil
}

// lockActiveAsset locks the metadata row and refuses a deleted, missing, or
// cross-organization asset. Every media operation resolves the asset this way,
// so lifecycle state is checked before any storage or quota work.
//
// metadata_items carries no org_id: tenancy lives on the containing folder, so
// the scope check is a join and the lock is narrowed to the metadata row with
// FOR UPDATE OF. Locking the folder too would serialize every asset that shares
// a folder against each other.
func lockActiveAsset(tx *gorm.DB, orgID, assetID string) (assetMediaPointers, error) {
	var pointers assetMediaPointers
	err := tx.Raw(`
		SELECT metadata_items.id,
		       folders.org_id,
		       metadata_items.folder_id,
		       metadata_items.active_media_version_id,
		       metadata_items.pending_media_version_id
		FROM metadata_items
		JOIN folders ON folders.id = metadata_items.folder_id
		WHERE metadata_items.id = ?
		  AND folders.org_id = ?
		  AND metadata_items.deleted_at IS NULL
		  AND folders.deleted_at IS NULL
		FOR UPDATE OF metadata_items`,
		assetID, orgID).Scan(&pointers).Error
	if err != nil {
		return assetMediaPointers{}, err
	}
	if pointers.ID == "" {
		return assetMediaPointers{}, ErrAssetNotAvailable
	}
	return pointers, nil
}

// assetMediaPointers is the media-relevant projection of metadata_items. The
// full asset row is deliberately not loaded: media transactions need only the
// two pointers and the tenant scope resolved from the containing folder.
type assetMediaPointers struct {
	ID                    string  `gorm:"column:id"`
	OrgID                 string  `gorm:"column:org_id"`
	FolderID              string  `gorm:"column:folder_id"`
	ActiveMediaVersionID  *string `gorm:"column:active_media_version_id"`
	PendingMediaVersionID *string `gorm:"column:pending_media_version_id"`
}

func (pointers assetMediaPointers) hasPendingVersion() bool {
	return pointers.PendingMediaVersionID != nil
}

// lockUploadSessionRow loads a session within its exact scope. A session that
// belongs to another organization, asset, or requester is reported as missing
// rather than forbidden, so probing cannot distinguish the cases.
func lockUploadSessionRow(tx *gorm.DB, scope UploadSessionScope) (domain.MediaUploadSession, error) {
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = ? AND org_id = ? AND asset_id = ?", scope.UploadID, scope.OrgID, scope.AssetID)
	if scope.RequestedBy != "" {
		query = query.Where("requested_by = ?", scope.RequestedBy)
	}

	var session domain.MediaUploadSession
	err := query.Take(&session).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.MediaUploadSession{}, ErrUploadNotFound
	}
	if err != nil {
		return domain.MediaUploadSession{}, err
	}
	return session, nil
}

// UploadSessionScope is the full tenant scope every session read requires.
// Passing it as one value keeps a caller from accidentally omitting the
// organization or asset and widening a lookup.
type UploadSessionScope struct {
	OrgID       string
	AssetID     string
	UploadID    string
	RequestedBy string
}

// reserveQuota increases the reservation if the invariant still holds. The
// check is done in SQL against the locked row so a concurrent admission cannot
// pass a stale read.
func reserveQuota(tx *gorm.DB, orgID string, bytes int64) error {
	result := tx.Exec(`
		UPDATE organization_media_usage
		SET reserved_raw_bytes = reserved_raw_bytes + ?
		WHERE org_id = ?
		  AND reserved_raw_bytes + stored_raw_bytes + ? <= raw_quota_bytes`,
		bytes, orgID, bytes)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrQuotaExceeded
	}
	return nil
}

// releaseReservation is the single conditional decrement shared by expiration
// and caller cancellation. It clamps at zero so a retried cleanup can never
// drive the ledger negative.
func releaseReservation(tx *gorm.DB, orgID string, bytes int64) error {
	return tx.Exec(`
		UPDATE organization_media_usage
		SET reserved_raw_bytes = GREATEST(reserved_raw_bytes - ?, 0)
		WHERE org_id = ?`,
		bytes, orgID).Error
}

// convertReservationToStored is the commit transition: the reservation becomes
// occupied bytes in one statement so no window exists where the organization
// appears to have freed space it is still using.
func convertReservationToStored(tx *gorm.DB, orgID string, reservedBytes, actualBytes int64) error {
	return tx.Exec(`
		UPDATE organization_media_usage
		SET reserved_raw_bytes = GREATEST(reserved_raw_bytes - ?, 0),
		    stored_raw_bytes = stored_raw_bytes + ?
		WHERE org_id = ?`,
		reservedBytes, actualBytes, orgID).Error
}

// releaseStoredBytes is used only by cleanup of a failed or invalid raw object.
// A completed version's bytes are never decremented in this phase.
func releaseStoredBytes(tx *gorm.DB, orgID string, bytes int64) error {
	return tx.Exec(`
		UPDATE organization_media_usage
		SET stored_raw_bytes = GREATEST(stored_raw_bytes - ?, 0)
		WHERE org_id = ?`,
		bytes, orgID).Error
}

// setPendingMediaVersion attaches a candidate to the asset. The partial unique
// index enforces that one pending version belongs to one asset; this additional
// scope check catches a cross-asset write before the constraint does.
func setPendingMediaVersion(tx *gorm.DB, orgID, assetID, versionID string) error {
	result := tx.Exec(`
		UPDATE metadata_items
		SET pending_media_version_id = ?
		FROM folders
		WHERE metadata_items.id = ?
		  AND folders.id = metadata_items.folder_id
		  AND folders.org_id = ?
		  AND metadata_items.deleted_at IS NULL
		  AND metadata_items.pending_media_version_id IS NULL`,
		versionID, assetID, orgID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrUploadInProgress
	}
	return nil
}

// clearPendingMediaVersion clears only the matching pointer, so a late failure
// from a superseded candidate cannot detach the current one.
func clearPendingMediaVersion(tx *gorm.DB, assetID, versionID string) error {
	return tx.Exec(`
		UPDATE metadata_items
		SET pending_media_version_id = NULL
		WHERE id = ? AND pending_media_version_id = ?`,
		assetID, versionID).Error
}

// sessionIsCommittable reports whether a loaded session may still be committed,
// separating "expired" from "wrong state" so callers can map them to their own
// safe error codes.
func sessionIsCommittable(session domain.MediaUploadSession, now time.Time) error {
	if session.State == domain.UploadSessionCommitted {
		return nil
	}
	if session.State != domain.UploadSessionCreated {
		return ErrUploadStateConflict
	}
	if !now.Before(session.SessionExpiresAt) {
		return ErrUploadSessionExpired
	}
	return nil
}

func (repository *mediaRepository) now() time.Time {
	return repository.clock.Now()
}

// CreateUploadSession reserves organization quota in the same transaction as
// the checksum-bound session. A retry is resolved before reservation so replay
// cannot double-charge quota and a conflicting retry cannot mutate the first
// session.
func (repository *mediaRepository) CreateUploadSession(
	ctx context.Context,
	request domain.CreateUploadSessionRequest,
	options CreateUploadSessionOptions,
) (domain.MediaUploadSession, bool, error) {
	fingerprint := uploadRequestFingerprint(request)
	extension, ok := request.DeclaredContentType.FileExtension()
	if !ok {
		return domain.MediaUploadSession{}, false, domain.ErrUnsupportedMediaType
	}
	if options.DefaultQuotaBytes <= 0 {
		return domain.MediaUploadSession{}, false, errors.New("default media quota must be positive")
	}
	var result domain.MediaUploadSession
	replayed := false
	err := repository.withTransaction(ctx, func(tx *gorm.DB) error {
		if _, err := lockOrganizationUsageRow(tx, request.OrgID, options.DefaultQuotaBytes); err != nil {
			return err
		}
		asset, err := lockActiveAsset(tx, request.OrgID, request.AssetID)
		if err != nil {
			return err
		}

		var existing domain.MediaUploadSession
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ? AND asset_id = ? AND idempotency_key = ?", request.OrgID, request.AssetID, request.IdempotencyKey).
			Take(&existing).Error
		if err == nil {
			if subtle.ConstantTimeCompare(existing.RequestFingerprint, fingerprint) != 1 {
				return ErrIdempotencyConflict
			}
			// A cancelled, expired, or failed session released its quota
			// reservation. Replaying it would hand back upload authority for a
			// raw key nothing reserves, so the key is spent: the caller starts
			// over under a new one.
			if !existing.State.IsPublished() {
				return ErrIdempotencyConflict
			}
			result = existing
			replayed = true
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if asset.hasPendingVersion() {
			return ErrUploadInProgress
		}
		var openSessionCount int64
		if err := tx.Model(&domain.MediaUploadSession{}).
			Where("asset_id = ? AND state = ?", request.AssetID, domain.UploadSessionCreated).
			Count(&openSessionCount).Error; err != nil {
			return err
		}
		if openSessionCount != 0 {
			return ErrUploadInProgress
		}
		if err := reserveQuota(tx, request.OrgID, request.ExpectedSizeBytes); err != nil {
			return err
		}
		now := repository.now()
		result = domain.MediaUploadSession{
			ID:                     options.UploadID,
			OrgID:                  request.OrgID,
			AssetID:                request.AssetID,
			RequestedBy:            request.RequestedBy,
			IdempotencyKey:         request.IdempotencyKey,
			RequestFingerprint:     fingerprint,
			State:                  domain.UploadSessionCreated,
			OriginalFilename:       request.OriginalFilename,
			DeclaredContentType:    request.DeclaredContentType,
			FileExtension:          extension,
			ExpectedSizeBytes:      request.ExpectedSizeBytes,
			DeclaredChecksumSHA256: append([]byte(nil), request.DeclaredChecksumSHA256...),
			RawObjectKey:           options.RawObjectKey.String(),
			CredentialExpiresAt:    options.CredentialExpiresAt,
			SessionExpiresAt:       options.SessionExpiresAt,
			CreatedAt:              now,
			UpdatedAt:              now,
		}
		if err := tx.Create(&result).Error; err != nil {
			if isUniqueViolation(err) {
				return ErrUploadInProgress
			}
			return err
		}
		return nil
	})
	return result, replayed, err
}

func uploadRequestFingerprint(request domain.CreateUploadSessionRequest) []byte {
	canonical := strings.Join([]string{
		request.AssetID,
		request.OriginalFilename,
		string(request.DeclaredContentType),
		strconv.FormatInt(request.ExpectedSizeBytes, 10),
		base64.StdEncoding.EncodeToString(request.DeclaredChecksumSHA256),
	}, "|")
	digest := sha256.Sum256([]byte(canonical))
	return digest[:]
}

func isUniqueViolation(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "duplicate key") || strings.Contains(err.Error(), "SQLSTATE 23505")
}

func (repository *mediaRepository) GetUploadSession(ctx context.Context, scope UploadSessionScope) (domain.MediaUploadSession, error) {
	var session domain.MediaUploadSession
	err := repository.withTransaction(ctx, func(tx *gorm.DB) error {
		if _, err := lockActiveAsset(tx, scope.OrgID, scope.AssetID); err != nil {
			return err
		}
		locked, err := lockUploadSessionRow(tx, scope)
		if err != nil {
			return err
		}
		session = locked
		return nil
	})
	if err != nil {
		return domain.MediaUploadSession{}, err
	}
	if session.State == domain.UploadSessionCreated && !repository.now().Before(session.SessionExpiresAt) {
		return domain.MediaUploadSession{}, ErrUploadSessionExpired
	}
	return session, nil
}

func (repository *mediaRepository) RefreshUploadSession(
	ctx context.Context,
	scope UploadSessionScope,
	credentialExpiresAt time.Time,
) (domain.MediaUploadSession, error) {
	var refreshed domain.MediaUploadSession
	err := repository.withTransaction(ctx, func(tx *gorm.DB) error {
		if _, err := lockActiveAsset(tx, scope.OrgID, scope.AssetID); err != nil {
			return err
		}
		session, err := lockUploadSessionRow(tx, scope)
		if err != nil {
			return err
		}
		if session.State != domain.UploadSessionCreated {
			return ErrUploadStateConflict
		}
		now := repository.now()
		if !now.Before(session.SessionExpiresAt) {
			return ErrUploadSessionExpired
		}
		if !credentialExpiresAt.After(now) || credentialExpiresAt.After(session.SessionExpiresAt) {
			return ErrUploadSessionExpired
		}
		result := tx.Model(&domain.MediaUploadSession{}).
			Where("id = ? AND state = ?", session.ID, domain.UploadSessionCreated).
			Updates(map[string]any{"credential_expires_at": credentialExpiresAt, "updated_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUploadStateConflict
		}
		session.CredentialExpiresAt = credentialExpiresAt
		session.UpdatedAt = now
		refreshed = session
		return nil
	})
	return refreshed, err
}

func (repository *mediaRepository) CancelUploadSession(ctx context.Context, scope UploadSessionScope) error {
	return repository.transitionOpenUploadSession(ctx, scope, domain.UploadSessionCancelled)
}

func (repository *mediaRepository) ExpireUploadSession(ctx context.Context, scope UploadSessionScope) error {
	return repository.transitionOpenUploadSession(ctx, scope, domain.UploadSessionExpired)
}

func (repository *mediaRepository) transitionOpenUploadSession(
	ctx context.Context,
	scope UploadSessionScope,
	target domain.UploadSessionState,
) error {
	if target != domain.UploadSessionCancelled && target != domain.UploadSessionExpired {
		return ErrUploadStateConflict
	}
	return repository.withTransaction(ctx, func(tx *gorm.DB) error {
		var usage domain.OrganizationMediaUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("org_id = ?", scope.OrgID).Take(&usage).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrUploadNotFound
		} else if err != nil {
			return err
		}
		session, err := lockUploadSessionRow(tx, scope)
		if err != nil {
			return err
		}

		switch session.State {
		case domain.UploadSessionCancelled, domain.UploadSessionExpired, domain.UploadSessionFailed:
			return nil
		case domain.UploadSessionCommitted:
			if target == domain.UploadSessionCancelled {
				return ErrUploadStateConflict
			}
			return nil
		case domain.UploadSessionCreated:
		default:
			return ErrUploadStateConflict
		}

		now := repository.now()
		if target == domain.UploadSessionExpired && now.Before(session.SessionExpiresAt) {
			return nil
		}
		if err := releaseReservation(tx, session.OrgID, session.ExpectedSizeBytes); err != nil {
			return err
		}
		updates := map[string]any{"state": target, "updated_at": now}
		if target == domain.UploadSessionCancelled {
			updates["cancelled_at"] = now
		} else {
			updates["expired_at"] = now
		}
		result := tx.Model(&domain.MediaUploadSession{}).
			Where("id = ? AND state = ?", session.ID, domain.UploadSessionCreated).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUploadStateConflict
		}
		return nil
	})
}

func verifyStoredObject(session domain.MediaUploadSession, attributes domain.ObjectAttributes) error {
	if attributes.SizeBytes != session.ExpectedSizeBytes || attributes.ContentType != string(session.DeclaredContentType) {
		return ErrMediaObjectMismatch
	}
	if len(attributes.ChecksumSHA256) != 0 && !bytes.Equal(attributes.ChecksumSHA256, session.DeclaredChecksumSHA256) {
		return ErrMediaObjectMismatch
	}
	return nil
}

func (repository *mediaRepository) CommitUpload(
	ctx context.Context,
	request domain.CommitUploadRequest,
	attributes domain.ObjectAttributes,
	ids CommitUploadIDs,
) (domain.CommitAcceptance, bool, error) {
	scope := UploadSessionScope{
		OrgID: request.OrgID, AssetID: request.AssetID, UploadID: request.UploadID, RequestedBy: request.RequestedBy,
	}

	session, err := repository.GetUploadSession(ctx, scope)
	if err != nil {
		return domain.CommitAcceptance{}, false, err
	}
	if session.State != domain.UploadSessionCommitted {
		if err := verifyStoredObject(session, attributes); err != nil {
			return domain.CommitAcceptance{}, false, err
		}
	}

	var acceptance domain.CommitAcceptance
	replayed := false
	err = repository.withTransaction(ctx, func(tx *gorm.DB) error {
		var usage domain.OrganizationMediaUsage
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ?", request.OrgID).Take(&usage).Error; err != nil {
			return err
		}
		asset, err := lockActiveAsset(tx, request.OrgID, request.AssetID)
		if err != nil {
			return err
		}
		locked, err := lockUploadSessionRow(tx, scope)
		if err != nil {
			return err
		}
		if locked.State == domain.UploadSessionCommitted {
			var version domain.AssetMediaVersion
			if err := tx.Where("upload_id = ?", locked.ID).Take(&version).Error; err != nil {
				return err
			}
			var job domain.MediaProcessingJob
			if err := tx.Where("version_id = ?", version.ID).Take(&job).Error; err != nil {
				return err
			}
			acceptance = domain.CommitAcceptance{VersionID: version.ID, JobID: job.ID, Status: job.Status, AcceptedAt: version.CreatedAt}
			replayed = true
			return nil
		}
		if err := sessionIsCommittable(locked, repository.now()); err != nil {
			return err
		}
		if err := verifyStoredObject(locked, attributes); err != nil {
			return err
		}
		if asset.hasPendingVersion() {
			return ErrUploadInProgress
		}

		now := repository.now()
		version := domain.AssetMediaVersion{
			ID: ids.VersionID, OrgID: locked.OrgID, AssetID: locked.AssetID, UploadID: locked.ID,
			RequestedBy: locked.RequestedBy, Status: domain.MediaVersionPending,
			RawObjectKey: locked.RawObjectKey, RawRetained: true,
			DeclaredContentType: locked.DeclaredContentType, OriginalSizeBytes: attributes.SizeBytes,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		job := domain.MediaProcessingJob{
			ID: ids.JobID, OrgID: locked.OrgID, AssetID: locked.AssetID, VersionID: version.ID,
			Status: domain.ProcessingJobQueued, AttemptCount: 0, NextAttemptAt: now, QueuedAt: now,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.Create(&job).Error; err != nil {
			return err
		}
		if err := setPendingMediaVersion(tx, locked.OrgID, locked.AssetID, version.ID); err != nil {
			return err
		}
		if err := convertReservationToStored(tx, locked.OrgID, locked.ExpectedSizeBytes, attributes.SizeBytes); err != nil {
			return err
		}
		if err := tx.Model(&domain.MediaUploadSession{}).Where("id = ? AND state = ?", locked.ID, domain.UploadSessionCreated).
			Updates(map[string]any{"state": domain.UploadSessionCommitted, "committed_at": now, "updated_at": now}).Error; err != nil {
			return err
		}
		envelope := map[string]any{
			"eventId": ids.OutboxID, "eventType": domain.MediaProcessingRequestedEventType,
			"schemaVersion": 1, "source": "asset-core", "occurredAt": now,
			"orgId": locked.OrgID, "assetId": locked.AssetID, "uploadId": locked.ID,
			"versionId": version.ID, "jobId": job.ID,
		}
		if traceparent := event.Traceparent(ctx); traceparent != "" {
			envelope["traceparent"] = traceparent
		}
		payload, err := json.Marshal(envelope)
		if err != nil {
			return err
		}
		if err := insertOutboxJSON(tx, domain.MediaJobOutboxRecord{
			ID: ids.OutboxID, JobID: job.ID, EventType: domain.MediaProcessingRequestedEventType,
			SchemaVersion: 1, Payload: payload, Status: "pending", NextAttemptAt: now,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		acceptance = domain.CommitAcceptance{VersionID: version.ID, JobID: job.ID, Status: job.Status, AcceptedAt: now}
		return nil
	})
	return acceptance, replayed, err
}

func insertOutboxJSON(tx *gorm.DB, record domain.MediaJobOutboxRecord) error {
	if record.EventType == "" {
		return fmt.Errorf("outbox record for job %s has no event type", record.JobID)
	}
	return tx.Exec(`
		INSERT INTO media_job_outbox
			(id, job_id, event_type, schema_version, payload, status, attempt_count, next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?::jsonb, ?, 0, ?, ?, ?)`,
		record.ID, record.JobID, record.EventType, record.SchemaVersion, string(record.Payload), record.Status,
		record.NextAttemptAt, record.CreatedAt, record.UpdatedAt,
	).Error
}
