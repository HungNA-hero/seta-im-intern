package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/observability"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/requestcontext"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type MediaUsecase interface {
	CreateUploadSession(context.Context, domain.CreateUploadSessionRequest) (domain.UploadSessionResult, error)
	GetUploadSession(context.Context, repository.UploadSessionScope) (domain.UploadSessionResult, error)
	RefreshUploadSession(context.Context, repository.UploadSessionScope) (domain.UploadSessionResult, error)
	CancelUploadSession(context.Context, repository.UploadSessionScope) error
	CommitUpload(context.Context, domain.CommitUploadRequest) (domain.CommitUploadResult, error)
	GetMediaStatus(context.Context, repository.MediaStatusScope) (domain.MediaStatusResult, error)
}

type MediaHandler struct{ usecase MediaUsecase }

func NewMediaHandler(mux *http.ServeMux, mediaUsecase MediaUsecase) {
	handler := &MediaHandler{usecase: mediaUsecase}
	mux.HandleFunc("POST /internal/api/v1/metadata-items/{assetId}/media/uploads", RequireActor(handler.createUploadSession))
	mux.HandleFunc("GET /internal/api/v1/metadata-items/{assetId}/media/uploads/{uploadId}", RequireActor(handler.getUploadSession))
	mux.HandleFunc("POST /internal/api/v1/metadata-items/{assetId}/media/uploads/{uploadId}/refresh", RequireActor(handler.refreshUploadSession))
	mux.HandleFunc("DELETE /internal/api/v1/metadata-items/{assetId}/media/uploads/{uploadId}", RequireActor(handler.cancelUploadSession))
	mux.HandleFunc("PUT /internal/api/v1/metadata-items/{assetId}/media", RequireActor(handler.commitUpload))
	mux.HandleFunc("GET /internal/api/v1/metadata-items/{assetId}/media/status", RequireActor(handler.getMediaStatus))
}

type createMediaSessionBody struct {
	Filename       string `json:"filename"`
	ContentType    string `json:"content_type"`
	SizeBytes      int64  `json:"size_bytes"`
	ChecksumSHA256 string `json:"checksum_sha256"`
}

