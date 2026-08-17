package processing_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/processing"
)

// The supervisor is tested against a real child process, because the behavior
// under test — deadlines, killing, exit-code classification — only exists when
// there is a process to kill. The child is this test binary re-invoked with
// helperScriptVar set, the standard os/exec testing idiom.
const (
	helperScriptVar   = "MEDIA_PROCESSOR_HELPER_SCRIPT"
	helperArgument    = "-test.run=TestHelperProcess"
	helperEnableVar   = "GO_WANT_HELPER_PROCESS=1"
	testValidationCap = time.Second
	testTransformCap  = time.Second
	testTotalCap      = 3 * time.Second
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	// The child always consumes its request first, exactly as the real one does.
	var request processing.Request
	_ = json.NewDecoder(os.Stdin).Decode(&request)

	switch os.Getenv(helperScriptVar) {
	case "succeed":
		emit(processing.Message{
			Stage: processing.StageValidated, DetectedType: domain.MediaContentTypePNG,
			Width: 64, Height: 64, SourceSHA256: hex.EncodeToString(make([]byte, 32)),
		})
		emit(processing.Message{Stage: processing.StageCompleted, Artifacts: []processing.Artifact{
			{Kind: domain.MediaOutputThumbnail, Path: "/tmp/thumb.png", ContentType: domain.MediaContentTypePNG, Width: 64, Height: 64, SizeBytes: 10, SHA256: hex.EncodeToString(make([]byte, 32))},
			{Kind: domain.MediaOutputWeb, Path: "/tmp/web.png", ContentType: domain.MediaContentTypePNG, Width: 64, Height: 64, SizeBytes: 20, SHA256: hex.EncodeToString(make([]byte, 32))},
		}})
		os.Exit(processing.ExitSuccess)

	case "reject":
		emit(processing.Message{Stage: processing.StageRejected, ReasonCode: "INVALID_IMAGE"})
		os.Exit(processing.ExitRejected)

	case "crash":
		fmt.Fprintln(os.Stderr, "out of memory")
		os.Exit(processing.ExitInternal)

	case "hang-before-validating":
		time.Sleep(time.Minute)
		os.Exit(processing.ExitSuccess)

	case "hang-after-validating":
		emit(processing.Message{
			Stage: processing.StageValidated, DetectedType: domain.MediaContentTypePNG,
			Width: 64, Height: 64, SourceSHA256: hex.EncodeToString(make([]byte, 32)),
		})
		time.Sleep(time.Minute)
		os.Exit(processing.ExitSuccess)

	case "garbage":
		fmt.Fprintln(os.Stdout, "this is not the protocol")
		os.Exit(processing.ExitSuccess)

	case "record-request":
		encoded, _ := json.Marshal(request)
		_ = os.WriteFile(os.Getenv("MEDIA_PROCESSOR_HELPER_ECHO"), encoded, 0o600)
		emit(processing.Message{
			Stage: processing.StageValidated, DetectedType: domain.MediaContentTypePNG,
			Width: 1, Height: 1, SourceSHA256: hex.EncodeToString(make([]byte, 32)),
		})
		emit(processing.Message{Stage: processing.StageCompleted})
		os.Exit(processing.ExitSuccess)
	}
}

func emit(message processing.Message) {
	encoded, _ := json.Marshal(message)
	fmt.Fprintln(os.Stdout, string(encoded))
}

