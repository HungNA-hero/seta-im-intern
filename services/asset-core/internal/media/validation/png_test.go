package validation_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/validation"
)

func encodedPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	canvas := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			canvas.Set(x, y, color.NRGBA{R: uint8(x * 3), G: uint8(y * 9), B: 0x80, A: 0xff})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return encoded.Bytes()
}

func admitPNG(data []byte) (validation.Report, error) {
	return validation.Admit(data, domain.MediaContentTypePNG, validation.Limits{MaxSidePx: 20_000, MaxPixels: 50_000_000})
}

func pngChunk(chunkType string, payload []byte) []byte {
	chunk := make([]byte, 0, len(payload)+12)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(payload)))
	chunk = append(chunk, chunkType...)
	chunk = append(chunk, payload...)
	return binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(append([]byte(chunkType), payload...)))
}

func insertChunkAfterSignature(data []byte, chunk []byte) []byte {
	const signatureLength = 8
	spliced := append([]byte{}, data[:signatureLength]...)
	spliced = append(spliced, chunk...)
	return append(spliced, data[signatureLength:]...)
}

func TestPNGAdmitsAWellFormedImage(t *testing.T) {
	report, err := admitPNG(encodedPNG(t, 40, 30))
	if err != nil {
		t.Fatalf("a well-formed PNG was rejected: %v", err)
	}
	if report.Width != 40 || report.Height != 30 {
		t.Errorf("dimensions = %dx%d, want 40x30", report.Width, report.Height)
	}
	if report.DetectedType != domain.MediaContentTypePNG {
		t.Errorf("detectedType = %q, want image/png", report.DetectedType)
	}
}

func TestPNGRejectsStructuralAbuse(t *testing.T) {
	valid := encodedPNG(t, 40, 30)

	cases := map[string]func() []byte{
		"corrupted signature": func() []byte {
			corrupted := append([]byte{}, valid...)
			corrupted[1] = 'Q'
			return corrupted
		},
		"header chunk crc flipped": func() []byte {
			corrupted := append([]byte{}, valid...)
			corrupted[29] ^= 0xff
			return corrupted
		},
		"truncated before the end chunk": func() []byte {
			return valid[:len(valid)-12]
		},
		"truncated mid chunk": func() []byte {
			return valid[:len(valid)/2]
		},
		"trailing payload after the end chunk": func() []byte {
			return append(append([]byte{}, valid...), []byte("appended after IEND")...)
		},
		"two images concatenated": func() []byte {
			return append(append([]byte{}, valid...), valid...)
		},
		"chunk length past the end of the file": func() []byte {
			corrupted := append([]byte{}, valid...)
			binary.BigEndian.PutUint32(corrupted[8:12], 0x7fffffff)
			return corrupted
		},
		"animation control chunk": func() []byte {
			animation := make([]byte, 8)
			binary.BigEndian.PutUint32(animation[0:4], 2)
			binary.BigEndian.PutUint32(animation[4:8], 0)
			return insertChunkAfterSignature(valid, pngChunk("acTL", animation))
		},
		"header chunk is not first": func() []byte {
			return insertChunkAfterSignature(valid, pngChunk("tEXt", []byte("Comment\x00displaced")))
		},
		"signature only": func() []byte {
			return valid[:8]
		},
		"empty": func() []byte { return nil },
	}

	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := admitPNG(build()); err == nil {
				t.Error("hostile PNG structure was admitted")
			}
		})
	}
}

func TestPNGAdmitsLegalEncodingVariants(t *testing.T) {
	cases := map[string][]byte{
		"sixteen bit":  sixteenBitPNG(t),
		"interlaced":   interlacedPNG(t),
		"paletted":     palettedPNG(t),
		"with comment": withTextChunk(t, encodedPNG(t, 24, 24)),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := admitPNG(data); err != nil {
				t.Errorf("a legal PNG variant was rejected: %v", err)
			}
		})
	}
}

func sixteenBitPNG(t *testing.T) []byte {
	t.Helper()
	canvas := image.NewNRGBA64(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			canvas.Set(x, y, color.NRGBA64{R: uint16(x * 4096), G: uint16(y * 4096), B: 0x8000, A: 0xffff})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encode 16-bit png: %v", err)
	}
	return encoded.Bytes()
}

func interlacedPNG(t *testing.T) []byte {
	t.Helper()
	source := encodedPNG(t, 32, 32)
	interlaced := append([]byte{}, source...)
	// Byte 12 of the IHDR payload is the interlace method; Adam7 is 1.
	interlaced[8+8+12] = 1
	header := interlaced[12:29]
	binary.BigEndian.PutUint32(interlaced[29:33], crc32.ChecksumIEEE(header))
	return interlaced
}

func palettedPNG(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.NRGBA{A: 0xff}, color.NRGBA{R: 0xff, A: 0xff}, color.NRGBA{B: 0xff, A: 0xff}}
	canvas := image.NewPaletted(image.Rect(0, 0, 20, 20), palette)
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			canvas.SetColorIndex(x, y, uint8((x+y)%len(palette)))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatalf("encode paletted png: %v", err)
	}
	return encoded.Bytes()
}

func withTextChunk(t *testing.T, data []byte) []byte {
	t.Helper()
	const afterHeaderChunk = 8 + 25
	spliced := append([]byte{}, data[:afterHeaderChunk]...)
	spliced = append(spliced, pngChunk("tEXt", []byte("Software\x00seta-dam"))...)
	return append(spliced, data[afterHeaderChunk:]...)
}

// FuzzAdmitPNG asserts the two properties that make the scanner safe to run on
// hostile input: it always terminates without panicking, and anything it admits
// is genuinely decodable at the dimensions it reported.
func FuzzAdmitPNG(f *testing.F) {
	for _, name := range []string{
		"valid/small-64x64.png",
		"hostile/png-concatenated.png",
		"hostile/png-trailing-payload.png",
		"hostile/png-truncated.png",
		"hostile/png-bad-crc.png",
		"hostile/png-animated.apng.png",
		"hostile/png-dimension-bomb.png",
		"hostile/not-an-image.png",
	} {
		if data, err := fuzzSeed(name); err == nil {
			f.Add(data)
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		report, err := admitPNG(data)
		if err != nil {
			return
		}

		if report.DetectedType != domain.MediaContentTypePNG {
			t.Fatalf("admitted %q while declared image/png", report.DetectedType)
		}
		config, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
		if decodeErr != nil {
			t.Fatalf("admitted bytes that will not decode: %v", decodeErr)
		}
		if format != "png" {
			t.Fatalf("admitted bytes that decode as %q", format)
		}
		if config.Width != report.Width || config.Height != report.Height {
			t.Fatalf("reported %dx%d, decoder says %dx%d", report.Width, report.Height, config.Width, config.Height)
		}
	})
}
