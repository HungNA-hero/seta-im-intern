package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

var (
	ErrInvalidMediaFilename = errors.New("invalid media filename")
	ErrInvalidMediaSize     = errors.New("invalid media size")
	ErrInvalidMediaChecksum = errors.New("invalid media SHA-256")
)

type MediaRepository interface {
	CreateUploadSession(context.Context, domain.CreateUploadSessionRequest, repository.CreateUploadSessionOptions) (domain.MediaUploadSession, bool, error)
	GetUploadSession(context.Context, repository.UploadSessionScope) (domain.MediaUploadSession, error)
	RefreshUploadSession(context.Context, repository.UploadSessionScope, time.Time) (domain.MediaUploadSession, error)
	CancelUploadSession(context.Context, repository.UploadSessionScope) error
	CommitUpload(context.Context, domain.CommitUploadRequest, domain.ObjectAttributes, repository.CommitUploadIDs) (domain.CommitAcceptance, bool, error)
}

type MediaUsecase struct {
	repository MediaRepository
	storage    domain.ObjectStorage
	clock      domain.Clock
	ids        domain.IDGenerator
	config     domain.MediaConfig
}

func NewMediaUsecase(repository MediaRepository, storage domain.ObjectStorage, clock domain.Clock, ids domain.IDGenerator, config domain.MediaConfig) *MediaUsecase {
	return &MediaUsecase{repository: repository, storage: storage, clock: clock, ids: ids, config: config}
}

func (usecase *MediaUsecase) CreateUploadSession(ctx context.Context, request domain.CreateUploadSessionRequest) (domain.UploadSessionResult, error) {
	if err := usecase.validateAdmission(request); err != nil {
		return domain.UploadSessionResult{}, err
	}
	uploadID := usecase.ids.NewID()
	key, err := domain.RawObjectKey(request.OrgID, request.AssetID, uploadID, request.DeclaredContentType)
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	// PostgreSQL persists timestamptz at microsecond precision. Normalize before
	// the first response so a later refresh returns byte-for-byte identical
	// session timestamps rather than the same instant with fewer fractional
	// digits.
	now := usecase.clock.Now().UTC().Truncate(time.Microsecond)
	session, replayed, err := usecase.repository.CreateUploadSession(ctx, request, repository.CreateUploadSessionOptions{
		UploadID:            uploadID,
		RawObjectKey:        key,
		DefaultQuotaBytes:   usecase.config.Limits.DefaultRawQuotaBytes,
		CredentialExpiresAt: now.Add(usecase.config.Limits.CredentialTTL),
		SessionExpiresAt:    now.Add(usecase.config.Limits.SessionTTL),
	})
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	result := domain.UploadSessionResult{Session: session, Replayed: replayed}
	// A replay can land on an already committed session, or on one whose
	// credential lapsed while the caller was retrying. Neither takes new upload
	// authority, and both are still reportable sessions.
	if session.State == domain.UploadSessionCreated && usecase.credentialIsSignable(session) {
		result.Descriptor, err = usecase.presignSession(ctx, session)
		if err != nil {
			return domain.UploadSessionResult{}, err
		}
	}
	return result, nil
}

func (usecase *MediaUsecase) GetUploadSession(ctx context.Context, scope repository.UploadSessionScope) (domain.UploadSessionResult, error) {
	session, err := usecase.repository.GetUploadSession(ctx, scope)
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	// A cancelled, expired, or failed session has no contract representation, so
	// it reads as gone rather than as a state the client cannot interpret.
	if !session.State.IsPublished() {
		return domain.UploadSessionResult{}, repository.ErrUploadNotFound
	}
	result := domain.UploadSessionResult{Session: session}
	// A lapsed credential is not a gone session: the client refreshes it. Reporting
	// expiry here would leave a caller holding the asset's one open-session slot
	// with no way to use or replace it.
	if session.State == domain.UploadSessionCreated && usecase.credentialIsSignable(session) {
		result.Descriptor, err = usecase.presignSession(ctx, session)
		if err != nil {
			return domain.UploadSessionResult{}, err
		}
	}
	return result, nil
}

