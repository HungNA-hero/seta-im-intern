package usecase

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/processing"
	"seta-im-intern/go-asset-core/internal/repository"
	"strings"
)

const checksumMismatchCode = "MEDIA_CHECKSUM_MISMATCH"

var ErrArtifactOutsideScratch = errors.New("processor named an artifact outside its scratch directory")

type MediaSourceStore interface {
	LoadProcessingSource(ctx context.Context, versionID string) (repository.MediaProcessingSource, error)
	CompleteAndPromote(ctx context.Context, completion repository.MediaCompletion, held domain.JobLease) (bool, error)
	FailVersion(ctx context.Context, failure repository.MediaFailure, held domain.JobLease) (bool, error)
}

type MediaObjectStore interface {
	Get(ctx context.Context, key domain.ObjectKey) (io.ReadCloser, error)
	Head(ctx context.Context, key domain.ObjectKey) (domain.ObjectAttributes, error)
	Put(ctx context.Context, key domain.ObjectKey, body io.Reader, attributes domain.PutAttributes) error
	Delete(ctx context.Context, key domain.ObjectKey) error
}

type ImageProcessor interface {
	Process(ctx context.Context, request processing.Request) (processing.Outcome, error)
}

type RenditionOptions struct {
	Limits      domain.MediaLimits
	ScratchRoot string
	Logger      *slog.Logger
}

type RenditionExecutor struct {
	sources   MediaSourceStore
	objects   MediaObjectStore
	processor ImageProcessor
	options   RenditionOptions
}

func NewRenditionExecutor(
	sources MediaSourceStore,
	objects MediaObjectStore,
	processor ImageProcessor,
	options RenditionOptions,
) *RenditionExecutor {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &RenditionExecutor{sources: sources, objects: objects, processor: processor, options: options}
}

func (executor *RenditionExecutor) Execute(ctx context.Context, job domain.MediaProcessingJob, lease *HeldLease) error {
	source, err := executor.sources.LoadProcessingSource(ctx, job.VersionID)
	if err != nil {
		return fmt.Errorf("loading the processing source: %w", err)
	}

	scratch, err := os.MkdirTemp(executor.options.ScratchRoot, "media-job-")
	if err != nil {
		return fmt.Errorf("creating a scratch directory: %w", err)
	}
	defer executor.removeScratch(scratch)

	sourcePath, digest, err := executor.downloadRaw(ctx, source, scratch)
	if err != nil {
		return err
	}
	if !bytes.Equal(digest, source.AdmittedSHA256) {
		return executor.failVersion(ctx, source, job, lease, checksumMismatchCode)
	}

	outcome, err := executor.processor.Process(ctx, executor.requestFor(source, sourcePath, scratch))
	if err != nil {
		if errors.Is(err, processing.ErrRejected) {
			return executor.failVersion(ctx, source, job, lease, processing.ReasonCode(err))
		}
		return err
	}
	if !bytes.Equal(outcome.SourceSHA256, digest) {
		return fmt.Errorf("processor read different bytes than were downloaded for version %s", source.VersionID)
	}

	outputs, uploaded, err := executor.uploadArtifacts(ctx, source, outcome, scratch)
	if err != nil {
		executor.discard(ctx, uploaded)
		return err
	}

	applied, err := executor.sources.CompleteAndPromote(ctx, repository.MediaCompletion{
		JobID:               job.ID,
		VersionID:           source.VersionID,
		OrgID:               source.OrgID,
		AssetID:             source.AssetID,
		DetectedContentType: outcome.DetectedType,
		SourceWidth:         outcome.SourceWidth,
		SourceHeight:        outcome.SourceHeight,
		SourceSHA256:        digest,
		Outputs:             outputs,
	}, lease.Current())
	if err != nil {
		executor.discard(ctx, uploaded)
		return fmt.Errorf("promoting version %s: %w", source.VersionID, err)
	}
	if !applied {
		executor.options.Logger.Warn(
			"media job completed but the lease was no longer held",
			"jobId", job.ID, "versionId", source.VersionID,
		)
		return fmt.Errorf("promoting version %s: %w", source.VersionID, ErrLeaseLost)
	}
	return nil
}

