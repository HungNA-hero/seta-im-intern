package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/processing"
)

var processorBinary string

func TestMain(main *testing.M) {
	buildDir, err := os.MkdirTemp("", "media-processor-cmd")
	if err != nil {
		panic(err)
	}
	binary := filepath.Join(buildDir, "media-processor")
	if output, buildErr := exec.Command("go", "build", "-o", binary, "seta-im-intern/go-asset-core/cmd/media-processor").CombinedOutput(); buildErr != nil {
		panic(fmt.Sprintf("build media-processor: %v\n%s", buildErr, output))
	}
	processorBinary = binary

	code := main.Run()
	_ = os.RemoveAll(buildDir)
	os.Exit(code)
}

type childResult struct {
	exitCode int
	messages []processing.Message
	stdout   string
	stderr   string
}

func runChild(t *testing.T, request processing.Request) childResult {
	t.Helper()

	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	command := exec.Command(processorBinary)
	command.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	runErr := command.Run()
	exitCode := 0
	var exitError *exec.ExitError
	if runErr != nil {
		if !asExitError(runErr, &exitError) {
			t.Fatalf("run processor: %v", runErr)
		}
		exitCode = exitError.ExitCode()
	}

	result := childResult{exitCode: exitCode, stdout: stdout.String(), stderr: stderr.String()}
	for _, line := range strings.Split(strings.TrimSpace(result.stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message processing.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("stdout line is not protocol JSON: %q", line)
		}
		result.messages = append(result.messages, message)
	}
	return result
}

func asExitError(err error, target **exec.ExitError) bool {
	if converted, ok := err.(*exec.ExitError); ok {
		*target = converted
		return true
	}
	return false
}

