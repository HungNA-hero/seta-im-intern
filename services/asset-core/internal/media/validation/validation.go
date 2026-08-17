package validation

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"seta-im-intern/go-asset-core/internal/domain"
)

var (
	ErrNotAnImage         = errors.New("bytes are not a supported image")
	ErrTypeMismatch       = errors.New("detected image type does not match the admitted declaration")
	ErrDimensionsExceeded = errors.New("image dimensions exceed the configured limits")
	ErrCorruptContainer   = errors.New("image container is malformed or truncated")
	ErrTrailingContent    = errors.New("image is followed by additional content")
	ErrUnsupportedFeature = errors.New("image uses a feature this service does not accept")
)

type Limits struct {
	MaxSidePx int
	MaxPixels int64
}

type Report struct {
	DetectedType  domain.MediaContentType
	Width, Height int
}

var (
	jpegSignature = []byte{0xFF, 0xD8, 0xFF}
	pngSignature  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
)

func Admit(data []byte, declared domain.MediaContentType, limits Limits) (Report, error) {
	detected, err := detectType(data)
	if err != nil {
		return Report{}, err
	}
	if detected != declared {
		return Report{}, fmt.Errorf("%w: declared %s, detected %s", ErrTypeMismatch, declared, detected)
	}

	if err := scanContainer(data, detected); err != nil {
		return Report{}, err
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Report{}, fmt.Errorf("%w: %w", ErrNotAnImage, err)
	}
	if err := limits.admit(config.Width, config.Height); err != nil {
		return Report{}, err
	}
	return Report{DetectedType: detected, Width: config.Width, Height: config.Height}, nil
}

func (limits Limits) admit(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: %dx%d", ErrNotAnImage, width, height)
	}
	if width > limits.MaxSidePx || height > limits.MaxSidePx {
		return fmt.Errorf("%w: %dx%d exceeds %d per side", ErrDimensionsExceeded, width, height, limits.MaxSidePx)
	}
	if pixels := int64(width) * int64(height); pixels > limits.MaxPixels {
		return fmt.Errorf("%w: %d pixels exceeds %d", ErrDimensionsExceeded, pixels, limits.MaxPixels)
	}
	return nil
}

func scanContainer(data []byte, detected domain.MediaContentType) error {
	switch detected {
	case domain.MediaContentTypeJPEG:
		return scanJPEG(data)
	case domain.MediaContentTypePNG:
		return scanPNG(data)
	default:
		return fmt.Errorf("%w: %q", ErrNotAnImage, detected)
	}
}

func detectType(data []byte) (domain.MediaContentType, error) {
	switch {
	case bytes.HasPrefix(data, jpegSignature):
		return domain.MediaContentTypeJPEG, nil
	case bytes.HasPrefix(data, pngSignature):
		return domain.MediaContentTypePNG, nil
	default:
		return "", ErrNotAnImage
	}
}
