package rendition_test

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/media/rendition"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "media", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func artifactByKind(t *testing.T, artifacts []rendition.Artifact, kind domain.MediaOutputKind) rendition.Artifact {
	t.Helper()
	for _, artifact := range artifacts {
		if artifact.Kind == kind {
			return artifact
		}
	}
	t.Fatalf("no %s artifact in %d outputs", kind, len(artifacts))
	return rendition.Artifact{}
}

// Expected sizes are worked out by hand from the policy — fit inside the box,
// preserve aspect ratio, never upscale, never round a side to zero — so they
// disagree with the code if the code is wrong.
func TestRenderFitsBothOutputsInsideTheirBoxes(t *testing.T) {
	cases := map[string]struct {
		file                    string
		contentType             domain.MediaContentType
		thumbWidth, thumbHeight int
		webWidth, webHeight     int
	}{
		"landscape 2048x1152": {"valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG, 256, 144, 1080, 608},
		"portrait 1200x1600":  {"valid/portrait-1200x1600.png", domain.MediaContentTypePNG, 192, 256, 810, 1080},
		"exact 1080x1080":     {"valid/exact-1080x1080.png", domain.MediaContentTypePNG, 256, 256, 1080, 1080},
		"extreme 4000x10":     {"valid/wide-4000x10.png", domain.MediaContentTypePNG, 256, 1, 1080, 3},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			artifacts, err := rendition.Render(fixture(t, testCase.file), testCase.contentType, domain.MediaOutputManifest)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if len(artifacts) != 2 {
				t.Fatalf("artifacts = %d, want exactly 2", len(artifacts))
			}

			thumbnail := artifactByKind(t, artifacts, domain.MediaOutputThumbnail)
			if thumbnail.Width != testCase.thumbWidth || thumbnail.Height != testCase.thumbHeight {
				t.Errorf("thumbnail = %dx%d, want %dx%d",
					thumbnail.Width, thumbnail.Height, testCase.thumbWidth, testCase.thumbHeight)
			}

			web := artifactByKind(t, artifacts, domain.MediaOutputWeb)
			if web.Width != testCase.webWidth || web.Height != testCase.webHeight {
				t.Errorf("web = %dx%d, want %dx%d", web.Width, web.Height, testCase.webWidth, testCase.webHeight)
			}
		})
	}
}

func TestRenderKeepsTheAdmittedFormat(t *testing.T) {
	cases := map[string]struct {
		file        string
		contentType domain.MediaContentType
		signature   []byte
	}{
		"jpeg stays jpeg": {"valid/landscape-2048x1152.jpg", domain.MediaContentTypeJPEG, []byte{0xFF, 0xD8, 0xFF}},
		"png stays png":   {"valid/portrait-1200x1600.png", domain.MediaContentTypePNG, []byte{0x89, 'P', 'N', 'G'}},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			artifacts, err := rendition.Render(fixture(t, testCase.file), testCase.contentType, domain.MediaOutputManifest)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			for _, artifact := range artifacts {
				if !bytes.HasPrefix(artifact.Bytes, testCase.signature) {
					t.Errorf("%s is not encoded as %s", artifact.Kind, testCase.contentType)
				}
				if artifact.ContentType != testCase.contentType {
					t.Errorf("%s content type = %q, want %q", artifact.Kind, artifact.ContentType, testCase.contentType)
				}
			}
		})
	}
}

// Outputs are encoded from the pixel matrix, so text chunks, colour profiles,
// and anything else the source container carried must be gone. This is a
// sanitization guarantee, not an incidental property of the encoder.
func TestRenderCarriesNoSourceMetadata(t *testing.T) {
	source := pngWithMetadata(t)
	if !bytes.Contains(source, []byte("tEXt")) || !bytes.Contains(source, []byte("iCCP")) {
		t.Fatal("the fixture must actually contain the metadata being tested for")
	}

	artifacts, err := rendition.Render(source, domain.MediaContentTypePNG, domain.MediaOutputManifest)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, artifact := range artifacts {
		for _, forbidden := range []string{"tEXt", "iCCP", "secret-caption", "private-profile"} {
			if bytes.Contains(artifact.Bytes, []byte(forbidden)) {
				t.Errorf("%s still carries %q from the source", artifact.Kind, forbidden)
			}
		}
	}
}

// pngWithMetadata builds a valid PNG carrying a text chunk and a colour profile
// chunk, inserted before IEND.
func pngWithMetadata(t *testing.T) []byte {
	t.Helper()

	base := fixture(t, "valid/small-64x64.png")
	terminator := bytes.LastIndex(base, []byte("IEND"))
	if terminator < 4 {
		t.Fatal("fixture has no IEND chunk")
	}
	insertAt := terminator - 4 // the IEND chunk's own length field

	withMetadata := make([]byte, 0, len(base)+64)
	withMetadata = append(withMetadata, base[:insertAt]...)
	withMetadata = append(withMetadata, pngChunk("tEXt", []byte("Comment\x00secret-caption"))...)
	withMetadata = append(withMetadata, pngChunk("iCCP", []byte("private-profile\x00\x00"))...)
	withMetadata = append(withMetadata, base[insertAt:]...)
	return withMetadata
}

func pngChunk(chunkType string, payload []byte) []byte {
	body := append([]byte(chunkType), payload...)

	chunk := make([]byte, 0, len(body)+8)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(payload)))
	chunk = append(chunk, body...)
	return binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(body))
}

// An image smaller than both boxes is copied at its own size. Upscaling would
// invent detail that was never uploaded.
func TestRenderNeverUpscales(t *testing.T) {
	artifacts, err := rendition.Render(fixture(t, "valid/small-64x64.png"), domain.MediaContentTypePNG, domain.MediaOutputManifest)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, artifact := range artifacts {
		if artifact.Width != 64 || artifact.Height != 64 {
			t.Errorf("%s = %dx%d, want 64x64", artifact.Kind, artifact.Width, artifact.Height)
		}
	}
}
