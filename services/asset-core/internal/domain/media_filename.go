package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var ErrInvalidDisplayFilename = errors.New("invalid display filename")

const maxDisplayFilenameRunes = 255

var separatorRunes = []rune{
	'/',
	'\\',
	'⁄',
	'∕',
	'／',
	'＼',
	'⧵',
	'∖',
	'﹨',
}

var directionRunes = []rune{
	'‎',
	'‏',
	'‪',
	'‫',
	'‬',
	'‭',
	'‮',
	'⁦',
	'⁧',
	'⁨',
	'⁩',
}

func ValidateDisplayFilename(filename string) (string, error) {
	if !utf8.ValidString(filename) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidDisplayFilename)
	}

	normalized := strings.TrimSpace(norm.NFC.String(filename))
	if normalized == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidDisplayFilename)
	}
	if utf8.RuneCountInString(normalized) > maxDisplayFilenameRunes {
		return "", fmt.Errorf("%w: longer than %d characters", ErrInvalidDisplayFilename, maxDisplayFilenameRunes)
	}

	for _, character := range normalized {
		if err := admitFilenameRune(character); err != nil {
			return "", err
		}
	}
	if err := admitFilenameShape(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

func admitFilenameRune(character rune) error {
	switch {
	case character == 0:
		return fmt.Errorf("%w: contains NUL", ErrInvalidDisplayFilename)
	case unicode.IsControl(character):
		return fmt.Errorf("%w: contains a control character", ErrInvalidDisplayFilename)
	case containsRune(separatorRunes, character):
		return fmt.Errorf("%w: contains a path separator", ErrInvalidDisplayFilename)
	case containsRune(directionRunes, character):
		return fmt.Errorf("%w: contains a direction override", ErrInvalidDisplayFilename)
	case unicode.In(character, unicode.Cf, unicode.Co, unicode.Cs):
		return fmt.Errorf("%w: contains a formatting or unassigned character", ErrInvalidDisplayFilename)
	}
	return nil
}

func admitFilenameShape(normalized string) error {
	if strings.Trim(normalized, ".") == "" {
		return fmt.Errorf("%w: names only the current or parent directory", ErrInvalidDisplayFilename)
	}
	if strings.HasPrefix(normalized, ".") {
		return fmt.Errorf("%w: has no name before its extension", ErrInvalidDisplayFilename)
	}
	if strings.Contains(normalized, "..") {
		return fmt.Errorf("%w: contains a traversal sequence", ErrInvalidDisplayFilename)
	}
	if strings.ContainsRune(normalized, ':') {
		return fmt.Errorf("%w: contains a device or stream separator", ErrInvalidDisplayFilename)
	}
	return nil
}

func containsRune(set []rune, character rune) bool {
	for _, candidate := range set {
		if candidate == character {
			return true
		}
	}
	return false
}
