package usecase_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/processing"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

const (
	testOrgID     = "11111111-1111-1111-1111-111111111111"
	testAssetID   = "22222222-2222-2222-2222-222222222222"
	testVersionID = "33333333-3333-3333-3333-333333333333"
	testUploadID  = "44444444-4444-4444-4444-444444444444"
	testJobID     = "55555555-5555-5555-5555-555555555555"
)

var rawBytes = []byte("the original uploaded bytes")

func digestOf(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// --- fakes at the three ports the executor depends on ---

type fakeSourceStore struct {
	source      repository.MediaProcessingSource
	loadErr     error
	completions []repository.MediaCompletion
	failures    []repository.MediaFailure
	applied     bool
	writeErr    error
}

func (store *fakeSourceStore) LoadProcessingSource(_ context.Context, _ string) (repository.MediaProcessingSource, error) {
	if store.loadErr != nil {
		return repository.MediaProcessingSource{}, store.loadErr
	}
	return store.source, nil
}

func (store *fakeSourceStore) CompleteAndPromote(_ context.Context, completion repository.MediaCompletion, _ domain.JobLease) (bool, error) {
	store.completions = append(store.completions, completion)
	return store.applied, store.writeErr
}

func (store *fakeSourceStore) FailVersion(_ context.Context, failure repository.MediaFailure, _ domain.JobLease) (bool, error) {
	store.failures = append(store.failures, failure)
	return store.applied, store.writeErr
}

type fakeObjectStore struct {
	stored              map[string][]byte
	deleted             []string
	putErr              func(key string) error
	deleteErr           error
	deleteErrFor        map[string]error
	headWithoutChecksum bool
}

func newFakeObjectStore(rawKey string, raw []byte) *fakeObjectStore {
	return &fakeObjectStore{stored: map[string][]byte{rawKey: raw}}
}

func (store *fakeObjectStore) Get(_ context.Context, key domain.ObjectKey) (io.ReadCloser, error) {
	body, ok := store.stored[key.String()]
	if !ok {
		return nil, domain.ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (store *fakeObjectStore) Head(_ context.Context, key domain.ObjectKey) (domain.ObjectAttributes, error) {
	body, ok := store.stored[key.String()]
	if !ok {
		return domain.ObjectAttributes{}, domain.ErrObjectNotFound
	}
	attributes := domain.ObjectAttributes{
		SizeBytes:   int64(len(body)),
		ContentType: string(domain.MediaContentTypePNG),
	}
	if !store.headWithoutChecksum {
		attributes.ChecksumSHA256 = digestOf(body)
	}
	return attributes, nil
}

func (store *fakeObjectStore) Put(_ context.Context, key domain.ObjectKey, body io.Reader, _ domain.PutAttributes) error {
	if store.putErr != nil {
		if err := store.putErr(key.String()); err != nil {
			return err
		}
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	store.stored[key.String()] = data
	return nil
}

func (store *fakeObjectStore) Delete(_ context.Context, key domain.ObjectKey) error {
	if err, ok := store.deleteErrFor[key.String()]; ok {
		return err
	}
	if store.deleteErr != nil {
		return store.deleteErr
	}
	store.deleted = append(store.deleted, key.String())
	delete(store.stored, key.String())
	return nil
}

type fakeProcessor struct {
	outcome          processing.Outcome
	err              error
	calls            int
	requests         []processing.Request
	escapeArtifactTo func(scratch string) string
}

func (processor *fakeProcessor) Process(_ context.Context, request processing.Request) (processing.Outcome, error) {
	processor.calls++
	processor.requests = append(processor.requests, request)
	if processor.err != nil {
		return processing.Outcome{}, processor.err
	}
	// Artifacts are written where the child would have written them, so the
	// parent's upload step reads real files.
	for index, artifact := range processor.outcome.Artifacts {
		path := filepath.Join(request.ScratchDir, string(artifact.Kind)+".png")
		if err := os.WriteFile(path, artifactBytes(artifact.Kind), 0o600); err != nil {
			return processing.Outcome{}, err
		}
		processor.outcome.Artifacts[index].Path = path
	}
	if processor.escapeArtifactTo != nil {
		processor.outcome.Artifacts[0].Path = processor.escapeArtifactTo(request.ScratchDir)
	}
	return processor.outcome, nil
}

func artifactBytes(kind domain.MediaOutputKind) []byte {
	return []byte("rendered " + string(kind))
}

// --- fixture ---

type renditionFixture struct {
	sources   *fakeSourceStore
	objects   *fakeObjectStore
	processor *fakeProcessor
	scratch   string
	lease     *usecase.HeldLease
	executor  *usecase.RenditionExecutor
}

func newRenditionFixture(t *testing.T) *renditionFixture {
	t.Helper()

	rawKey, err := domain.RawObjectKey(testOrgID, testAssetID, testUploadID, domain.MediaContentTypePNG)
	if err != nil {
		t.Fatalf("derive raw key: %v", err)
	}
	sources := &fakeSourceStore{
		applied: true,
		source: repository.MediaProcessingSource{
			OrgID: testOrgID, AssetID: testAssetID, VersionID: testVersionID, UploadID: testUploadID,
			RawObjectKey: rawKey, DeclaredContentType: domain.MediaContentTypePNG,
			AdmittedSHA256: digestOf(rawBytes), OriginalSizeBytes: int64(len(rawBytes)),
		},
	}
	processor := &fakeProcessor{outcome: successfulOutcome()}
	objects := newFakeObjectStore(rawKey.String(), rawBytes)
	scratch := t.TempDir()

	return &renditionFixture{
		sources: sources, objects: objects, processor: processor, scratch: scratch,
		lease: usecase.NewHeldLease(domain.JobLease{Owner: "worker-a"}),
		executor: usecase.NewRenditionExecutor(sources, objects, processor, usecase.RenditionOptions{
			Limits:      domain.MediaLimits{MaxUploadSizeBytes: 1 << 20, MaxImageSidePx: 20_000, MaxImagePixels: 50_000_000},
			ScratchRoot: scratch,
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		}),
	}
}

func successfulOutcome() processing.Outcome {
	artifacts := make([]processing.Artifact, 0, len(domain.MediaOutputManifest))
	for _, policy := range domain.MediaOutputManifest {
		artifacts = append(artifacts, processing.Artifact{
			Kind: policy.Kind, ContentType: domain.MediaContentTypePNG,
			Width: 64, Height: 64, SizeBytes: int64(len(artifactBytes(policy.Kind))),
			SHA256: hex.EncodeToString(digestOf(artifactBytes(policy.Kind))),
		})
	}
	return processing.Outcome{
		DetectedType: domain.MediaContentTypePNG, SourceWidth: 2048, SourceHeight: 1152,
		SourceSHA256: digestOf(rawBytes), Artifacts: artifacts,
	}
}

func (fixture *renditionFixture) run(t *testing.T) error {
	t.Helper()
	return fixture.executor.Execute(
		context.Background(),
		domain.MediaProcessingJob{ID: testJobID, OrgID: testOrgID, AssetID: testAssetID, VersionID: testVersionID},
		fixture.lease,
	)
}

func (fixture *renditionFixture) processedKey(t *testing.T, kind domain.MediaOutputKind) string {
	t.Helper()
	key, err := domain.ProcessedObjectKey(testOrgID, testAssetID, testVersionID, kind, domain.MediaContentTypePNG)
	if err != nil {
		t.Fatalf("derive processed key: %v", err)
	}
	return key.String()
}

// --- the content-identity gate ---

// The checksum admitted at session creation is the client's commitment to which
// bytes it would upload. Bytes that do not match it are a different file, and
// no amount of retrying will change that.
func TestExecuteFailsTheVersionWhenTheRawBytesDoNotMatchTheAdmittedChecksum(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.sources.source.AdmittedSHA256 = digestOf([]byte("a completely different file"))

	err := fixture.run(t)

	if err != nil {
		t.Fatalf("a deterministic failure must be settled, not returned: %v", err)
	}
	if len(fixture.sources.failures) != 1 {
		t.Fatalf("failures = %d, want the version failed once", len(fixture.sources.failures))
	}
	if code := fixture.sources.failures[0].ErrorCode; code != "MEDIA_CHECKSUM_MISMATCH" {
		t.Errorf("errorCode = %q, want MEDIA_CHECKSUM_MISMATCH", code)
	}
	if fixture.processor.calls != 0 {
		t.Error("bytes that failed the identity check must never reach the decoder")
	}
	if len(fixture.sources.completions) != 0 {
		t.Error("a mismatched upload must not be promoted")
	}
}

// A rejection is the child's verdict on the image, and the code it names is
// what the asset owner is eventually shown.
func TestExecuteFailsTheVersionWithTheProcessorsReasonCode(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.processor.err = &processing.Rejection{Code: "IMAGE_DIMENSIONS_EXCEEDED"}

	err := fixture.run(t)

	if err != nil {
		t.Fatalf("a rejection must be settled, not returned: %v", err)
	}
	if len(fixture.sources.failures) != 1 {
		t.Fatalf("failures = %d, want the version failed once", len(fixture.sources.failures))
	}
	if code := fixture.sources.failures[0].ErrorCode; code != "IMAGE_DIMENSIONS_EXCEEDED" {
		t.Errorf("errorCode = %q, want the processor's code", code)
	}
}

// The mirror case: a processor that died says nothing about the image. Writing
// terminal state here would destroy a valid upload permanently.
func TestExecuteReturnsAProcessorFailureWithoutWritingTerminalState(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.processor.err = processing.ErrProcessorFailed

	err := fixture.run(t)

	if !errors.Is(err, processing.ErrProcessorFailed) {
		t.Fatalf("error = %v, want the failure returned for retry", err)
	}
	if len(fixture.sources.failures) != 0 {
		t.Error("an infrastructure failure must not fail the version")
	}
	if len(fixture.sources.completions) != 0 {
		t.Error("an infrastructure failure must not promote the version")
	}
}

// --- the successful path ---

// Both derivatives must exist in storage before the database is told they do,
// and they must land on keys derived from the version rather than on anything
// the child chose.
func TestExecuteStoresBothDerivativesBeforePromoting(t *testing.T) {
	fixture := newRenditionFixture(t)

	if err := fixture.run(t); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, policy := range domain.MediaOutputManifest {
		key := fixture.processedKey(t, policy.Kind)
		stored, ok := fixture.objects.stored[key]
		if !ok {
			t.Fatalf("no object stored at %s", key)
		}
		if !bytes.Equal(stored, artifactBytes(policy.Kind)) {
			t.Errorf("object at %s = %q, want the rendered bytes", key, stored)
		}
	}

	if len(fixture.sources.completions) != 1 {
		t.Fatalf("completions = %d, want exactly one promotion", len(fixture.sources.completions))
	}
	completion := fixture.sources.completions[0]
	if len(completion.Outputs) != len(domain.MediaOutputManifest) {
		t.Errorf("outputs = %d, want %d", len(completion.Outputs), len(domain.MediaOutputManifest))
	}
	if !bytes.Equal(completion.SourceSHA256, digestOf(rawBytes)) {
		t.Errorf("sourceSha256 = %x, want the digest of the downloaded bytes", completion.SourceSHA256)
	}
	if completion.SourceWidth != 2048 || completion.SourceHeight != 1152 {
		t.Errorf("source = %dx%d, want the processor's measurements", completion.SourceWidth, completion.SourceHeight)
	}
	for _, output := range completion.Outputs {
		if output.ObjectKey != fixture.processedKey(t, output.Kind) {
			t.Errorf("%s key = %q, want the derived key", output.Kind, output.ObjectKey)
		}
	}
}

func TestExecuteReturnsLeaseLossWhenPromotionIsNotApplied(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.sources.applied = false

	err := fixture.run(t)

	if !errors.Is(err, usecase.ErrLeaseLost) {
		t.Fatalf("error = %v, want %v", err, usecase.ErrLeaseLost)
	}
}

func TestExecuteReturnsLeaseLossWhenFailureIsNotApplied(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.sources.applied = false
	fixture.processor.err = &processing.Rejection{Code: "INVALID_IMAGE"}

	err := fixture.run(t)

	if !errors.Is(err, usecase.ErrLeaseLost) {
		t.Fatalf("error = %v, want %v", err, usecase.ErrLeaseLost)
	}
}

// The child writes into a scratch directory the parent owns. Leaving it behind
// would accumulate decoded images on a worker that runs indefinitely.
func TestExecuteRemovesItsScratchDirectory(t *testing.T) {
	fixture := newRenditionFixture(t)

	if err := fixture.run(t); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	entries, err := os.ReadDir(fixture.scratch)
	if err != nil {
		t.Fatalf("read scratch root: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("scratch root still holds %d entries, want it empty", len(entries))
	}
}

// --- partial-artifact cleanup (T073) ---

// A run that stores one derivative and then fails must take that derivative
// back. The raw original is never touched: it is retained for the asset's
// lifetime regardless of how processing went.
func TestExecuteRemovesPartialDerivativesWhenAnUploadFails(t *testing.T) {
	fixture := newRenditionFixture(t)
	webKey := fixture.processedKey(t, domain.MediaOutputWeb)
	fixture.objects.putErr = func(key string) error {
		if key == webKey {
			return errors.New("storage refused the write")
		}
		return nil
	}

	err := fixture.run(t)

	if err == nil {
		t.Fatal("a failed upload must leave the job retryable")
	}
	thumbnailKey := fixture.processedKey(t, domain.MediaOutputThumbnail)
	if !contains(fixture.objects.deleted, thumbnailKey) {
		t.Errorf("deleted = %v, want the stored thumbnail removed", fixture.objects.deleted)
	}
	rawKey := fixture.sources.source.RawObjectKey.String()
	if contains(fixture.objects.deleted, rawKey) {
		t.Error("the raw original must never be deleted by the worker")
	}
	if _, ok := fixture.objects.stored[rawKey]; !ok {
		t.Error("the raw original must survive a failed attempt")
	}
	if len(fixture.sources.completions) != 0 {
		t.Error("a partial output set must not be promoted")
	}
}

// A deterministic rejection after a partial upload must clean up too, or every
// hostile file would leave a derivative behind.
func TestExecuteRemovesPartialDerivativesWhenThePromotionIsRefused(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.sources.writeErr = repository.ErrIncompleteOutputSet

	err := fixture.run(t)

	if err == nil {
		t.Fatal("a refused promotion must be reported")
	}
	for _, policy := range domain.MediaOutputManifest {
		if !contains(fixture.objects.deleted, fixture.processedKey(t, policy.Kind)) {
			t.Errorf("deleted = %v, want the %s derivative removed", fixture.objects.deleted, policy.Kind)
		}
	}
}

// A retry re-derives the same bytes for the same key. The no-overwrite write
// that protects raw uploads must not turn that into a permanent failure.
func TestExecuteToleratesDerivativesAnEarlierAttemptAlreadyStored(t *testing.T) {
	fixture := newRenditionFixture(t)
	for _, policy := range domain.MediaOutputManifest {
		fixture.objects.stored[fixture.processedKey(t, policy.Kind)] = artifactBytes(policy.Kind)
	}
	fixture.objects.putErr = func(string) error { return domain.ErrObjectAlreadyExists }

	if err := fixture.run(t); err != nil {
		t.Fatalf("Execute on a retried job: %v", err)
	}

	if len(fixture.sources.completions) != 1 {
		t.Fatalf("completions = %d, want the retry to promote", len(fixture.sources.completions))
	}
}

func TestExecuteRefusesAnExistingDerivativeWithDifferentBytes(t *testing.T) {
	fixture := newRenditionFixture(t)
	for _, policy := range domain.MediaOutputManifest {
		fixture.objects.stored[fixture.processedKey(t, policy.Kind)] = artifactBytes(policy.Kind)
	}
	webBody := artifactBytes(domain.MediaOutputWeb)
	fixture.objects.stored[fixture.processedKey(t, domain.MediaOutputWeb)] = bytes.Repeat([]byte{'x'}, len(webBody))
	fixture.objects.putErr = func(string) error { return domain.ErrObjectAlreadyExists }

	err := fixture.run(t)

	if err == nil {
		t.Fatal("a corrupt object at an immutable key must not be promoted")
	}
	if len(fixture.sources.completions) != 0 {
		t.Error("metadata for newly rendered bytes must not describe different stored bytes")
	}
	thumbnailKey := fixture.processedKey(t, domain.MediaOutputThumbnail)
	if contains(fixture.objects.deleted, thumbnailKey) {
		t.Error("a matching pre-existing derivative must not be deleted when another derivative is corrupt")
	}
}

func TestExecuteHashesAnExistingDerivativeWhenHeadHasNoChecksum(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.objects.headWithoutChecksum = true
	for _, policy := range domain.MediaOutputManifest {
		fixture.objects.stored[fixture.processedKey(t, policy.Kind)] = artifactBytes(policy.Kind)
	}
	fixture.objects.putErr = func(string) error { return domain.ErrObjectAlreadyExists }

	if err := fixture.run(t); err != nil {
		t.Fatalf("Execute on a valid retry without HEAD checksum: %v", err)
	}
}

// --- content identity ---

// A stored object that is not the size admitted is not the admitted object.
// Catching it before hashing bounds what a substituted object can write.
func TestExecuteRefusesARawObjectOfTheWrongSize(t *testing.T) {
	fixture := newRenditionFixture(t)
	fixture.objects.stored[fixture.sources.source.RawObjectKey.String()] = append(rawBytes, "extra"...)

	err := fixture.run(t)

	if err == nil {
		t.Fatal("a raw object of the wrong size must not be processed")
	}
	if fixture.processor.calls != 0 {
		t.Error("an object failing the size check must never reach the decoder")
	}
}

// The child is trusted with two paths and some numbers, and nothing else.
func TestExecuteHandsTheProcessorNoClientControlledValues(t *testing.T) {
	fixture := newRenditionFixture(t)

	if err := fixture.run(t); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(fixture.processor.requests) != 1 {
		t.Fatalf("processor calls = %d, want 1", len(fixture.processor.requests))
	}
	request := fixture.processor.requests[0]
	rawKey := fixture.sources.source.RawObjectKey.String()
	for _, field := range []string{request.SourcePath, request.ScratchDir} {
		if strings.Contains(field, rawKey) || strings.Contains(field, testUploadID) {
			t.Errorf("%q leaks the object key or upload identity to the child", field)
		}
	}
	if request.MaxSidePx != 20_000 || request.MaxPixels != 50_000_000 {
		t.Errorf("limits = %d/%d, want the configured bounds", request.MaxSidePx, request.MaxPixels)
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// The whole point of the child process is that it may be compromised by a
// hostile image. A path it names must be confined to the scratch directory the
// parent created, or a compromised decoder can have any file the worker can
// read uploaded to a processed key that PresignGet will sign.
func TestExecuteRefusesArtifactPathsOutsideItsScratchDirectory(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "worker-credentials")
	if err := os.WriteFile(secret, []byte("ASSET_DB_PASSWORD=hunter2"), 0o600); err != nil {
		t.Fatalf("stage secret: %v", err)
	}

	cases := map[string]func(scratch string) string{
		"absolute path elsewhere": func(string) string { return secret },
		"traversal out of scratch": func(scratch string) string {
			return filepath.Join(scratch, "..", filepath.Base(secret))
		},
	}

	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newRenditionFixture(t)
			fixture.processor.escapeArtifactTo = path

			err := fixture.run(t)

			if err == nil {
				t.Fatal("an artifact outside the scratch directory was accepted")
			}
			for key, stored := range fixture.objects.stored {
				if bytes.Contains(stored, []byte("hunter2")) {
					t.Errorf("secret bytes were uploaded to %s", key)
				}
			}
			if len(fixture.sources.completions) != 0 {
				t.Error("a version naming an escaped artifact must not be promoted")
			}
		})
	}
}
