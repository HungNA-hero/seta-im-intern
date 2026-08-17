// Command media-processor decodes one untrusted image and writes its
// derivatives, then exits. It is a separate binary because a context cannot
// stop a CPU-bound decoder: the only enforceable deadline is a killable
// process.
//
// It contains no networking, no credentials, and no database access. Everything
// it is trusted with arrives as one JSON request on stdin — two local paths and
// numeric policy — and everything it reports leaves as JSON lines on stdout.
package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"seta-im-intern/go-asset-core/internal/media/processing"
	"seta-im-intern/go-asset-core/internal/media/rendition"
	"seta-im-intern/go-asset-core/internal/media/validation"
)

func main() {
	os.Exit(run())
}

func run() int {
	var request processing.Request
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		return processing.ExitInternal
	}

	source, err := os.ReadFile(request.SourcePath)
	if err != nil {
		return processing.ExitInternal
	}

	report, err := validation.Admit(source, request.DeclaredType, validation.Limits{
		MaxSidePx: request.MaxSidePx,
		MaxPixels: request.MaxPixels,
	})
	if err != nil {
		emit(processing.Message{Stage: processing.StageRejected, ReasonCode: reasonFor(err)})
		return processing.ExitRejected
	}

	// The content identity is the hash of the complete uploaded bytes, computed
	// here from what was actually downloaded rather than trusted from the client
	// declaration. The parent compares it against the admitted checksum.
	sourceDigest := rendition.Digest(source)

	emit(processing.Message{
		Stage:        processing.StageValidated,
		DetectedType: report.DetectedType,
		Width:        report.Width,
		Height:       report.Height,
		SourceSHA256: hex.EncodeToString(sourceDigest),
	})

	artifacts, err := rendition.Render(source, report.DetectedType, request.Outputs)
	if err != nil {
		emit(processing.Message{Stage: processing.StageRejected, ReasonCode: reasonFor(err)})
		return processing.ExitRejected
	}

	written, err := writeArtifacts(request.ScratchDir, artifacts)
	if err != nil {
		return processing.ExitInternal
	}

	emit(processing.Message{Stage: processing.StageCompleted, Artifacts: written})
	return processing.ExitSuccess
}

// writeArtifacts puts each derivative in the scratch directory under a name
// derived from its kind, so the parent never has to trust a path the child
// invented from anything client-supplied.
func writeArtifacts(scratchDir string, artifacts []rendition.Artifact) ([]processing.Artifact, error) {
	written := make([]processing.Artifact, 0, len(artifacts))

	for _, artifact := range artifacts {
		extension, ok := artifact.ContentType.FileExtension()
		if !ok {
			return nil, fmt.Errorf("no extension for %q", artifact.ContentType)
		}
		path := filepath.Join(scratchDir, string(artifact.Kind)+"."+extension)
		if err := os.WriteFile(path, artifact.Bytes, 0o600); err != nil {
			return nil, err
		}

		written = append(written, processing.Artifact{
			Kind:        artifact.Kind,
			Path:        path,
			ContentType: artifact.ContentType,
			Width:       artifact.Width,
			Height:      artifact.Height,
			SizeBytes:   int64(len(artifact.Bytes)),
			SHA256:      hex.EncodeToString(artifact.SHA256),
		})
	}
	return written, nil
}

// reasonFor maps a rejection to the shared error code vocabulary. The
// underlying parser message never travels with it: a caller learns which rule
// was broken, not which byte broke it.
func reasonFor(err error) string {
	switch {
	case errors.Is(err, validation.ErrDimensionsExceeded):
		return "IMAGE_DIMENSIONS_EXCEEDED"
	case errors.Is(err, validation.ErrTypeMismatch):
		return "MEDIA_TYPE_UNSUPPORTED"
	case errors.Is(err, validation.ErrNotAnImage),
		errors.Is(err, validation.ErrCorruptContainer),
		errors.Is(err, validation.ErrTrailingContent),
		errors.Is(err, validation.ErrUnsupportedFeature):
		return "INVALID_IMAGE"
	default:
		return "MEDIA_PROCESSING_FAILED"
	}
}

func emit(message processing.Message) {
	encoded, err := json.Marshal(message)
	if err != nil {
		return
	}
	fmt.Fprintln(os.Stdout, string(encoded))
}
