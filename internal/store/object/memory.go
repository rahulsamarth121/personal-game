package object

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory object Store for tests and local dev without
// credentials. Presigned URLs use the mem:// scheme and are honored by
// UploadURL/DownloadURL below; nothing leaves the process.
type MemoryStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{blobs: map[string][]byte{}}
}

// PresignUpload implements Store.
func (m *MemoryStore) PresignUpload(_ context.Context, key, _ string, _ time.Duration) (string, error) {
	if key == "" {
		return "", fmt.Errorf("object: empty key")
	}
	return "mem://upload/" + key, nil
}

// PresignDownload implements Store.
func (m *MemoryStore) PresignDownload(_ context.Context, key string, _ time.Duration) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.blobs[key]; !ok {
		return "", fmt.Errorf("object: key not found")
	}
	return "mem://download/" + key, nil
}

// UploadURL stores body at the key named by a mem://upload/ URL after
// verifying its SHA-256. Non-mem URLs are refused (this fake never dials).
func (m *MemoryStore) UploadURL(_ context.Context, url string, body io.Reader, wantSHA string) error {
	key, ok := strings.CutPrefix(url, "mem://upload/")
	if !ok || key == "" {
		return fmt.Errorf("object: invalid upload URL")
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != wantSHA {
		return fmt.Errorf("object: upload sha256 mismatch")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = append([]byte(nil), raw...)
	return nil
}

// DownloadURL returns the bytes named by a mem://download/ URL.
func (m *MemoryStore) DownloadURL(_ context.Context, url string) ([]byte, error) {
	key, ok := strings.CutPrefix(url, "mem://download/")
	if !ok || key == "" {
		return nil, fmt.Errorf("object: invalid download URL")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[key]
	if !ok {
		return nil, fmt.Errorf("object: key not found")
	}
	return append([]byte(nil), b...), nil
}

// DirectPut bypasses URLs for tests that drive the store in-process.
func (m *MemoryStore) DirectPut(key string, b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blobs[key] = append([]byte(nil), b...)
}