func supervisorRunning(t *testing.T, script string, environment ...string) *processing.ChildSupervisor {
	t.Helper()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate the test binary: %v", err)
	}
	return processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: executable,
		Arguments:      []string{helperArgument},
		Environment:    append([]string{helperEnableVar, helperScriptVar + "=" + script}, environment...),
		Budgets: processing.Budgets{
			Validation: testValidationCap,
			Transform:  testTransformCap,
			Total:      testTotalCap,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func testRequest(t *testing.T) processing.Request {
	t.Helper()
	scratch := t.TempDir()
	source := scratch + "/original.png"
	if err := os.WriteFile(source, []byte("not really an image"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	return processing.Request{
		SourcePath:   source,
		ScratchDir:   scratch,
		DeclaredType: domain.MediaContentTypePNG,
		MaxSidePx:    20_000,
		MaxPixels:    50_000_000,
		Outputs:      domain.MediaOutputManifest,
	}
}

func TestProcessReturnsTheChildsOutcome(t *testing.T) {
	outcome, err := supervisorRunning(t, "succeed").Process(context.Background(), testRequest(t))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if outcome.DetectedType != domain.MediaContentTypePNG {
		t.Errorf("detected type = %q, want image/png", outcome.DetectedType)
	}
	if outcome.SourceWidth != 64 || outcome.SourceHeight != 64 {
		t.Errorf("source = %dx%d, want 64x64", outcome.SourceWidth, outcome.SourceHeight)
	}
	if len(outcome.Artifacts) != 2 {
		t.Fatalf("artifacts = %d, want 2", len(outcome.Artifacts))
	}
	if len(outcome.SourceSHA256) != 32 {
		t.Errorf("source hash = %d bytes, want 32", len(outcome.SourceSHA256))
	}
}

// A deterministic rejection must never be retried: the same bytes reach the
// same verdict, so retrying only burns the attempt budget.
func TestProcessClassifiesAnImageRejectionAsDeterministic(t *testing.T) {
	_, err := supervisorRunning(t, "reject").Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrRejected) {
		t.Fatalf("error = %v, want %v", err, processing.ErrRejected)
	}
	if errors.Is(err, processing.ErrProcessorFailed) {
		t.Error("a rejected image must not be reported as an infrastructure failure")
	}
}

// The mirror case: a child killed by the OS says nothing about the image, and
// treating it as a rejection would destroy a valid upload permanently.
func TestProcessClassifiesAChildCrashAsRetryable(t *testing.T) {
	_, err := supervisorRunning(t, "crash").Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrProcessorFailed) {
		t.Fatalf("error = %v, want %v", err, processing.ErrProcessorFailed)
	}
	if errors.Is(err, processing.ErrRejected) {
		t.Error("a crashed child must not be reported as a rejected image")
	}
}

func TestProcessRejectsAChildThatSpeaksNonsense(t *testing.T) {
	_, err := supervisorRunning(t, "garbage").Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrProcessorFailed) {
		t.Fatalf("error = %v, want %v", err, processing.ErrProcessorFailed)
	}
}

// A context cannot stop a stuck decoder, so the supervisor must kill. The
// timeout is deterministic — the same bytes will exhaust the same budget on the
// next worker — so it is a rejection, not a retryable failure.
func TestProcessKillsAChildThatExceedsItsValidationBudget(t *testing.T) {
	start := time.Now()

	_, err := supervisorRunning(t, "hang-before-validating").Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrRejected) {
		t.Fatalf("error = %v, want %v", err, processing.ErrRejected)
	}
	if !errors.Is(err, processing.ErrValidationTimeout) {
		t.Errorf("error = %v, want it to name the validation budget", err)
	}
	if elapsed := time.Since(start); elapsed > testTotalCap {
		t.Errorf("took %s; the validation budget of %s should have fired first", elapsed, testValidationCap)
	}
}

// The transformation budget starts when the child reports validation done, so
// it cannot be expressed as a plain context deadline.
func TestProcessKillsAChildThatExceedsItsTransformBudget(t *testing.T) {
	_, err := supervisorRunning(t, "hang-after-validating").Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrRejected) {
		t.Fatalf("error = %v, want %v", err, processing.ErrRejected)
	}
	if !errors.Is(err, processing.ErrTransformTimeout) {
		t.Errorf("error = %v, want it to name the transformation budget", err)
	}
}

// Nothing client-controlled may reach the child: no URL, credential, object
// key, or original filename.
func TestProcessHandsTheChildOnlyPathsAndNumbers(t *testing.T) {
	echoed := t.TempDir() + "/request.json"
	request := testRequest(t)

	_, err := supervisorRunning(t, "record-request", "MEDIA_PROCESSOR_HELPER_ECHO="+echoed).
		Process(context.Background(), request)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	raw, err := os.ReadFile(echoed)
	if err != nil {
		t.Fatalf("read the request the child saw: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	permitted := map[string]bool{
		"sourcePath": true, "scratchDir": true, "declaredType": true,
		"maxSidePx": true, "maxPixels": true, "outputs": true,
	}
	for field := range fields {
		if !permitted[field] {
			t.Errorf("the child was handed an unexpected field %q", field)
		}
	}
	if fields["sourcePath"] != request.SourcePath {
		t.Errorf("sourcePath = %v, want the generated scratch path", fields["sourcePath"])
	}
}

// Guards the invariant that keeps the lease-holding parent unable to decode:
// nothing in this package may reach an image decoder.
func TestProcessingPackageLinksNoImageDecoder(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "seta-im-intern/go-asset-core/internal/media/processing").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	for _, forbidden := range []string{"image/jpeg", "image/png", "golang.org/x/image/draw"} {
		for _, dependency := range splitLines(string(output)) {
			if dependency == forbidden {
				t.Errorf("processing depends on %q; the supervising parent must not link a decoder", forbidden)
			}
		}
	}
}

