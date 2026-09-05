package replay

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
)

func TestRawTickSpoolRejectsNegativeFrameIndex(t *testing.T) {
	spool, err := newRawTickSpool()
	if err != nil {
		t.Fatalf("newRawTickSpool: %v", err)
	}
	defer spool.discard()

	err = spool.write(map[int]string{-1: `{}`})
	if err == nil || !strings.Contains(err.Error(), "negative frame index") {
		t.Fatalf("write error = %v, want negative frame index error", err)
	}
}

func TestRawTickSpoolRejectsFrameIndexOutsidePlatformRange(t *testing.T) {
	spool, err := newRawTickSpool()
	if err != nil {
		t.Fatalf("newRawTickSpool: %v", err)
	}
	defer spool.discard()

	var header [16]byte
	binary.LittleEndian.PutUint64(header[:8], ^uint64(0))
	if _, err := spool.writer.Write(header[:]); err != nil {
		t.Fatalf("write header: %v", err)
	}

	_, err = spool.store(context.Background(), nil, "match")
	if err == nil || !strings.Contains(err.Error(), "frame index exceeds platform limit") {
		t.Fatalf("store error = %v, want platform limit error", err)
	}
}
