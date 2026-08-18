package validation

import (
	"encoding/binary"
	"fmt"
)

const (
	jpegMarkerPrefix   = 0xFF
	jpegStartOfImage   = 0xD8
	jpegEndOfImage     = 0xD9
	jpegStartOfScan    = 0xDA
	jpegTemporary      = 0x01
	jpegRestartFirst   = 0xD0
	jpegRestartLast    = 0xD7
	jpegStuffedByte    = 0x00
	jpegSegmentMinimum = 2 // the length field counts itself
)

func scanJPEG(data []byte) error {
	if len(data) < 2 || data[0] != jpegMarkerPrefix || data[1] != jpegStartOfImage {
		return fmt.Errorf("%w: missing JPEG start of image", ErrNotAnImage)
	}

	offset := 2
	for {
		marker, next, err := readMarker(data, offset)
		if err != nil {
			return err
		}
		offset = next

		switch {
		case marker == jpegStartOfImage:
			return fmt.Errorf("%w: a second start of image at %d", ErrTrailingContent, offset)

		case marker == jpegEndOfImage:
			if offset != len(data) {
				return fmt.Errorf("%w: %d bytes follow EOI", ErrTrailingContent, len(data)-offset)
			}
			return nil

		case marker == jpegTemporary, marker >= jpegRestartFirst && marker <= jpegRestartLast:
			continue

		default:
			segmentEnd, err := segmentEnd(data, offset, marker)
			if err != nil {
				return err
			}
			offset = segmentEnd
			if marker == jpegStartOfScan {
				offset, err = skipEntropyCodedData(data, offset)
				if err != nil {
					return err
				}
			}
		}
	}
}

func readMarker(data []byte, offset int) (byte, int, error) {
	if offset >= len(data) {
		return 0, 0, fmt.Errorf("%w: expected a marker at end of file", ErrCorruptContainer)
	}
	if data[offset] != jpegMarkerPrefix {
		return 0, 0, fmt.Errorf("%w: expected a marker at %d, found 0x%02X", ErrCorruptContainer, offset, data[offset])
	}
	for offset < len(data) && data[offset] == jpegMarkerPrefix {
		offset++
	}
	if offset >= len(data) {
		return 0, 0, fmt.Errorf("%w: marker prefix at end of file", ErrCorruptContainer)
	}
	return data[offset], offset + 1, nil
}

func segmentEnd(data []byte, offset int, marker byte) (int, error) {
	if offset+2 > len(data) {
		return 0, fmt.Errorf("%w: segment 0x%02X has no length", ErrCorruptContainer, marker)
	}
	length := int(binary.BigEndian.Uint16(data[offset : offset+2]))
	if length < jpegSegmentMinimum {
		return 0, fmt.Errorf("%w: segment 0x%02X declares %d bytes", ErrCorruptContainer, marker, length)
	}
	end := offset + length
	if end > len(data) {
		return 0, fmt.Errorf("%w: segment 0x%02X runs past end of file", ErrCorruptContainer, marker)
	}
	return end, nil
}

func skipEntropyCodedData(data []byte, offset int) (int, error) {
	for offset+1 < len(data) {
		if data[offset] != jpegMarkerPrefix {
			offset++
			continue
		}
		switch following := data[offset+1]; {
		case following == jpegStuffedByte:
			offset += 2
		case following >= jpegRestartFirst && following <= jpegRestartLast:
			offset += 2
		case following == jpegMarkerPrefix:
			offset++
		default:
			return offset, nil
		}
	}
	return 0, fmt.Errorf("%w: entropy-coded scan runs to end of file without a terminator", ErrCorruptContainer)
}
