// Package object defines the object-storage abstraction. R2 is the initial
// target; MinIO serves local integration tests. Nodes only ever receive
// short-lived scoped presigned URLs, never long-lived credentials.
package object

import (
	"context"
	"time"
)

// StoredObject is the metadata returned after an upload/commit.
type StoredObject struct {
	Key       string
	SHA256    string
	SizeBytes uint64
}

// Store is the minimal interface the save system needs.
type Store interface {
	// PresignUpload returns a scoped upload URL for an immutable key.
	PresignUpload(ctx context.Context, key, contentSHA256 string, ttl time.Duration) (string, error)
	// PresignDownload returns a scoped download URL for a committed key.
	PresignDownload(ctx context.Context, key string, ttl time.Duration) (string, error)
}
