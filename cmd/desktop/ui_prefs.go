package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// The page's preferences (theme, density, blinded review, history filters,
// note drafts) cannot live in localStorage alone: the app listens on a random
// port, so its origin, and with it localStorage, changes on every launch. The
// server keeps one opaque JSON object beside the database instead, next to
// nevr-desktop-settings.json. It never interprets the contents.
const (
	uiPrefsFileName       = "nevr-desktop-ui-prefs.json"
	maxUIPrefsBytes int64 = 64 << 10
)

type uiPrefsStore struct {
	mu   sync.Mutex
	path string
}

// load returns the stored object, or {} when nothing usable is stored. A
// damaged file is reported as empty rather than failing the page's startup;
// the next save replaces it.
func (p *uiPrefsStore) load() ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	doc, err := os.ReadFile(p.path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte("{}"), nil
	}
	if err != nil {
		return nil, err
	}
	if int64(len(doc)) > maxUIPrefsBytes || validateUIPrefs(doc) != nil {
		return []byte("{}"), nil
	}
	return doc, nil
}

// save replaces the stored object atomically: the new document is written and
// synced to a temporary file in the same directory and then renamed over the
// old one, so a crash leaves either the old preferences or the new ones.
func (p *uiPrefsStore) save(doc []byte) error {
	if err := validateUIPrefs(doc); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	dir := filepath.Dir(p.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, uiPrefsFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(doc); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, p.path); err != nil {
		return err
	}
	keep = true
	return nil
}

var errUIPrefsNotObject = errors.New("UI preferences must be one JSON object")

// validateUIPrefs accepts exactly one JSON object of at most maxUIPrefsBytes.
func validateUIPrefs(doc []byte) error {
	if int64(len(doc)) > maxUIPrefsBytes {
		return fmt.Errorf("UI preferences exceed %d KiB", maxUIPrefsBytes>>10)
	}
	trimmed := bytes.TrimSpace(doc)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errUIPrefsNotObject
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return errUIPrefsNotObject
	}
	return nil
}

func (s *server) handleGetUIPrefs(w http.ResponseWriter, _ *http.Request) {
	doc, err := s.uiPrefs.load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reading UI preferences: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(doc)
}

func (s *server) handlePutUIPrefs(w http.ResponseWriter, r *http.Request) {
	doc, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUIPrefsBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "UI preferences exceed %d KiB", maxUIPrefsBytes>>10)
			return
		}
		writeError(w, http.StatusBadRequest, "reading UI preferences: %v", err)
		return
	}
	if err := validateUIPrefs(doc); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := s.uiPrefs.save(doc); err != nil {
		writeError(w, http.StatusInternalServerError, "saving UI preferences: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