func (executor *RenditionExecutor) downloadRaw(
	ctx context.Context,
	source repository.MediaProcessingSource,
	scratch string,
) (string, []byte, error) {
	body, err := executor.objects.Get(ctx, source.RawObjectKey)
	if err != nil {
		return "", nil, fmt.Errorf("reading the raw object: %w", err)
	}
	defer func() { _ = body.Close() }()

	path := filepath.Join(scratch, "original")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("creating the scratch source file: %w", err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	limited := io.LimitReader(body, source.OriginalSizeBytes+1)
	written, err := io.Copy(io.MultiWriter(file, hasher), limited)
	if err != nil {
		return "", nil, fmt.Errorf("downloading the raw object: %w", err)
	}
	if written != source.OriginalSizeBytes {
		return "", nil, fmt.Errorf(
			"raw object for version %s is %d bytes, admitted %d", source.VersionID, written, source.OriginalSizeBytes,
		)
	}
	return path, hasher.Sum(nil), nil
}

func (executor *RenditionExecutor) requestFor(
	source repository.MediaProcessingSource,
	sourcePath string,
	scratch string,
) processing.Request {
	return processing.Request{
		SourcePath:   sourcePath,
		ScratchDir:   scratch,
		DeclaredType: source.DeclaredContentType,
		MaxSidePx:    executor.options.Limits.MaxImageSidePx,
		MaxPixels:    executor.options.Limits.MaxImagePixels,
		Outputs:      domain.MediaOutputManifest,
	}
}

func (executor *RenditionExecutor) uploadArtifacts(
	ctx context.Context,
	source repository.MediaProcessingSource,
	outcome processing.Outcome,
	scratch string,
) ([]domain.MediaOutput, []domain.ObjectKey, error) {
	outputs := make([]domain.MediaOutput, 0, len(outcome.Artifacts))
	uploaded := make([]domain.ObjectKey, 0, len(outcome.Artifacts))

	for _, artifact := range outcome.Artifacts {
		key, err := domain.ProcessedObjectKey(
			source.OrgID, source.AssetID, source.VersionID, artifact.Kind, artifact.ContentType,
		)
		if err != nil {
			return nil, uploaded, fmt.Errorf("deriving the %s key: %w", artifact.Kind, err)
		}
		digest, err := hex.DecodeString(artifact.SHA256)
		if err != nil {
			return nil, uploaded, fmt.Errorf("unusable %s digest: %w", artifact.Kind, err)
		}
		artifact.Path, err = confinedToScratch(scratch, artifact.Path)
		if err != nil {
			return nil, uploaded, err
		}

		created, err := executor.putArtifact(ctx, key, artifact, digest)
		if err != nil {
			return nil, uploaded, err
		}
		if created {
			uploaded = append(uploaded, key)
		}

		outputs = append(outputs, domain.MediaOutput{
			VersionID:   source.VersionID,
			Kind:        artifact.Kind,
			ObjectKey:   key.String(),
			ContentType: artifact.ContentType,
			Width:       artifact.Width,
			Height:      artifact.Height,
			SizeBytes:   artifact.SizeBytes,
			SHA256:      digest,
		})
	}
	return outputs, uploaded, nil
}

// confinedToScratch refuses any path the child named that does not resolve
// inside the directory this job was given. The child is the process assumed to
// be compromised by a hostile image, so a path it chose is the one input here
// that must not be trusted: an unchecked path is uploaded to a processed key
// that PresignGet will sign for download.
//
// Symlinks are resolved before the comparison because the child can create one
// inside its own scratch directory.
func confinedToScratch(scratch, candidate string) (string, error) {
	resolvedScratch, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return "", fmt.Errorf("resolving the scratch directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(candidate))
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrArtifactOutsideScratch, candidate)
	}

	root := resolvedScratch + string(filepath.Separator)
	if !strings.HasPrefix(resolved, root) {
		return "", fmt.Errorf("%w: %s", ErrArtifactOutsideScratch, candidate)
	}
	return resolved, nil
}

