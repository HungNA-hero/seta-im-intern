package processing

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

type Budgets struct {
	Validation time.Duration
	Transform  time.Duration
	Total      time.Duration
}

var (
	ErrValidationTimeout  = errors.New("validation budget exhausted")
	ErrTransformTimeout   = errors.New("transformation budget exhausted")
	ErrTotalTimeout       = errors.New("claimed-job budget exhausted")
	ErrProcessorIsolation = errors.New("media processor identity is not isolated from the worker")
)

type ProcessIdentity struct {
	UID uint32
	GID uint32
}

type SupervisorOptions struct {
	ExecutablePath string
	Arguments      []string
	Environment    []string
	Identity       *ProcessIdentity
	Budgets        Budgets
	Logger         *slog.Logger
}

const killGrace = 2 * time.Second

type ChildSupervisor struct {
	options SupervisorOptions
}

func NewChildSupervisor(options SupervisorOptions) *ChildSupervisor {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &ChildSupervisor{options: options}
}

func (supervisor *ChildSupervisor) Process(ctx context.Context, request Request) (Outcome, error) {
	bounded, cancel := context.WithTimeout(ctx, supervisor.options.Budgets.Total)
	defer cancel()
	if err := supervisor.prepareIdentity(request); err != nil {
		return Outcome{}, err
	}

	command := exec.CommandContext(bounded, supervisor.options.ExecutablePath, supervisor.options.Arguments...)
	// A nil Env makes os/exec inherit the parent's environment, which would hand
	// the decoder the storage and database credentials it must never hold.
	command.Env = supervisor.options.Environment
	if command.Env == nil {
		command.Env = []string{}
	}
	if identity := supervisor.options.Identity; identity != nil {
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{
			Uid: identity.UID,
			Gid: identity.GID,
		}}
	}
	command.WaitDelay = killGrace

	stdin, err := command.StdinPipe()
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: opening the processor's input: %w", ErrProcessorFailed, err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: opening the processor's output: %w", ErrProcessorFailed, err)
	}
	command.Stderr = io.Discard

	if err := command.Start(); err != nil {
		return Outcome{}, fmt.Errorf("%w: starting the processor: %w", ErrProcessorFailed, err)
	}

	sendRequest(stdin, request)
	outcome, readErr := supervisor.readUntilTerminal(command, stdout)
	waitErr := command.Wait()

	return supervisor.classify(outcome, readErr, waitErr)
}

func (supervisor *ChildSupervisor) prepareIdentity(request Request) error {
	identity := supervisor.options.Identity
	if identity == nil {
		return nil
	}
	if os.Geteuid() != 0 || identity.UID == 0 || identity.GID == 0 || int(identity.UID) == os.Geteuid() {
		return fmt.Errorf(
			"%w: worker uid=%d, processor uid=%d gid=%d",
			ErrProcessorIsolation,
			os.Geteuid(),
			identity.UID,
			identity.GID,
		)
	}
	if err := os.Chown(request.ScratchDir, int(identity.UID), int(identity.GID)); err != nil {
		return fmt.Errorf("%w: assigning scratch directory ownership: %w", ErrProcessorIsolation, err)
	}
	if err := os.Chown(request.SourcePath, int(identity.UID), int(identity.GID)); err != nil {
		return fmt.Errorf("%w: assigning source ownership: %w", ErrProcessorIsolation, err)
	}
	return nil
}

func sendRequest(stdin io.WriteCloser, request Request) {
	defer func() { _ = stdin.Close() }()
	_ = json.NewEncoder(stdin).Encode(request)
}

func (supervisor *ChildSupervisor) readUntilTerminal(command *exec.Cmd, stdout io.Reader) (Outcome, error) {
	messages := make(chan Message, 4)
	scanFailed := make(chan error, 1)
	go scanMessages(stdout, messages, scanFailed)
	// Every early return abandons the scanner mid-send, so drain until it closes
	// rather than leaving it blocked on a full channel.
	defer func() {
		go func() {
			for range messages {
			}
		}()
	}()

	budgets := supervisor.options.Budgets
	stageTimer := time.NewTimer(budgets.Validation)
	defer stageTimer.Stop()

	var outcome Outcome
	validated := false

	for {
		select {
		case <-stageTimer.C:
			supervisor.kill(command)
			if validated {
				return Outcome{}, ErrTransformTimeout
			}
			return Outcome{}, ErrValidationTimeout

		case err := <-scanFailed:
			return Outcome{}, err

		case message, open := <-messages:
			if !open {
				return Outcome{}, errNoTerminalMessage
			}
			switch message.Stage {
			case StageValidated:
				validated = true
				outcome.DetectedType = message.DetectedType
				outcome.SourceWidth = message.Width
				outcome.SourceHeight = message.Height
				digest, err := hex.DecodeString(message.SourceSHA256)
				if err != nil {
					return Outcome{}, fmt.Errorf("%w: unusable source digest", errUnusableMessage)
				}
				outcome.SourceSHA256 = digest

				stageTimer.Stop()
				stageTimer.Reset(budgets.Transform)

			case StageCompleted:
				outcome.Artifacts = message.Artifacts
				return outcome, nil

			case StageRejected:
				return Outcome{}, &Rejection{Code: safeReasonCode(message.ReasonCode)}

			default:
				return Outcome{}, fmt.Errorf("%w: unknown stage", errUnusableMessage)
			}
		}
	}
}

func scanMessages(stdout io.Reader, messages chan<- Message, scanFailed chan<- error) {
	defer close(messages)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, maxMessageBytes), maxMessageBytes)

	for scanner.Scan() {
		var message Message
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			scanFailed <- fmt.Errorf("%w: %w", errUnusableMessage, err)
			return
		}
		messages <- message
	}
	if err := scanner.Err(); err != nil {
		scanFailed <- fmt.Errorf("%w: %w", errUnusableMessage, err)
	}
}

func (supervisor *ChildSupervisor) classify(outcome Outcome, readErr, waitErr error) (Outcome, error) {
	switch {
	case readErr == nil && waitErr == nil:
		return outcome, nil

	case errors.Is(readErr, ErrValidationTimeout), errors.Is(readErr, ErrTransformTimeout):
		return Outcome{}, &Rejection{Code: "MEDIA_PROCESSING_TIMEOUT", cause: readErr}

	case errors.Is(readErr, ErrRejected):
		return Outcome{}, readErr

	case readErr != nil:
		supervisor.options.Logger.Warn("media processor produced an unusable result", "error", readErr.Error())
		return Outcome{}, fmt.Errorf("%w: %w", ErrProcessorFailed, readErr)

	default:
		if exitedRejecting(waitErr) {
			return Outcome{}, &Rejection{Code: "MEDIA_PROCESSING_FAILED", cause: waitErr}
		}
		return Outcome{}, fmt.Errorf("%w: %w", ErrProcessorFailed, waitErr)
	}
}

func exitedRejecting(waitErr error) bool {
	var exitErr *exec.ExitError
	return errors.As(waitErr, &exitErr) && exitErr.ExitCode() == ExitRejected
}

func (supervisor *ChildSupervisor) kill(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}

func safeReasonCode(reasonCode string) string {
	const maxReasonCodeLength = 64
	if len(reasonCode) > maxReasonCodeLength {
		return reasonCode[:maxReasonCodeLength]
	}
	if reasonCode == "" {
		return "MEDIA_PROCESSING_FAILED"
	}
	return reasonCode
}

const maxMessageBytes = 64 * 1024

var (
	errNoTerminalMessage = errors.New("processor exited without a terminal message")
	errUnusableMessage   = errors.New("processor emitted an unusable message")
)