func stagedRequest(t *testing.T, corpusFile string, declared domain.MediaContentType) processing.Request {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", corpusFile))
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

func TestChildEmitsExactlyValidatedThenCompletedOnSuccess(t *testing.T) {
	request := stagedRequest(t, "valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG)

	result := runChild(t, request)

	if result.exitCode != processing.ExitSuccess {
		t.Fatalf("exit = %d, want %d; stderr=%q", result.exitCode, processing.ExitSuccess, result.stderr)
	}
	if len(result.messages) != 2 {
		t.Fatalf("emitted %d messages, want exactly validated then completed", len(result.messages))
	}
	if result.messages[0].Stage != processing.StageValidated {
		t.Errorf("first stage = %q, want validated", result.messages[0].Stage)
	}
	if result.messages[1].Stage != processing.StageCompleted {
		t.Errorf("second stage = %q, want completed", result.messages[1].Stage)
	}
	if len(result.messages[1].Artifacts) != len(domain.MediaOutputManifest) {
		t.Fatalf("artifacts = %d, want %d", len(result.messages[1].Artifacts), len(domain.MediaOutputManifest))
	}
	for _, artifact := range result.messages[1].Artifacts {
		if filepath.Dir(artifact.Path) != request.ScratchDir {
			t.Errorf("artifact %s escaped the scratch directory: %s", artifact.Kind, artifact.Path)
		}
		if _, err := os.Stat(artifact.Path); err != nil {
			t.Errorf("artifact %s is not on disk: %v", artifact.Kind, err)
		}
	}
}

func TestChildRejectsHostileContentWithTheRejectedExitCode(t *testing.T) {
	result := runChild(t, stagedRequest(t, "hostile/jpeg-trailing-payload.jpg", domain.MediaContentTypeJPEG))

	if result.exitCode != processing.ExitRejected {
		t.Fatalf("exit = %d, want %d", result.exitCode, processing.ExitRejected)
	}
	if len(result.messages) != 1 || result.messages[0].Stage != processing.StageRejected {
		t.Fatalf("messages = %+v, want a single rejected line", result.messages)
	}
	if result.messages[0].ReasonCode == "" {
		t.Error("a rejection must name the rule that was broken")
	}
}

// The distinction that matters most in this binary: a full or unwritable disk
// is not a verdict on the image. Reporting it as a rejection would fail a valid
// upload permanently instead of retrying it elsewhere.
func TestChildReportsAnUnwritableScratchAsInternalNotRejected(t *testing.T) {
	request := stagedRequest(t, "valid/small-64x64.png", domain.MediaContentTypePNG)
	readOnlyScratch := t.TempDir()
	staged := filepath.Join(readOnlyScratch, "original")
	source, err := os.ReadFile(request.SourcePath)
	if err != nil {
		t.Fatalf("read staged source: %v", err)
	}
	if err := os.WriteFile(staged, source, 0o400); err != nil {
		t.Fatalf("stage read-only source: %v", err)
	}
	if err := os.Chmod(readOnlyScratch, 0o500); err != nil {
		t.Fatalf("make scratch read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyScratch, 0o700) })
	request.SourcePath = staged
	request.ScratchDir = readOnlyScratch

	result := runChild(t, request)

	if result.exitCode != processing.ExitInternal {
		t.Fatalf("exit = %d, want %d for a storage failure", result.exitCode, processing.ExitInternal)
	}
	// It must have failed while writing, not before: a validated line proves the
	// image itself was fine and only the disk was not.
	if len(result.messages) != 1 || result.messages[0].Stage != processing.StageValidated {
		t.Fatalf("messages = %+v, want validation to have succeeded first", result.messages)
	}
}

func TestChildReportsMissingOrUnusableInputAsInternal(t *testing.T) {
	cases := map[string]processing.Request{
		"missing source file": {
			SourcePath: filepath.Join(t.TempDir(), "absent"),
			ScratchDir: t.TempDir(),
			MaxSidePx:  20_000, MaxPixels: 50_000_000, Outputs: domain.MediaOutputManifest,
		},
		"source is a directory": {
			SourcePath: t.TempDir(),
			ScratchDir: t.TempDir(),
			MaxSidePx:  20_000, MaxPixels: 50_000_000, Outputs: domain.MediaOutputManifest,
		},
	}

	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			result := runChild(t, request)
			if result.exitCode != processing.ExitInternal {
				t.Errorf("exit = %d, want %d", result.exitCode, processing.ExitInternal)
			}
		})
	}
}

func TestChildRefusesAMalformedRequest(t *testing.T) {
	command := exec.Command(processorBinary)
	command.Stdin = strings.NewReader("this is not a request")
	output, err := command.CombinedOutput()

	var exitError *exec.ExitError
	if !asExitError(err, &exitError) {
		t.Fatalf("a malformed request must fail the child, got %v", err)
	}
	if exitError.ExitCode() != processing.ExitInternal {
		t.Errorf("exit = %d, want %d", exitError.ExitCode(), processing.ExitInternal)
	}
	if len(bytes.TrimSpace(output)) != 0 && bytes.Contains(output, []byte("\"stage\"")) {
		t.Error("a malformed request must not produce a protocol verdict")
	}
}

func TestChildEnforcesDimensionAndAreaLimits(t *testing.T) {
	cases := map[string]struct {
		maxSidePx int
		maxPixels int64
	}{
		"side limit": {maxSidePx: 64, maxPixels: 50_000_000},
		"area limit": {maxSidePx: 20_000, maxPixels: 1024},
	}

	for name, limits := range cases {
		t.Run(name, func(t *testing.T) {
			request := stagedRequest(t, "valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG)
			request.MaxSidePx = limits.maxSidePx
			request.MaxPixels = limits.maxPixels

			result := runChild(t, request)

			if result.exitCode != processing.ExitRejected {
				t.Fatalf("exit = %d, want the image rejected", result.exitCode)
			}
			if code := result.messages[0].ReasonCode; code != "IMAGE_DIMENSIONS_EXCEEDED" {
				t.Errorf("reasonCode = %q, want IMAGE_DIMENSIONS_EXCEEDED", code)
			}
		})
	}
}

// Nothing the child writes to stderr may reach the protocol stream: the parent
// parses stdout only, and a diagnostic that leaked into it would be attacker
// influence over a machine-read channel.
func TestChildKeepsDiagnosticsOffTheProtocolStream(t *testing.T) {
	result := runChild(t, stagedRequest(t, "hostile/not-an-image.png", domain.MediaContentTypePNG))

	for _, line := range strings.Split(strings.TrimSpace(result.stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var message processing.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("non-protocol content on stdout: %q", line)
		}
	}
}
