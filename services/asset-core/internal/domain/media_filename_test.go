package domain

import (
	"strings"
	"testing"
)

func TestValidateDisplayFilenameAcceptsOrdinaryNames(t *testing.T) {
	cases := map[string]string{
		"plain ascii":       "holiday.jpg",
		"spaces and dashes": "Family Holiday - 2026.png",
		"non-latin script":  "ảnh-gia-đình.jpg",
		"emoji":             "sunset \U0001F305.jpg",
		"internal dots":     "report.v2.final.png",
	}

	for name, filename := range cases {
		t.Run(name, func(t *testing.T) {
			normalized, err := ValidateDisplayFilename(filename)
			if err != nil {
				t.Fatalf("ValidateDisplayFilename(%q) = %v, want it accepted", filename, err)
			}
			if normalized == "" {
				t.Error("an accepted filename must come back non-empty")
			}
		})
	}
}

func TestValidateDisplayFilenameRejectsHostileNames(t *testing.T) {
	cases := map[string]string{
		"empty":                   "",
		"blank":                   "   ",
		"nul":                     "photo\x00.jpg",
		"line feed":               "photo\n.jpg",
		"carriage return":         "photo\r.jpg",
		"tab":                     "photo\t.jpg",
		"escape":                  "photo\x1b[31m.jpg",
		"delete":                  "photo\x7f.jpg",
		"c1 control":              "photo.jpg",
		"posix separator":         "dir/photo.jpg",
		"windows separator":       "dir\\photo.jpg",
		"parent traversal":        "..",
		"current directory":       ".",
		"traversal prefix":        "../photo.jpg",
		"fullwidth solidus":       "dir／photo.jpg",
		"fraction slash":          "dir⁄photo.jpg",
		"division slash":          "dir∕photo.jpg",
		"right to left override":  "photo‮gpj.jpg",
		"left to right override":  "photo‭gpj.jpg",
		"right to left embedding": "photo‫gpj.jpg",
		"pop directional":         "photo‬gpj.jpg",
		"right to left isolate":   "photo⁧gpj.jpg",
		"first strong isolate":    "photo⁨gpj.jpg",
		"pop directional isolate": "photo⁩gpj.jpg",
		"right to left mark":      "photo‏gpj.jpg",
		"leading only extension":  ".jpg",
	}

	for name, filename := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ValidateDisplayFilename(filename); err == nil {
				t.Errorf("ValidateDisplayFilename(%q) was accepted, want it rejected", filename)
			}
		})
	}
}

func TestValidateDisplayFilenameNormalizesToComposedForm(t *testing.T) {
	decomposed := "café.jpg"
	composed := "café.jpg"

	fromDecomposed, err := ValidateDisplayFilename(decomposed)
	if err != nil {
		t.Fatalf("decomposed form rejected: %v", err)
	}
	fromComposed, err := ValidateDisplayFilename(composed)
	if err != nil {
		t.Fatalf("composed form rejected: %v", err)
	}

	if fromDecomposed != fromComposed {
		t.Errorf("normalized forms differ: %q vs %q", fromDecomposed, fromComposed)
	}
	if fromDecomposed != composed {
		t.Errorf("normalized = %q, want the composed form %q", fromDecomposed, composed)
	}
}

func TestValidateDisplayFilenameBoundsLengthByRunes(t *testing.T) {
	if _, err := ValidateDisplayFilename(strings.Repeat("é", 256) + ".jpg"); err == nil {
		t.Error("an over-long name was accepted")
	}
	if _, err := ValidateDisplayFilename(strings.Repeat("é", 200) + ".jpg"); err != nil {
		t.Errorf("a 204-rune name was rejected: %v", err)
	}
}

func TestValidateDisplayFilenameRejectsInvalidUTF8(t *testing.T) {
	if _, err := ValidateDisplayFilename("photo\xff\xfe.jpg"); err == nil {
		t.Error("invalid UTF-8 was accepted")
	}
}
