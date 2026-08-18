package processing

import (
	"errors"
	"seta-im-intern/go-asset-core/internal/domain"
)

type Stage string

const (
	StageValidated Stage = "validated"
	StageCompleted Stage = "completed"
	StageRejected  Stage = "rejected"
)

type Request struct {
	SourcePath   string                         `json:"sourcePath"`
	ScratchDir   string                         `json:"scratchDir"`
	DeclaredType domain.MediaContentType        `json:"declaredType"`
	MaxSidePx    int                            `json:"maxSidePx"`
	MaxPixels    int64                          `json:"maxPixels"`
	Outputs      []domain.MediaProcessingPolicy `json:"outputs"`
}

type Artifact struct {
	Kind        domain.MediaOutputKind  `json:"kind"`
	Path        string                  `json:"path"`
	ContentType domain.MediaContentType `json:"contentType"`
	Width       int                     `json:"width"`
	Height      int                     `json:"height"`
	SizeBytes   int64                   `json:"sizeBytes"`
	SHA256      string                  `json:"sha256"`
}

type Message struct {
	Stage        Stage                   `json:"stage"`
	DetectedType domain.MediaContentType `json:"detectedType,omitempty"`
	Width        int                     `json:"width,omitempty"`
	Height       int                     `json:"height,omitempty"`
	SourceSHA256 string                  `json:"sourceSha256,omitempty"`
	Artifacts    []Artifact              `json:"artifacts,omitempty"`
	ReasonCode   string                  `json:"reasonCode,omitempty"`
}

type Outcome struct {
	DetectedType domain.MediaContentType
	SourceWidth  int
	SourceHeight int
	SourceSHA256 []byte
	Artifacts    []Artifact
}

var (
	ErrRejected        = errors.New("image was rejected by the processor")
	ErrProcessorFailed = errors.New("media processor failed")
)

const (
	ExitSuccess  = 0
	ExitRejected = 1
	ExitInternal = 2
)

type Rejection struct {
	Code  string
	cause error
}

func (rejection *Rejection) Error() string {
	if rejection.cause == nil {
		return ErrRejected.Error() + ": " + rejection.Code
	}
	return ErrRejected.Error() + ": " + rejection.Code + ": " + rejection.cause.Error()
}

func (rejection *Rejection) Unwrap() []error {
	if rejection.cause == nil {
		return []error{ErrRejected}
	}
	return []error{ErrRejected, rejection.cause}
}

func ReasonCode(err error) string {
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Code
	}
	return ""
}