func splitLines(value string) []string {
	lines := make([]string, 0, 64)
	current := ""
	for _, character := range value {
		if character == '\n' {
			lines = append(lines, current)
			current = ""
			continue
		}
		current += string(character)
	}
	return lines
}

// A deadline kill is deterministic — the same bytes exhaust the same budget on
// the next worker — so it must reach the version row as a code of its own,
// distinguishable from a malformed image.
func TestTimeoutsCarryATimeoutReasonCode(t *testing.T) {
	for name, script := range map[string]string{
		"validation": "hang-before-validating",
		"transform":  "hang-after-validating",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := supervisorRunning(t, script).Process(context.Background(), testRequest(t))

			if code := processing.ReasonCode(err); code != "MEDIA_PROCESSING_TIMEOUT" {
				t.Errorf("ReasonCode = %q, want MEDIA_PROCESSING_TIMEOUT", code)
			}
		})
	}
}

// The child decodes hostile images; the parent holds the database and storage
// credentials. A nil Environment must mean "nothing", not "inherit everything":
// the production wiring passes no environment, and os/exec treats that as
// inheriting the parent's, which would hand the decoder every secret it has.
func TestProcessGivesTheChildNoInheritedEnvironment(t *testing.T) {
	t.Setenv("ASSET_MEDIA_S3_SECRET_ACCESS_KEY", "super-secret-value")
	echoed := filepath.Join(t.TempDir(), "environment.txt")

	supervisor := processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: "/bin/sh",
		Arguments:      []string{"-c", "env > " + echoed + "; exit 2"},
		Budgets:        processing.Budgets{Validation: testValidationCap, Transform: testTransformCap, Total: testTotalCap},
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	_, _ = supervisor.Process(context.Background(), testRequest(t))

	recorded, err := os.ReadFile(echoed)
	if err != nil {
		t.Fatalf("read recorded environment: %v", err)
	}
	if strings.Contains(string(recorded), "super-secret-value") {
		t.Errorf("the child inherited a credential from the parent environment:\n%s", recorded)
	}
}

func TestProcessRefusesAProcessorIdentityMatchingTheWorker(t *testing.T) {
	supervisor := processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: "/bin/true",
		Identity: &processing.ProcessIdentity{
			UID: uint32(os.Geteuid()),
			GID: uint32(os.Getegid()),
		},
		Budgets: processing.Budgets{
			Validation: testValidationCap,
			Transform:  testTransformCap,
			Total:      testTotalCap,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	_, err := supervisor.Process(context.Background(), testRequest(t))

	if !errors.Is(err, processing.ErrProcessorIsolation) {
		t.Fatalf("error = %v, want %v", err, processing.ErrProcessorIsolation)
	}
}

func TestProcessIdentityCannotReadTheWorkersProcEnvironment(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing child identity requires the production worker's root launcher")
	}

	scratch, err := os.MkdirTemp("", "media-isolation-")
	if err != nil {
		t.Fatalf("create scratch: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	source := filepath.Join(scratch, "original.png")
	if err := os.WriteFile(source, []byte("not really an image"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	request := processing.Request{
		SourcePath:   source,
		ScratchDir:   scratch,
		DeclaredType: domain.MediaContentTypePNG,
		MaxSidePx:    20_000,
		MaxPixels:    50_000_000,
		Outputs:      domain.MediaOutputManifest,
	}
	statusPath := filepath.Join(request.ScratchDir, "proc-access")
	supervisor := processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: "/bin/sh",
		Arguments: []string{
			"-c",
			`if tr '\0' '\n' < /proc/$PPID/environ >/dev/null 2>/dev/null; then echo readable > "$1"; else echo blocked > "$1"; fi; exit 2`,
			"media-processor",
			statusPath,
		},
		Identity: &processing.ProcessIdentity{UID: 65534, GID: 65534},
		Budgets: processing.Budgets{
			Validation: testValidationCap,
			Transform:  testTransformCap,
			Total:      testTotalCap,
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	_, processErr := supervisor.Process(context.Background(), request)

	status, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read child probe after Process returned %v: %v", processErr, err)
	}
	if strings.TrimSpace(string(status)) != "blocked" {
		t.Fatalf("processor /proc access = %q, want blocked", status)
	}
}