func (handler *MediaHandler) createUploadSession(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	retryKey := strings.TrimSpace(request.Header.Get("Idempotency-Key"))
	if uuid.Validate(retryKey) != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	var body createMediaSessionBody
	if err := decodeStrictMediaJSON(response, request, &body); err != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if body.SizeBytes < domain.MinUploadSizeBytes {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if body.SizeBytes > domain.MaxUploadSizeBytes {
		writeError(response, request, http.StatusRequestEntityTooLarge, "MEDIA_PAYLOAD_TOO_LARGE")
		return
	}
	contentType := domain.MediaContentType(body.ContentType)
	if _, allowed := contentType.FileExtension(); !allowed {
		writeError(response, request, http.StatusUnsupportedMediaType, "MEDIA_TYPE_UNSUPPORTED")
		return
	}
	checksum, err := base64.StdEncoding.Strict().DecodeString(body.ChecksumSHA256)
	if err != nil || len(checksum) != domain.ChecksumByteLength {
		observability.RecordMediaFailure("deterministic")
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := handler.usecase.CreateUploadSession(request.Context(), domain.CreateUploadSessionRequest{
		OrgID: actor.OrgID, AssetID: assetID, RequestedBy: actor.UserID, IdempotencyKey: retryKey,
		OriginalFilename: body.Filename, DeclaredContentType: contentType,
		ExpectedSizeBytes: body.SizeBytes, DeclaredChecksumSHA256: checksum,
	})
	if err != nil {
		writeMediaError(response, request, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	observability.RecordMediaSession(map[bool]string{true: "replayed", false: "created"}[result.Replayed], body.SizeBytes)
	response.Header().Set("Idempotency-Replayed", map[bool]string{true: "true", false: "false"}[result.Replayed])
	writeMediaJSON(response, status, map[string]any{"data": uploadSessionEnvelope(result)})
}

func (handler *MediaHandler) getUploadSession(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	uploadID := request.PathValue("uploadId")
	if uuid.Validate(uploadID) != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := handler.usecase.GetUploadSession(request.Context(), repository.UploadSessionScope{
		OrgID: actor.OrgID, AssetID: assetID, UploadID: uploadID, RequestedBy: actor.UserID,
	})
	if err != nil {
		writeMediaError(response, request, err)
		return
	}
	writeMediaJSON(response, http.StatusOK, map[string]any{"data": uploadSessionEnvelope(result)})
}

func (handler *MediaHandler) refreshUploadSession(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	uploadID := request.PathValue("uploadId")
	if uuid.Validate(uploadID) != nil || request.ContentLength != 0 {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := handler.usecase.RefreshUploadSession(request.Context(), repository.UploadSessionScope{
		OrgID: actor.OrgID, AssetID: assetID, UploadID: uploadID, RequestedBy: actor.UserID,
	})
	if err != nil {
		writeMediaError(response, request, err)
		return
	}
	writeMediaJSON(response, http.StatusOK, map[string]any{"data": uploadSessionEnvelope(result)})
}

func (handler *MediaHandler) cancelUploadSession(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	uploadID := request.PathValue("uploadId")
	if uuid.Validate(uploadID) != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if err := handler.usecase.CancelUploadSession(request.Context(), repository.UploadSessionScope{
		OrgID: actor.OrgID, AssetID: assetID, UploadID: uploadID, RequestedBy: actor.UserID,
	}); err != nil {
		writeMediaError(response, request, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type commitMediaBody struct {
	UploadID string `json:"upload_id"`
}

func (handler *MediaHandler) commitUpload(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	var body commitMediaBody
	if err := decodeStrictMediaJSON(response, request, &body); err != nil || uuid.Validate(body.UploadID) != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	result, err := handler.usecase.CommitUpload(request.Context(), domain.CommitUploadRequest{
		OrgID: actor.OrgID, AssetID: assetID, RequestedBy: actor.UserID, UploadID: body.UploadID,
	})
	if err != nil {
		writeMediaError(response, request, err)
		return
	}
	status := http.StatusAccepted
	if result.Replayed {
		status = http.StatusOK
	}
	writeMediaJSON(response, status, map[string]any{"data": map[string]any{
		"asset_id": result.AssetID, "upload_id": result.UploadID, "job_id": result.JobID,
		"status": result.Status,
		"original": map[string]any{
			"filename": result.OriginalFilename, "content_type": result.OriginalContentType,
			"size_bytes": result.OriginalSizeBytes,
		},
		"accepted_at": result.AcceptedAt,
	}})
}

func (handler *MediaHandler) getMediaStatus(response http.ResponseWriter, request *http.Request) {
	actor, assetID, ok := mediaActorAndAsset(response, request)
	if !ok {
		return
	}
	result, err := handler.usecase.GetMediaStatus(request.Context(), repository.MediaStatusScope{
		OrgID: actor.OrgID, AssetID: assetID,
	})
	if err != nil {
		writeMediaError(response, request, err)
		return
	}
	writeMediaJSON(response, http.StatusOK, map[string]any{"data": mediaStatusEnvelope(result)})
}

func mediaActorAndAsset(response http.ResponseWriter, request *http.Request) (requestcontext.Actor, string, bool) {
	actor, err := requestcontext.GetActor(request.Context())
	if err != nil {
		writeError(response, request, http.StatusUnauthorized, "UNAUTHENTICATED")
		return requestcontext.Actor{}, "", false
	}
	assetID := request.PathValue("assetId")
	if uuid.Validate(assetID) != nil {
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
		return requestcontext.Actor{}, "", false
	}
	return actor, assetID, true
}

func decodeStrictMediaJSON(response http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(response, request.Body, 32*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body contains trailing JSON")
	}
	return nil
}

func uploadSessionEnvelope(result domain.UploadSessionResult) map[string]any {
	session := result.Session
	var descriptor any
	if result.Descriptor.URL != "" {
		descriptor = map[string]any{
			"protocol": result.Descriptor.Protocol, "method": result.Descriptor.Method,
			"url": result.Descriptor.URL, "headers": result.Descriptor.Headers,
			"credential_expires_at": result.Descriptor.CredentialExpiresAt,
		}
	}
	return map[string]any{
		"upload_id": session.ID, "asset_id": session.AssetID, "state": session.State,
		"upload": descriptor, "session_expires_at": session.SessionExpiresAt, "created_at": session.CreatedAt,
	}
}

func mediaStatusEnvelope(result domain.MediaStatusResult) map[string]any {
	var outputs any
	if result.Outputs != nil {
		output := func(value domain.MediaStatusOutput) map[string]any {
			return map[string]any{
				"url": value.URL, "width": value.Width, "height": value.Height,
				"size_bytes": value.SizeBytes, "content_type": value.ContentType,
			}
		}
		outputs = map[string]any{
			"thumbnail":  output(result.Outputs.Thumbnail),
			"web":        output(result.Outputs.Web),
			"expires_at": result.Outputs.ExpiresAt,
		}
	}
	var failure any
	if result.Error != nil {
		failure = map[string]any{"code": result.Error.Code, "message": result.Error.Message}
	}
	return map[string]any{
		"asset_id": result.AssetID, "upload_id": result.UploadID, "job_id": result.JobID,
		"status": result.Status, "attempt_count": result.AttemptCount, "stage": result.Stage,
		"original": map[string]any{
			"filename":              result.Original.Filename,
			"declared_content_type": result.Original.DeclaredContentType,
			"detected_content_type": result.Original.DetectedContentType,
			"size_bytes":            result.Original.SizeBytes,
			"sha256":                result.Original.SHA256,
		},
		"outputs": outputs, "error": failure,
		"accepted_at": result.AcceptedAt, "started_at": result.StartedAt,
		"completed_at": result.CompletedAt, "failed_at": result.FailedAt,
	}
}

func writeMediaJSON(response http.ResponseWriter, status int, payload any) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(payload)
}

func writeMediaError(response http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrUploadNotFound), errors.Is(err, repository.ErrAssetNotAvailable), errors.Is(err, repository.ErrMediaStatusNotFound):
		writeError(response, request, http.StatusNotFound, "MEDIA_UPLOAD_NOT_FOUND")
	case errors.Is(err, repository.ErrUploadInProgress):
		writeError(response, request, http.StatusConflict, "MEDIA_UPLOAD_IN_PROGRESS")
	case errors.Is(err, repository.ErrIdempotencyConflict):
		observability.RecordMediaRetryConflict()
		writeError(response, request, http.StatusConflict, "IDEMPOTENCY_KEY_REUSED")
	case errors.Is(err, repository.ErrUploadSessionExpired):
		writeError(response, request, http.StatusGone, "UPLOAD_SESSION_EXPIRED")
	case errors.Is(err, repository.ErrQuotaExceeded):
		writeError(response, request, http.StatusRequestEntityTooLarge, "MEDIA_QUOTA_EXCEEDED")
	case errors.Is(err, repository.ErrMediaObjectMismatch), errors.Is(err, domain.ErrObjectNotFound):
		observability.RecordMediaFailure("deterministic")
		writeError(response, request, http.StatusConflict, "MEDIA_OBJECT_MISMATCH")
	case errors.Is(err, repository.ErrUploadStateConflict):
		writeError(response, request, http.StatusConflict, "MEDIA_UPLOAD_STATE_CONFLICT")
	case errors.Is(err, domain.ErrUnsupportedMediaType):
		writeError(response, request, http.StatusUnsupportedMediaType, "MEDIA_TYPE_UNSUPPORTED")
	case errors.Is(err, usecase.ErrInvalidMediaSize):
		writeError(response, request, http.StatusRequestEntityTooLarge, "MEDIA_PAYLOAD_TOO_LARGE")
	case errors.Is(err, usecase.ErrInvalidMediaFilename), errors.Is(err, usecase.ErrInvalidMediaChecksum):
		writeError(response, request, http.StatusBadRequest, "BAD_REQUEST")
	default:
		observability.RecordMediaFailure("storage")
		writeError(response, request, http.StatusServiceUnavailable, "MEDIA_STORAGE_UNAVAILABLE")
	}
}
