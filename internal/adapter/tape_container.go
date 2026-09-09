package adapter

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

const maxTapeWindowBytes = 8 << 20
const maxTapeCompressedBlocks = 2_000_000

// checkTapeContainer bounds compressor window declarations before the pinned
// codec allocates its decoder. That codec couples window memory to the much
// larger cumulative decoded-byte budget. This pass reads headers only; codec
// remains responsible for envelope decoding, footer counts and CRC validation.
// Both per-block and whole-stream captures are supported. Uncompressed tapes
// have no compressor checksum and are reported separately in provenance.
func checkTapeContainer(path string) (compressed bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	size := info.Size()
	var magic [4]byte
	if _, err = f.ReadAt(magic[:], 0); err != nil {
		return false, fmt.Errorf("native tape header: %w", err)
	}
	if binary.LittleEndian.Uint32(magic[:]) != 0xfd2fb528 {
		return false, nil
	}
	var offset int64
	blocks := 0
	for offset < size {
		var headerBytes [zstd.HeaderMaxSize]byte
		n, readErr := f.ReadAt(headerBytes[:], offset)
		if readErr != nil && readErr != io.EOF {
			return true, readErr
		}
		var header zstd.Header
		if err := header.Decode(headerBytes[:n]); err != nil {
			return true, fmt.Errorf("native tape compression header at %d: %w", offset, err)
		}
		offset += int64(header.HeaderSize)
		if header.Skippable {
			if int64(header.SkippableSize) > size-offset {
				return true, fmt.Errorf("truncated native tape skippable block")
			}
			offset += int64(header.SkippableSize)
			blocks++
			if blocks > maxTapeCompressedBlocks {
				return true, fmt.Errorf("native tape exceeds compression block budget")
			}
			continue
		}
		window := header.WindowSize
		if header.SingleSegment {
			window = header.FrameContentSize
		}
		if window > maxTapeWindowBytes {
			return true, fmt.Errorf("native tape compression window exceeds %d-byte safety limit", maxTapeWindowBytes)
		}
		if header.DictionaryID != 0 {
			return true, fmt.Errorf("dictionary-compressed native tape is unsupported")
		}
		if !header.HasCheckSum {
			return true, fmt.Errorf("native tape compressed frames require content checksums; re-export with checksums enabled")
		}
		for {
			blocks++
			if blocks > maxTapeCompressedBlocks {
				return true, fmt.Errorf("native tape exceeds compression block budget")
			}
			var bh [3]byte
			if _, err := f.ReadAt(bh[:], offset); err != nil {
				return true, fmt.Errorf("truncated native tape compression block: %w", err)
			}
			value := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
			kind, count := (value>>1)&3, int64(value>>3)
			if kind == 3 || count > 128<<10 {
				return true, fmt.Errorf("invalid native tape compression block")
			}
			offset += 3
			if kind == 1 {
				count = 1
			} // RLE stores a single byte, not decoded size.
			if count > size-offset {
				return true, fmt.Errorf("truncated native tape compression payload")
			}
			offset += count
			if value&1 != 0 {
				break
			}
		}
		if 4 > size-offset {
			return true, fmt.Errorf("truncated native tape compression checksum")
		}
		offset += 4
	}
	return true, nil
}
