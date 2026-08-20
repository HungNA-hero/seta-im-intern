package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/event"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/usecase"
)

type stubRunner struct {
	result error
	calls  []string
}

type stubNotificationVerifier struct {
	err   error
	calls int
}

func (verifier *stubNotificationVerifier) VerifyNotification(context.Context, string, media.Payload) error {
	verifier.calls++
	return verifier.err
}

func (runner *stubRunner) RunJob(_ context.Context, jobID string) error {
	runner.calls = append(runner.calls, jobID)
	return runner.result
}

func testEffect(runner jobRunner) *notificationEffect {
	return &notificationEffect{runner: runner, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

// acceptedEnvelope builds a record the shared consumer would have accepted, so
// the effect is tested on exactly the input it really receives.
func acceptedEnvelope(t *testing.T, payload media.Payload) event.Envelope {
	t.Helper()

	value, err := media.Marshal(event.Envelope{
		EventID:    uuid.NewString(),
		OrgID:      uuid.NewString(),
		OccurredAt: time.Date(2026, time.August, 14, 9, 0, 0, 0, time.UTC),
	}, payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	envelope, err := event.Parse(value, []int{media.SchemaVersion})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return envelope
}

func validJobPayload() media.Payload {
	return media.Payload{
		AssetID:   uuid.NewString(),
		UploadID:  uuid.NewString(),
		VersionID: uuid.NewString(),
		JobID:     uuid.NewString(),
	}
}

func TestApplyRunsTheJobTheNotificationNames(t *testing.T) {
	payload := validJobPayload()
	runner := &stubRunner{}

	if err := testEffect(runner).Apply(context.Background(), acceptedEnvelope(t, payload)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(runner.calls) != 1 || runner.calls[0] != payload.JobID {
		t.Errorf("runner calls = %v, want [%s]", runner.calls, payload.JobID)
	}
}

// Settled work must acknowledge the record. ErrAlreadyApplied is how the shared
// consumer is told to commit the offset.
func TestApplyCommitsTheOffsetForSettledWork(t *testing.T) {
	runner := &stubRunner{result: fmt.Errorf("%w: job-1", usecase.ErrJobSettled)}

	err := testEffect(runner).Apply(context.Background(), acceptedEnvelope(t, validJobPayload()))

	if !errors.Is(err, consume.ErrAlreadyApplied) {
		t.Errorf("error = %v, want %v", err, consume.ErrAlreadyApplied)
	}
}

// Contention must leave the offset uncommitted so the record is redelivered.
func TestApplyLeavesTheOffsetUncommittedWhenTheJobIsUnavailable(t *testing.T) {
	for name, result := range map[string]error{
		"leased elsewhere": fmt.Errorf("%w: %w", usecase.ErrJobUnavailable, repository.ErrJobLeased),
		"lease lost":       fmt.Errorf("%w: job-1", usecase.ErrLeaseLost),
		"exhausted queued": fmt.Errorf("%w: job-1 used 3 of 3", repository.ErrJobExhausted),
		"database down":    errors.New("connection refused"),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &stubRunner{result: result}

			err := testEffect(runner).Apply(context.Background(), acceptedEnvelope(t, validJobPayload()))

			if err == nil {
				t.Fatal("unfinished work must not acknowledge its record")
			}
			if errors.Is(err, consume.ErrAlreadyApplied) {
				t.Errorf("error = %v, which would commit the offset", err)
			}
		})
	}
}

func TestApplyQuarantinesANotificationForAMissingJob(t *testing.T) {
	runner := &stubRunner{result: fmt.Errorf("%w: job-1", repository.ErrJobNotFound)}

	err := testEffect(runner).Apply(context.Background(), acceptedEnvelope(t, validJobPayload()))

	if reason, poison := consume.PoisonReason(err); !poison || reason != "MEDIA_JOB_NOT_FOUND" {
		t.Errorf("error = %v, want MEDIA_JOB_NOT_FOUND poison", err)
	}
}

func TestApplyQuarantinesAnUnusablePayloadWithoutRunningAnything(t *testing.T) {
	envelope := acceptedEnvelope(t, validJobPayload())
	envelope.Raw = []byte(`{"eventId":"` + envelope.EventID + `","jobId":"not-a-uuid"}`)
	runner := &stubRunner{}

	err := testEffect(runner).Apply(context.Background(), envelope)

	if reason, poison := consume.PoisonReason(err); !poison || reason != "INVALID_MEDIA_PAYLOAD" {
		t.Errorf("error = %v, want INVALID_MEDIA_PAYLOAD poison", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("runner was called %v for an unusable payload", runner.calls)
	}
}

func TestApplyQuarantinesADatabaseTruthMismatchBeforeRunningAnything(t *testing.T) {
	runner := &stubRunner{}
	verifier := &stubNotificationVerifier{err: repository.ErrNotificationMismatch}
	effect := &notificationEffect{
		runner:   runner,
		verifier: verifier,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := effect.Apply(context.Background(), acceptedEnvelope(t, validJobPayload()))

	if reason, poison := consume.PoisonReason(err); !poison || reason != "MEDIA_NOTIFICATION_MISMATCH" {
		t.Fatalf("error = %v, want MEDIA_NOTIFICATION_MISMATCH poison", err)
	}
	if verifier.calls != 1 || len(runner.calls) != 0 {
		t.Fatalf("verifier calls = %d, runner calls = %v; mismatch must spend no processing attempt", verifier.calls, runner.calls)
	}
}

func TestApplyRetriesATemporaryDatabaseVerificationFailure(t *testing.T) {
	runner := &stubRunner{}
	databaseFailure := errors.New("database unavailable")
	verifier := &stubNotificationVerifier{err: databaseFailure}
	effect := &notificationEffect{
		runner:   runner,
		verifier: verifier,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	err := effect.Apply(context.Background(), acceptedEnvelope(t, validJobPayload()))

	if !errors.Is(err, databaseFailure) || consumeErrIsPoison(err) {
		t.Fatalf("error = %v, want transient database failure", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %v, want none before authoritative verification", runner.calls)
	}
}

func consumeErrIsPoison(err error) bool {
	_, poisoned := consume.PoisonReason(err)
	return poisoned
}

// The whole point of the child process: this binary holds the database lease
// and the storage credentials, so it must be structurally incapable of decoding
// an untrusted image. Wiring the rendition executor in is exactly the change
// that could break that, which is why the assertion lives on the binary.
func TestMediaWorkerLinksNoImageDecoder(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "seta-im-intern/go-asset-core/cmd/media-worker").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	linked := strings.Split(string(output), "\n")
	for _, forbidden := range []string{"image/jpeg", "image/png", "image/gif", "golang.org/x/image/draw"} {
		for _, dependency := range linked {
			if strings.TrimSpace(dependency) == forbidden {
				t.Errorf("media-worker links %q; only the child process may decode images", forbidden)
			}
		}
	}
}
