// Package rendition turns admitted image bytes into the fixed set of
// derivatives every completed version owns. It is pure: bytes in, bytes out,
// with no storage, database, clock, or context. Deciding whether the input is
// admissible at all belongs to internal/media/validation, which runs first.
package rendition

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"seta-im-intern/go-asset-core/internal/domain"

	"golang.org/x/image/draw"
)

var (
	ErrDecodeFailed       = errors.New("admitted image could not be decoded")
	ErrUnsupportedType    = errors.New("unsupported output content type")
	ErrFormatNotPreserved = errors.New("decoded format does not match the admitted content type")
)

const (
	webJPEGQuality       = 85
	thumbnailJPEGQuality = 80
)

type Artifact struct {
	Kind          domain.MediaOutputKind
	ContentType   domain.MediaContentType
	Width, Height int
	Bytes         []byte
	SHA256        []byte
}

func Digest(data []byte) []byte {
	digest := sha256.Sum256(data)
	return digest[:]
}

func Render(data []byte, contentType domain.MediaContentType, policies []domain.MediaProcessingPolicy) ([]Artifact, error) {
	source, decodedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if err := requireFormat(decodedFormat, contentType); err != nil {
		return nil, err
	}

	artifacts := make([]Artifact, 0, len(policies))
	for _, policy := range policies {
		artifact, err := renderOne(source, contentType, policy)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func renderOne(source image.Image, contentType domain.MediaContentType, policy domain.MediaProcessingPolicy) (Artifact, error) {
	bounds := source.Bounds()
	width, height := fitInside(bounds.Dx(), bounds.Dy(), policy.BoundingBoxPx)

	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.ApproxBiLinear.Scale(resized, resized.Bounds(), source, bounds, draw.Src, nil)

	encoded, err := encode(resized, contentType, policy.Kind)
	if err != nil {
		return Artifact{}, err
	}
	digest := sha256.Sum256(encoded)

	return Artifact{
		Kind:        policy.Kind,
		ContentType: contentType,
		Width:       width,
		Height:      height,
		Bytes:       encoded,
		SHA256:      digest[:],
	}, nil
}

func fitInside(width, height, boundingBox int) (int, int) {
	longestSide := width
	if height > longestSide {
		longestSide = height
	}
	if longestSide <= boundingBox {
		return width, height
	}

	scale := float64(boundingBox) / float64(longestSide)
	return atLeastOnePixel(width, scale), atLeastOnePixel(height, scale)
}

func atLeastOnePixel(side int, scale float64) int {
	scaled := int(math.Round(float64(side) * scale))
	if scaled < 1 {
		return 1
	}
	return scaled
}

func encode(source image.Image, contentType domain.MediaContentType, kind domain.MediaOutputKind) ([]byte, error) {
	var encoded bytes.Buffer

	switch contentType {
	case domain.MediaContentTypeJPEG:
		if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: jpegQualityFor(kind)}); err != nil {
			return nil, err
		}
	case domain.MediaContentTypePNG:
		if err := png.Encode(&encoded, source); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedType, contentType)
	}
	return encoded.Bytes(), nil
}

func jpegQualityFor(kind domain.MediaOutputKind) int {
	if kind == domain.MediaOutputThumbnail {
		return thumbnailJPEGQuality
	}
	return webJPEGQuality
}

func requireFormat(decodedFormat string, contentType domain.MediaContentType) error {
	expected := map[domain.MediaContentType]string{
		domain.MediaContentTypeJPEG: "jpeg",
		domain.MediaContentTypePNG:  "png",
	}[contentType]

	if expected == "" {
		return fmt.Errorf("%w: %q", ErrUnsupportedType, contentType)
	}
	if decodedFormat != expected {
		return fmt.Errorf("%w: decoded %s, admitted %s", ErrFormatNotPreserved, decodedFormat, contentType)
	}
	return nil
}