// RefreshUploadSession issues another whole-object authority for the same
// immutable session identity. HEAD distinguishes an interrupted PUT (no object,
// safe to retransmit the complete file) from a lost successful response (the
// exact object exists, so the caller should commit instead of overwriting it).
func (usecase *MediaUsecase) RefreshUploadSession(ctx context.Context, scope repository.UploadSessionScope) (domain.UploadSessionResult, error) {
	session, err := usecase.repository.GetUploadSession(ctx, scope)
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	if session.State != domain.UploadSessionCreated {
		return domain.UploadSessionResult{}, repository.ErrUploadStateConflict
	}

	attributes, headErr := usecase.storage.Head(ctx, domain.ObjectKey(session.RawObjectKey))
	switch {
	case headErr == nil:
		if _, err := usecase.verifyStoredObject(ctx, session, attributes); err != nil {
			return domain.UploadSessionResult{}, err
		}
		return domain.UploadSessionResult{}, repository.ErrUploadStateConflict
	case !errors.Is(headErr, domain.ErrObjectNotFound):
		return domain.UploadSessionResult{}, headErr
	}

	credentialExpiresAt := usecase.clock.Now().UTC().Truncate(time.Microsecond).Add(usecase.config.Limits.CredentialTTL)
	if credentialExpiresAt.After(session.SessionExpiresAt) {
		credentialExpiresAt = session.SessionExpiresAt
	}
	refreshed, err := usecase.repository.RefreshUploadSession(ctx, scope, credentialExpiresAt)
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	descriptor, err := usecase.presignSession(ctx, refreshed)
	if err != nil {
		return domain.UploadSessionResult{}, err
	}
	return domain.UploadSessionResult{Session: refreshed, Descriptor: descriptor}, nil
}

// CancelUploadSession is deliberately storage-free: quota and session state
// commit synchronously, while any exact-key abandoned object remains for the
// bounded cleanup loop introduced by T097.
func (usecase *MediaUsecase) CancelUploadSession(ctx context.Context, scope repository.UploadSessionScope) error {
	return usecase.repository.CancelUploadSession(ctx, scope)
}

// credentialIsSignable reports whether enough credential lifetime remains to
// sign a usable URL. Presigned expiry has whole-second resolution, so a
// sub-second remainder cannot be signed at all.
func (usecase *MediaUsecase) credentialIsSignable(session domain.MediaUploadSession) bool {
	return session.CredentialExpiresAt.Sub(usecase.clock.Now()) >= time.Second
}

func (usecase *MediaUsecase) presignSession(ctx context.Context, session domain.MediaUploadSession) (domain.UploadDescriptor, error) {
	ttl := session.CredentialExpiresAt.Sub(usecase.clock.Now())
	if ttl < time.Second {
		return domain.UploadDescriptor{}, repository.ErrUploadSessionExpired
	}
	return usecase.storage.PresignPut(ctx, domain.PutAuthority{
		Key:               domain.ObjectKey(session.RawObjectKey),
		ContentType:       session.DeclaredContentType,
		ExpectedSizeBytes: session.ExpectedSizeBytes,
		ChecksumSHA256:    append([]byte(nil), session.DeclaredChecksumSHA256...),
		ExpiresIn:         ttl,
	})
}

