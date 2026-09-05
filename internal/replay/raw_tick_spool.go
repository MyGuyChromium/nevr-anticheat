package replay

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nevr-anticheat/nevr-anticheat/internal/adapter"
	"github.com/nevr-anticheat/nevr-anticheat/internal/storage/sqlite"
)

// rawTickSpool bounds memory without touching canonical match_ticks until a
// match has parsed completely. Its binary records safely preserve arbitrary
// JSON bytes and are always deleted before the analysis call returns.
type rawTickSpool struct {
	file   *os.File
	writer *bufio.Writer
}

func newRawTickSpool() (*rawTickSpool, error) {
	cleanupStaleRawTickSpools()
	f, err := os.CreateTemp("", "nevr-raw-ticks-*.spool")
	if err != nil {
		return nil, fmt.Errorf("create raw tick spool: %w", err)
	}
	return &rawTickSpool{file: f, writer: bufio.NewWriterSize(f, 256*1024)}, nil
}

func cleanupStaleRawTickSpools() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "nevr-raw-ticks-*.spool"))
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, path := range matches {
		if info, err := os.Stat(path); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

func (s *rawTickSpool) write(rawByFrame map[int]string) error {
	idxs := make([]int, 0, len(rawByFrame))
	for idx := range rawByFrame {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	var header [16]byte
	for _, idx := range idxs {
		raw := rawByFrame[idx]
		if raw == "" {
			continue
		}
		binary.LittleEndian.PutUint64(header[:8], uint64(int64(idx)))
		binary.LittleEndian.PutUint64(header[8:], uint64(len(raw)))
		if _, err := s.writer.Write(header[:]); err != nil {
			return fmt.Errorf("write raw tick spool header: %w", err)
		}
		if _, err := s.writer.WriteString(raw); err != nil {
			return fmt.Errorf("write raw tick spool payload: %w", err)
		}
	}
	return nil
}

func (s *rawTickSpool) store(ctx context.Context, store *sqlite.Store, matchID string) (sqlite.TelemetryStoreResult, error) {
	var total sqlite.TelemetryStoreResult
	if err := s.writer.Flush(); err != nil {
		return total, fmt.Errorf("flush raw tick spool: %w", err)
	}
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return total, fmt.Errorf("rewind raw tick spool: %w", err)
	}
	r := bufio.NewReaderSize(s.file, 256*1024)
	chunk := make(map[int]string, rawTickFlushEvery)
	flush := func() error {
		if len(chunk) == 0 {
			return nil
		}
		written, err := store.StoreTelemetryFramesWithRaw(ctx, matchID, nil, chunk)
		if err != nil {
			return err
		}
		total.TicksInserted += written.TicksInserted
		total.TicksIgnored += written.TicksIgnored
		chunk = make(map[int]string, rawTickFlushEvery)
		return nil
	}
	var header [16]byte
	for {
		_, err := io.ReadFull(r, header[:])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return total, fmt.Errorf("read raw tick spool header: %w", err)
		}
		idx := int(int64(binary.LittleEndian.Uint64(header[:8])))
		size := binary.LittleEndian.Uint64(header[8:])
		if size > uint64(adapter.DefaultMaxLineBytes) {
			return total, fmt.Errorf("raw tick spool record exceeds limit: %d bytes", size)
		}
		payload := make([]byte, int(size))
		if _, err := io.ReadFull(r, payload); err != nil {
			return total, fmt.Errorf("read raw tick spool payload: %w", err)
		}
		chunk[idx] = string(payload)
		if len(chunk) >= rawTickFlushEvery {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	return total, nil
}

func (s *rawTickSpool) discard() {
	if s == nil || s.file == nil {
		return
	}
	_ = s.file.Close()
	_ = os.Remove(s.file.Name())
	s.file = nil
}
