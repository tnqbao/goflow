package renderer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/tnqbao/kubereel/internal/storage"
)

const (
	minMP4Bytes       = 24
	boxHeaderBytes    = 8
	largeHeaderBytes  = 16
	minFtypHeaderSize = 16
)

// ValidateMP4 checks that the stored artifact is a structurally valid MP4 container
// containing ftyp, moov, and non-empty mdat top-level ISO BMFF boxes.
func ValidateMP4(ctx context.Context, store storage.Store, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if store == nil {
		return fmt.Errorf("artifact storage is required")
	}

	object, err := store.Open(ctx, key)
	if err != nil {
		return fmt.Errorf("open MP4 artifact: %w", err)
	}
	defer object.Close()

	info, err := object.Stat()
	if err != nil {
		return fmt.Errorf("stat MP4 artifact: %w", err)
	}

	return validateMP4Reader(ctx, object, info.Size())
}

func validateMP4Reader(ctx context.Context, reader io.ReadSeeker, totalSize int64) error {
	if totalSize < minMP4Bytes {
		return fmt.Errorf("MP4 is truncated (%d bytes)", totalSize)
	}

	var (
		offset   int64
		boxIndex int
		hasFtyp  bool
		hasMoov  bool
		hasMdat  bool
	)

	for offset < totalSize {
		if err := ctx.Err(); err != nil {
			return err
		}

		boxType, boxSize, headerSize, err := readBoxHeader(reader, offset, totalSize)
		if err != nil {
			return err
		}

		if boxIndex == 0 && (boxType != "ftyp" || boxSize < minFtypHeaderSize) {
			return fmt.Errorf("MP4 must start with a valid ftyp box")
		}

		payloadSize := boxSize - headerSize
		if boxType == "ftyp" {
			hasFtyp = true
		}
		if boxType == "moov" && payloadSize > 0 {
			hasMoov = true
		}
		if boxType == "mdat" && payloadSize > 0 {
			hasMdat = true
		}

		offset += boxSize
		boxIndex++
	}

	if !hasFtyp || !hasMoov || !hasMdat {
		return fmt.Errorf("MP4 is missing required ftyp, moov, or mdat boxes")
	}

	return nil
}

func readBoxHeader(reader io.ReadSeeker, offset, totalSize int64) (string, int64, int64, error) {
	if _, err := reader.Seek(offset, io.SeekStart); err != nil {
		return "", 0, 0, fmt.Errorf("seek MP4 box: %w", err)
	}

	remaining := totalSize - offset
	if remaining < boxHeaderBytes {
		return "", 0, 0, fmt.Errorf("truncated MP4 box header")
	}

	var header [boxHeaderBytes]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return "", 0, 0, fmt.Errorf("read MP4 box header: %w", err)
	}

	rawSize := binary.BigEndian.Uint32(header[:4])
	boxType := string(header[4:8])
	if !isPrintableASCIIFourCC(boxType) {
		return "", 0, 0, fmt.Errorf("invalid MP4 box type")
	}

	if rawSize == 0 {
		return boxType, remaining, boxHeaderBytes, nil
	}

	if rawSize == 1 {
		return readLargeBoxSize(reader, boxType, remaining)
	}

	boxSize := int64(rawSize)
	if boxSize < boxHeaderBytes || boxSize > remaining {
		return "", 0, 0, errors.New("invalid MP4 box size")
	}

	return boxType, boxSize, boxHeaderBytes, nil
}

func readLargeBoxSize(reader io.Reader, boxType string, remaining int64) (string, int64, int64, error) {
	if remaining < largeHeaderBytes {
		return "", 0, 0, fmt.Errorf("truncated large MP4 box header")
	}

	var ext [8]byte
	if _, err := io.ReadFull(reader, ext[:]); err != nil {
		return "", 0, 0, fmt.Errorf("read large MP4 box size: %w", err)
	}

	large := binary.BigEndian.Uint64(ext[:])
	if large < largeHeaderBytes || large > uint64(remaining) {
		return "", 0, 0, errors.New("invalid large MP4 box size")
	}

	return boxType, int64(large), largeHeaderBytes, nil
}

func isPrintableASCIIFourCC(fourCC string) bool {
	if len(fourCC) != 4 {
		return false
	}

	for i := 0; i < 4; i++ {
		ch := fourCC[i]
		if ch < 0x20 || ch > 0x7e {
			return false
		}
	}

	return true
}
