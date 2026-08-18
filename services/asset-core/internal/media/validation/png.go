package validation

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	pngChunkHeaderBytes = 8 // 4-byte length + 4-byte type
	pngChunkCRCBytes    = 4
)

func scanPNG(data []byte) error {
	if !bytes.HasPrefix(data, pngSignature) {
		return fmt.Errorf("%w: missing PNG signature", ErrNotAnImage)
	}

	offset := len(pngSignature)
	sawHeader, sawTerminator := false, false

	for offset < len(data) && !sawTerminator {
		if len(data)-offset < pngChunkHeaderBytes {
			return fmt.Errorf("%w: truncated chunk header at %d", ErrCorruptContainer, offset)
		}

		declaredLength := binary.BigEndian.Uint32(data[offset : offset+4])
		if int64(declaredLength) > int64(len(data)-offset) {
			return fmt.Errorf("%w: chunk at %d declares %d bytes", ErrCorruptContainer, offset, declaredLength)
		}
		chunkType := string(data[offset+4 : offset+pngChunkHeaderBytes])

		payloadEnd := offset + pngChunkHeaderBytes + int(declaredLength)
		chunkEnd := payloadEnd + pngChunkCRCBytes
		if chunkEnd > len(data) {
			return fmt.Errorf("%w: chunk %q at %d runs past end of file", ErrCorruptContainer, chunkType, offset)
		}

		// The CRC covers the type and the payload, not the length.
		if computed := crc32.ChecksumIEEE(data[offset+4 : payloadEnd]); computed != binary.BigEndian.Uint32(data[payloadEnd:chunkEnd]) {
			return fmt.Errorf("%w: chunk %q at %d fails its CRC", ErrCorruptContainer, chunkType, offset)
		}

		switch chunkType {
		case "IHDR":
			if sawHeader || offset != len(pngSignature) {
				return fmt.Errorf("%w: IHDR is not the single first chunk", ErrCorruptContainer)
			}
			sawHeader = true
		case "acTL", "fcTL", "fdAT":
			return fmt.Errorf("%w: animated PNG", ErrUnsupportedFeature)
		case "IEND":
			sawTerminator = true
		default:
			if !sawHeader {
				return fmt.Errorf("%w: chunk %q precedes IHDR", ErrCorruptContainer, chunkType)
			}
		}
		offset = chunkEnd
	}

	if !sawHeader || !sawTerminator {
		return fmt.Errorf("%w: incomplete PNG stream", ErrCorruptContainer)
	}
	if offset != len(data) {
		return fmt.Errorf("%w: %d bytes follow IEND", ErrTrailingContent, len(data)-offset)
	}
	return nil
}