func (executor *RenditionExecutor) putArtifact(
	ctx context.Context,
	key domain.ObjectKey,
	artifact processing.Artifact,
	digest []byte,
) (bool, error) {
	file, err := os.Open(artifact.Path)
	if err != nil {
		return false, fmt.Errorf("opening the %s derivative: %w", artifact.Kind, err)
	}
	defer func() { _ = file.Close() }()

	err = executor.objects.Put(ctx, key, file, domain.PutAttributes{
		ContentType:    artifact.ContentType,
		ChecksumSHA256: digest,
	})
	if errors.Is(err, domain.ErrObjectAlreadyExists) {
		if verifyErr := executor.verifyExistingArtifact(ctx, key, artifact, digest); verifyErr != nil {
			return false, verifyErr
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storing the %s derivative: %w", artifact.Kind, err)
	}
	return true, nil
}

func (executor *RenditionExecutor) verifyExistingArtifact(
	ctx context.Context,
	key domain.ObjectKey,
	artifact processing.Artifact,
	digest []byte,
) error {
	attributes, err := executor.objects.Head(ctx, key)
	if err != nil {
		return fmt.Errorf("inspecting the existing %s derivative: %w", artifact.Kind, err)
	}
	if attributes.SizeBytes != artifact.SizeBytes || attributes.ContentType != string(artifact.ContentType) {
		return fmt.Errorf(
			"existing %s derivative metadata does not match the rendered artifact",
			artifact.Kind,
		)
	}
	if len(attributes.ChecksumSHA256) > 0 {
		if !bytes.Equal(attributes.ChecksumSHA256, digest) {
			return fmt.Errorf("existing %s derivative checksum does not match the rendered artifact", artifact.Kind)
		}
		return nil
	}

	body, err := executor.objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("reading the existing %s derivative: %w", artifact.Kind, err)
	}
	defer func() { _ = body.Close() }()

	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(body, artifact.SizeBytes+1))
	if err != nil {
		return fmt.Errorf("hashing the existing %s derivative: %w", artifact.Kind, err)
	}
	if written != artifact.SizeBytes || !bytes.Equal(hasher.Sum(nil), digest) {
		return fmt.Errorf("existing %s derivative bytes do not match the rendered artifact", artifact.Kind)
	}
	return nil
}

func (executor *RenditionExecutor) failVersion(
	ctx context.Context,
	source repository.MediaProcessingSource,
	job domain.MediaProcessingJob,
	lease *HeldLease,
	code string,
	uploaded ...domain.ObjectKey,
) error {
	applied, err := executor.sources.FailVersion(ctx, repository.MediaFailure{
		JobID:     job.ID,
		VersionID: source.VersionID,
		OrgID:     source.OrgID,
		AssetID:   source.AssetID,
		ErrorCode: code,
	}, lease.Current())
	if err != nil {
		return fmt.Errorf("failing version %s: %w", source.VersionID, err)
	}
	if !applied {
		executor.options.Logger.Warn(
			"media job failed but the lease was no longer held",
			"jobId", job.ID, "versionId", source.VersionID, "errorCode", code,
		)
		return fmt.Errorf("failing version %s: %w", source.VersionID, ErrLeaseLost)
	}

	executor.discard(ctx, uploaded)
	executor.options.Logger.Info(
		"media version failed deterministically",
		"jobId", job.ID, "versionId", source.VersionID, "errorCode", code,
	)
	return nil
}

func (executor *RenditionExecutor) discard(ctx context.Context, uploaded []domain.ObjectKey) {
	for _, key := range uploaded {
		if err := executor.objects.Delete(ctx, key); err != nil {
			executor.options.Logger.Warn(
				"could not remove a partial media derivative",
				"objectKey", key.String(), "error", err.Error(),
			)
		}
	}
}

func (executor *RenditionExecutor) removeScratch(scratch string) {
	if err := os.RemoveAll(scratch); err != nil {
		executor.options.Logger.Warn("could not remove the media scratch directory", "path", scratch, "error", err.Error())
	}
}
