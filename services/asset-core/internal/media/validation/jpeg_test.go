package validation_test

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/validation"
)

func baselineJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.RGBA{R: uint8(x * 7), G: uint8(y * 5), B: 0x40, A: 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return encoded.Bytes()
}

func admitJPEG(data []byte) (validation.Report, error) {
	return validation.Admit(data, domain.MediaContentTypeJPEG, validation.Limits{MaxSidePx: 20_000, MaxPixels: 50_000_000})
}

func TestJPEGAdmitsAWellFormedBaselineImage(t *testing.T) {
	report, err := admitJPEG(baselineJPEG(t, 32, 24))
	if err != nil {
		t.Fatalf("a well-formed baseline JPEG was rejected: %v", err)
	}
	if report.Width != 32 || report.Height != 24 {
		t.Errorf("dimensions = %dx%d, want 32x24", report.Width, report.Height)
	}
	if report.DetectedType != domain.MediaContentTypeJPEG {
		t.Errorf("detectedType = %q, want image/jpeg", report.DetectedType)
	}
}

func TestJPEGRejectsStructuralAbuse(t *testing.T) {
	valid := baselineJPEG(t, 32, 24)

	cases := map[string]func() []byte{
		"no start of image": func() []byte {
			return append([]byte{0x00, 0x00}, valid[2:]...)
		},
		"truncated mid scan": func() []byte {
			return valid[:len(valid)-8]
		},
		"truncated to the header": func() []byte {
			return valid[:12]
		},
		"trailing payload after the end marker": func() []byte {
			return append(append([]byte{}, valid...), []byte("attacker controlled trailer")...)
		},
		"two images concatenated": func() []byte {
			return append(append([]byte{}, valid...), valid...)
		},
		"segment length below its own header": func() []byte {
			corrupted := append([]byte{}, valid...)
			corrupted[4], corrupted[5] = 0x00, 0x01
			return corrupted
		},
		"segment length past the end of the file": func() []byte {
			corrupted := append([]byte{}, valid...)
			corrupted[4], corrupted[5] = 0xff, 0xf0
			return corrupted
		},
		"empty": func() []byte { return nil },
		"start of image only": func() []byte {
			return []byte{0xff, 0xd8}
		},
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := admitJPEG(build()); err == nil {
				t.Error("hostile JPEG structure was admitted")
			}
		})
	}
}

func TestJPEGAcceptsLegalEntropyCodedConstructs(t *testing.T) {
	restarts := jpegWithRestartMarkers(t)

	report, err := admitJPEG(restarts)
	if err != nil {
		t.Fatalf("a JPEG using restart markers was rejected: %v", err)
	}
	if report.Width == 0 || report.Height == 0 {
		t.Error("a restart-marker JPEG reported no dimensions")
	}
}

func jpegWithRestartMarkers(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewGray(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			canvas.SetGray(x, y, color.Gray{Y: uint8((x ^ y) * 3)})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, canvas, &jpeg.Options{Quality: 75}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return encoded.Bytes()
}

func TestJPEGDeclarationMustMatchTheBytes(t *testing.T) {
	_, err := validation.Admit(baselineJPEG(t, 16, 16), domain.MediaContentTypePNG, validation.Limits{MaxSidePx: 20_000, MaxPixels: 50_000_000})

	if !errors.Is(err, validation.ErrTypeMismatch) {
		t.Fatalf("error = %v, want %v", err, validation.ErrTypeMismatch)
	}
}

// FuzzAdmitJPEG asserts the two properties that make the scanner safe to run on
// hostile input: it always terminates without panicking, and anything it admits
// is genuinely decodable at the dimensions it reported.
func FuzzAdmitJPEG(f *testing.F) {
	for _, name := range []string{
		"valid/landscape-2048x1152.jpg",
		"valid/portrait-1200x1600.jpg",
		"hostile/jpeg-concatenated.jpg",
		"hostile/jpeg-trailing-payload.jpg",
		"hostile/jpeg-truncated.jpg",
		"hostile/not-an-image.png",
	} {
		if data, err := fuzzSeed(name); err == nil {
			f.Add(data)
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		report, err := admitJPEG(data)
		if err != nil {
			return
		}

		if report.DetectedType != domain.MediaContentTypeJPEG {
			t.Fatalf("admitted %q while declared image/jpeg", report.DetectedType)
		}
		config, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			t.Fatalf("admitted bytes that will not decode: %v", decodeErr)
		}
		if format != "jpeg" {
			t.Fatalf("admitted bytes that decode as %q", format)
		}
		if config.Width != report.Width || config.Height != report.Height {
			t.Fatalf("reported %dx%d, decoder says %dx%d", report.Width, report.Height, config.Width, config.Height)
		}
	})
}
