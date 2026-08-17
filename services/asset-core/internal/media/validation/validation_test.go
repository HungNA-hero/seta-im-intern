package validation_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/validation"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "media", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func testLimits() validation.Limits {
	return validation.Limits{MaxSidePx: 20_000, MaxPixels: 50_000_000}
}

func TestAdmitReportsTheTrueTypeAndDimensions(t *testing.T) {
	cases := map[string]struct {
		file          string
		declared      domain.MediaContentType
		width, height int
	}{
		"landscape jpeg": {"valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG, 2048, 1152},
		"portrait png":   {"valid/portrait-1200x1600.png", domain.MediaContentTypePNG, 1200, 1600},
		"extreme aspect": {"valid/wide-4000x10.png", domain.MediaContentTypePNG, 4000, 10},
		"one pixel":      {"boundary/one-pixel.png", domain.MediaContentTypePNG, 1, 1},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			report, err := validation.Admit(fixture(t, testCase.file), testCase.declared, testLimits())
			if err != nil {
				t.Fatalf("Admit: %v", err)
			}

			if report.DetectedType != testCase.declared {
				t.Errorf("detected = %q, want %q", report.DetectedType, testCase.declared)
			}
			if report.Width != testCase.width || report.Height != testCase.height {
				t.Errorf("dimensions = %dx%d, want %dx%d", report.Width, report.Height, testCase.width, testCase.height)
			}
		})
	}
}

// The standard Go decoders stop at the first image and do not require EOF, so
// they accept appended payloads and concatenated images on their own. Every one
// of these must be refused.
func TestAdmitRejectsTheHostileCorpus(t *testing.T) {
	cases := map[string]struct {
		file     string
		declared domain.MediaContentType
	}{
		"jpeg with a second image appended": {"hostile/jpeg-concatenated.jpg", domain.MediaContentTypeJPEG},
		"jpeg with payload after EOI":       {"hostile/jpeg-trailing-payload.jpg", domain.MediaContentTypeJPEG},
		"jpeg with no EOI":                  {"hostile/jpeg-no-eoi.jpg", domain.MediaContentTypeJPEG},
		"jpeg truncated mid-stream":         {"hostile/jpeg-truncated.jpg", domain.MediaContentTypeJPEG},
		"jpeg signature only":               {"hostile/jpeg-signature-only.jpg", domain.MediaContentTypeJPEG},
		"png with a second image appended":  {"hostile/png-concatenated.png", domain.MediaContentTypePNG},
		"png with payload after IEND":       {"hostile/png-trailing-payload.png", domain.MediaContentTypePNG},
		"png with no IEND":                  {"hostile/png-no-iend.png", domain.MediaContentTypePNG},
		"png truncated mid-stream":          {"hostile/png-truncated.png", domain.MediaContentTypePNG},
		"png signature only":                {"hostile/png-signature-only.png", domain.MediaContentTypePNG},
		"png with a corrupt chunk CRC":      {"hostile/png-bad-crc.png", domain.MediaContentTypePNG},
		"animated png":                      {"hostile/png-animated.apng.png", domain.MediaContentTypePNG},
		"not an image at all":               {"hostile/not-an-image.png", domain.MediaContentTypePNG},
		"a single byte":                     {"boundary/one-byte.bin", domain.MediaContentTypePNG},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := validation.Admit(fixture(t, testCase.file), testCase.declared, testLimits()); err == nil {
				t.Fatal("hostile content was admitted")
			}
		})
	}
}

// A decompression bomb declares enormous dimensions in a tiny header. It must
// be refused from the header alone, since decoding it is the attack.
func TestAdmitRejectsADimensionBomb(t *testing.T) {
	data := fixture(t, "hostile/png-dimension-bomb.png")

	_, err := validation.Admit(data, domain.MediaContentTypePNG, testLimits())

	if !errors.Is(err, validation.ErrDimensionsExceeded) {
		t.Fatalf("error = %v, want %v", err, validation.ErrDimensionsExceeded)
	}
}

// The area limit is separate from the side limit: an image can sit inside
// 20,000 pixels on both sides and still decode to far more than 50 megapixels.
func TestAdmitRejectsExcessiveAreaWithinTheSideLimit(t *testing.T) {
	data := fixture(t, "valid/landscape-2048x1152.jpg")
	areaOnly := validation.Limits{MaxSidePx: 20_000, MaxPixels: 2048*1152 - 1}

	_, err := validation.Admit(data, domain.MediaContentTypeJPEG, areaOnly)

	if !errors.Is(err, validation.ErrDimensionsExceeded) {
		t.Fatalf("error = %v, want %v", err, validation.ErrDimensionsExceeded)
	}
}

func TestAdmitAcceptsAnImageExactlyOnTheLimits(t *testing.T) {
	data := fixture(t, "valid/landscape-2048x1152.jpg")
	exact := validation.Limits{MaxSidePx: 2048, MaxPixels: 2048 * 1152}

	if _, err := validation.Admit(data, domain.MediaContentTypeJPEG, exact); err != nil {
		t.Fatalf("an image exactly on both limits must be admitted: %v", err)
	}
}

// The declared type is a claim by the client. The bytes are the evidence, and
// when they disagree the upload is rejected rather than reinterpreted.
func TestAdmitRejectsAJPEGDeclaredAsPNG(t *testing.T) {
	data := fixture(t, "hostile/jpeg-bytes-named.png")

	_, err := validation.Admit(data, domain.MediaContentTypePNG, testLimits())

	if !errors.Is(err, validation.ErrTypeMismatch) {
		t.Fatalf("error = %v, want %v", err, validation.ErrTypeMismatch)
	}
}

func fuzzSeed(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("..", "..", "..", "testdata", "media", name))
}

func TestAdmitStaysLinearOnAdversarialInput(t *testing.T) {
	limits := testLimits()
	cases := map[string]struct {
		declared domain.MediaContentType
		data     []byte
	}{
		"jpeg fill byte run": {
			domain.MediaContentTypeJPEG,
			append([]byte{0xff, 0xd8}, bytes.Repeat([]byte{0xff}, 4_000_000)...),
		},
		"jpeg stuffed scan": {
			domain.MediaContentTypeJPEG,
			append([]byte{0xff, 0xd8, 0xff, 0xda, 0x00, 0x02}, bytes.Repeat([]byte{0xff, 0x00}, 2_000_000)...),
		},
		"jpeg restart marker run": {
			domain.MediaContentTypeJPEG,
			append([]byte{0xff, 0xd8, 0xff, 0xda, 0x00, 0x02}, bytes.Repeat([]byte{0xff, 0xd0}, 2_000_000)...),
		},
		"jpeg repeated start of image": {
			domain.MediaContentTypeJPEG,
			bytes.Repeat([]byte{0xff, 0xd8}, 2_000_000),
		},
		"png junk after signature": {
			domain.MediaContentTypePNG,
			append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{'A'}, 4_000_000)...),
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			started := time.Now()
			if _, err := validation.Admit(testCase.data, testCase.declared, limits); err == nil {
				t.Error("adversarial filler was admitted")
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Errorf("scanning %d bytes took %s; the scan must stay linear", len(testCase.data), elapsed)
			}
		})
	}
}