func (usecase *MediaUsecase) CommitUpload(ctx context.Context, request domain.CommitUploadRequest) (domain.CommitUploadResult, error) {
	scope := repository.UploadSessionScope{
		OrgID: request.OrgID, AssetID: request.AssetID, UploadID: request.UploadID, RequestedBy: request.RequestedBy,
	}
	session, err := usecase.repository.GetUploadSession(ctx, scope)
	if err != nil {
		return domain.CommitUploadResult{}, err
	}
	var attributes domain.ObjectAttributes
	var ids repository.CommitUploadIDs
	if session.State != domain.UploadSessionCommitted {
		attributes, err = usecase.storage.Head(ctx, domain.ObjectKey(session.RawObjectKey))
		if err != nil {
			return domain.CommitUploadResult{}, err
		}
		attributes, err = usecase.verifyStoredObject(ctx, session, attributes)
		if err != nil {
			return domain.CommitUploadResult{}, err
		}
		ids = repository.CommitUploadIDs{
			VersionID: usecase.ids.NewID(),
			JobID:     usecase.ids.NewID(),
			OutboxID:  usecase.ids.NewID(),
		}
	}
	acceptance, replayed, err := usecase.repository.CommitUpload(ctx, request, attributes, ids)
	if err != nil {
		return domain.CommitUploadResult{}, err
	}
	return domain.CommitUploadResult{
		AssetID: request.AssetID, UploadID: request.UploadID, VersionID: acceptance.VersionID,
		JobID: acceptance.JobID, Status: acceptance.Status,
		OriginalFilename: session.OriginalFilename, OriginalContentType: session.DeclaredContentType,
		OriginalSizeBytes: session.ExpectedSizeBytes, AcceptedAt: acceptance.AcceptedAt, Replayed: replayed,
	}, nil
}

func (usecase *MediaUsecase) verifyStoredObject(
	ctx context.Context,
	session domain.MediaUploadSession,
	attributes domain.ObjectAttributes,
) (domain.ObjectAttributes, error) {
	if attributes.SizeBytes != session.ExpectedSizeBytes ||
		attributes.ContentType != string(session.DeclaredContentType) {
		return domain.ObjectAttributes{}, repository.ErrMediaObjectMismatch
	}
	if len(attributes.ChecksumSHA256) != 0 {
		if !bytes.Equal(attributes.ChecksumSHA256, session.DeclaredChecksumSHA256) {
			return domain.ObjectAttributes{}, repository.ErrMediaObjectMismatch
		}
		return attributes, nil
	}

	body, err := usecase.storage.Get(ctx, domain.ObjectKey(session.RawObjectKey))
	if err != nil {
		return domain.ObjectAttributes{}, err
	}
	hasher := sha256.New()
	count, readErr := io.Copy(
		hasher,
		io.LimitReader(body, session.ExpectedSizeBytes+1),
	)
	closeErr := body.Close()
	if readErr != nil {
		return domain.ObjectAttributes{}, readErr
	}
	if closeErr != nil {
		return domain.ObjectAttributes{}, closeErr
	}
	computedChecksum := hasher.Sum(nil)
	if count != session.ExpectedSizeBytes ||
		!bytes.Equal(computedChecksum, session.DeclaredChecksumSHA256) {
		return domain.ObjectAttributes{}, repository.ErrMediaObjectMismatch
	}
	attributes.ChecksumSHA256 = computedChecksum
	return attributes, nil
}

func (usecase *MediaUsecase) validateAdmission(request domain.CreateUploadSessionRequest) error {
	if !usecase.config.Limits.AdmitsUploadSize(request.ExpectedSizeBytes) {
		return ErrInvalidMediaSize
	}
	if len(request.DeclaredChecksumSHA256) != domain.ChecksumByteLength {
		return ErrInvalidMediaChecksum
	}
	extension, ok := request.DeclaredContentType.FileExtension()
	if !ok {
		return domain.ErrUnsupportedMediaType
	}
	filename := strings.TrimSpace(request.OriginalFilename)
	if filename == "" || len(filename) > 255 || strings.ContainsAny(filename, "/\\\x00\r\n") {
		return ErrInvalidMediaFilename
	}
	actualExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(filename)), ".")
	if request.DeclaredContentType == domain.MediaContentTypeJPEG && actualExtension == "jpeg" {
		return nil
	}
	if actualExtension != extension {
		return domain.ErrUnsupportedMediaType
	}
	return nil
}
