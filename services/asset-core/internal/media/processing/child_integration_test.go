package processing_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/processing"
)

// These exercise the real cmd/media-processor binary through the real
// supervisor. Everything else in this package stubs the child; this is the one
// place that proves the two halves actually speak the same protocol.

var builtProcessor string

// TestMain builds the processor once for the whole package. It cannot live in a
// t.TempDir(), which is removed when its own test ends and would leave every
// later test pointing at a deleted binary.
func TestMain(main *testing.M) {
	// The helper child re-invokes this binary; it must not rebuild anything.
	if os.Getenv("GO_WANT_HELPER_PROCESS") == "1" {
		os.Exit(main.Run())
	}

	buildDir, err := os.MkdirTemp("", "media-processor-build")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(buildDir) }()

	binary := filepath.Join(buildDir, "media-processor")
	if output, err := exec.Command("go", "build", "-o", binary, "seta-im-intern/go-asset-core/cmd/media-processor").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("build media-processor: %v\n%s", err, output))
	}
	builtProcessor = binary

	code := main.Run()
	_ = os.RemoveAll(buildDir)
	os.Exit(code)
}

func processorBinary(t *testing.T) string {
	t.Helper()
	if builtProcessor == "" {
		t.Skip("media-processor was not built for this run")
	}
	return builtProcessor
}

func realSupervisor(t *testing.T) *processing.ChildSupervisor {
	t.Helper()
	return processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: processorBinary(t),
		Budgets:        processing.Budgets{Validation: 15 * time.Second, Transform: 60 * time.Second, Total: 90 * time.Second},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func requestFor(t *testing.T, corpusFile string, declared domain.MediaContentType) processing.Request {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "media", corpusFile))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	scratch := t.TempDir()
	source := filepath.Join(scratch, "original")
	if err := os.WriteFile(source, data, 0o600); err != nil {
		t.Fatalf("stage source: %v", err)
	}

	return processing.Request{
		SourcePath:   source,
		ScratchDir:   scratch,
		DeclaredType: declared,
		MaxSidePx:    20_000,
		MaxPixels:    50_000_000,
		Outputs:      domain.MediaOutputManifest,
	}
}

func TestRealProcessorProducesTwoDerivativesOnDisk(t *testing.T) {
	request := requestFor(t, "valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG)

	outcome, err := realSupervisor(t).Process(context.Background(), request)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(outcome.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want exactly 2", len(outcome.Artifacts))
	}
	if outcome.DetectedType != domain.MediaContentTypeJPEG {
		t.Errorf("detected = %q, want image/jpeg", outcome.DetectedType)
	}
	if outcome.SourceWidth != 2048 || outcome.SourceHeight != 1152 {
		t.Errorf("source = %dx%d, want 2048x1152", outcome.SourceWidth, outcome.SourceHeight)
	}

	for _, artifact := range outcome.Artifacts {
		written, err := os.ReadFile(artifact.Path)
		if err != nil {
			t.Fatalf("read %s: %v", artifact.Kind, err)
		}
		if int64(len(written)) != artifact.SizeBytes {
			t.Errorf("%s is %d bytes on disk, manifest says %d", artifact.Kind, len(written), artifact.SizeBytes)
		}
		if _, err := hex.DecodeString(artifact.SHA256); err != nil {
			t.Errorf("%s has an unusable digest: %v", artifact.Kind, err)
		}
	}
}

// The content identity the parent compares against the admitted checksum is
// computed from the complete downloaded bytes, not from anything the client said.
func TestRealProcessorHashesTheCompleteSourceBytes(t *testing.T) {
	request := requestFor(t, "valid/small-64x64.png", domain.MediaContentTypePNG)
	source, err := os.ReadFile(request.SourcePath)
	if err != nil {
		t.Fatalf("read staged source: %v", err)
	}

	outcome, err := realSupervisor(t).Process(context.Background(), request)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	expected := sha256Of(source)
	if hex.EncodeToString(outcome.SourceSHA256) != expected {
		t.Errorf("source digest = %x, want %s", outcome.SourceSHA256, expected)
	}
}

// The full hostile corpus, through the real binary: every one must come back as
// a deterministic rejection rather than a retryable failure, or a hostile file
// would consume the whole attempt budget on every worker.
func TestRealProcessorRejectsTheHostileCorpusDeterministically(t *testing.T) {
	cases := map[string]struct {
		file     string
		declared domain.MediaContentType
	}{
		"concatenated jpeg": {"hostile/jpeg-concatenated.jpg", domain.MediaContentTypeJPEG},
		"trailing jpeg":     {"hostile/jpeg-trailing-payload.jpg", domain.MediaContentTypeJPEG},
		"truncated jpeg":    {"hostile/jpeg-truncated.jpg", domain.MediaContentTypeJPEG},
		"concatenated png":  {"hostile/png-concatenated.png", domain.MediaContentTypePNG},
		"trailing png":      {"hostile/png-trailing-payload.png", domain.MediaContentTypePNG},
		"truncated png":     {"hostile/png-truncated.png", domain.MediaContentTypePNG},
		"bad crc png":       {"hostile/png-bad-crc.png", domain.MediaContentTypePNG},
		"animated png":      {"hostile/png-animated.apng.png", domain.MediaContentTypePNG},
		"dimension bomb":    {"hostile/png-dimension-bomb.png", domain.MediaContentTypePNG},
		"type mismatch":     {"hostile/jpeg-bytes-named.png", domain.MediaContentTypePNG},
		"not an image":      {"hostile/not-an-image.png", domain.MediaContentTypePNG},
	}

	supervisor := realSupervisor(t)
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := supervisor.Process(context.Background(), requestFor(t, testCase.file, testCase.declared))

			if err == nil {
				t.Fatal("hostile content was processed successfully")
			}
			if !isRejection(err) {
				t.Fatalf("error = %v, want a deterministic rejection", err)
			}
		})
	}
}

func isRejection(err error) bool {
	return errors.Is(err, processing.ErrRejected) && !errors.Is(err, processing.ErrProcessorFailed)
}

func sha256Of(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// The parent records a rejection as a stable error code on the version, so the
// code has to survive as data. Recovering it by matching on error text would
// bind the database to the wording of a message.
func TestRealProcessorRejectionsCarryTheirReasonCode(t *testing.T) {
	cases := map[string]struct {
		file     string
		declared domain.MediaContentType
		want     string
	}{
		"dimension bomb": {"hostile/png-dimension-bomb.png", domain.MediaContentTypePNG, "IMAGE_DIMENSIONS_EXCEEDED"},
		"type mismatch":  {"hostile/jpeg-bytes-named.png", domain.MediaContentTypePNG, "MEDIA_TYPE_UNSUPPORTED"},
		"truncated png":  {"hostile/png-truncated.png", domain.MediaContentTypePNG, "INVALID_IMAGE"},
	}

	supervisor := realSupervisor(t)
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := supervisor.Process(context.Background(), requestFor(t, testCase.file, testCase.declared))

			if code := processing.ReasonCode(err); code != testCase.want {
				t.Errorf("ReasonCode = %q, want %q", code, testCase.want)
			}
		})
	}
}

// Anything that is not a rejection has no reason code to report, and must not
// invent one.
func TestReasonCodeIsEmptyForNonRejections(t *testing.T) {
	if code := processing.ReasonCode(nil); code != "" {
		t.Errorf("ReasonCode(nil) = %q, want empty", code)
	}
	if code := processing.ReasonCode(processing.ErrProcessorFailed); code != "" {
		t.Errorf("ReasonCode(ErrProcessorFailed) = %q, want empty", code)
	}
}
