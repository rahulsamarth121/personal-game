// R2 (S3-compatible) presigned URLs implemented with the standard library.
//
// The control plane holds the R2 credentials and hands nodes short-lived
// scoped URLs; nodes never see the secret. Signing follows AWS Signature
// Version 4 for S3 (R2 speaks the same scheme). The crypto primitive is
// pinned to RFC 4231 vectors in tests; full live verification happens
// against R2/MinIO at deploy time (see deploy/compose).
package object

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// R2Config holds the control-plane-side credentials (never sent to nodes).
type R2Config struct {
	Endpoint  string // e.g. https://<account>.r2.cloudflarestorage.com
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // "auto" for R2
}

// R2Store implements Store via SigV4 presigned URLs.
type R2Store struct {
	cfg R2Config
	now func() time.Time
}

// NewR2Store builds a store; now defaults to time.Now (tests inject).
func NewR2Store(cfg R2Config) *R2Store {
	return &R2Store{cfg: cfg, now: time.Now}
}

// SetNow overrides the clock (tests).
func (r *R2Store) SetNow(fn func() time.Time) { r.now = fn }

// PresignUpload implements Store: scoped PUT good for ttl.
func (r *R2Store) PresignUpload(_ context.Context, key, _ string, ttl time.Duration) (string, error) {
	return r.presign("PUT", key, "UNSIGNED-PAYLOAD", ttl)
}

// PresignDownload implements Store: scoped GET good for ttl.
func (r *R2Store) PresignDownload(_ context.Context, key string, ttl time.Duration) (string, error) {
	return r.presign("GET", key, "UNSIGNED-PAYLOAD", ttl)
}

// PresignHead returns a scoped HEAD URL for existence/size checks without
// downloading the blob (used by live verification and diagnostics).
func (r *R2Store) PresignHead(_ context.Context, key string, ttl time.Duration) (string, error) {
	return r.presign("HEAD", key, "UNSIGNED-PAYLOAD", ttl)
}

func (r *R2Store) presign(method, key, payloadHash string, ttl time.Duration) (string, error) {
	if r.cfg.Endpoint == "" || r.cfg.Bucket == "" || r.cfg.AccessKey == "" || r.cfg.SecretKey == "" {
		return "", fmt.Errorf("object: r2 not configured")
	}
	if key == "" {
		return "", fmt.Errorf("object: empty key")
	}
	if ttl <= 0 || ttl > 7*24*time.Hour {
		return "", fmt.Errorf("object: ttl out of range (1s..7d)")
	}
	ep, err := url.Parse(strings.TrimSuffix(r.cfg.Endpoint, "/"))
	if err != nil {
		return "", fmt.Errorf("object: bad endpoint: %w", err)
	}
	now := r.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	region := r.cfg.Region
	if region == "" {
		region = "auto"
	}
	// Path-style: /bucket/key with each segment escaped, slashes kept.
	escaped := escapeKey(key)
	canonicalURI := "/" + r.cfg.Bucket + "/" + escaped
	params := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    r.cfg.AccessKey + "/" + dateStamp + "/" + region + "/s3/aws4_request",
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       fmt.Sprintf("%d", int64(ttl.Seconds())),
		"X-Amz-SignedHeaders": "host",
	}
	var keys []string
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var qs strings.Builder
	for i, k := range keys {
		if i > 0 {
			qs.WriteByte('&')
		}
		qs.WriteString(k)
		qs.WriteByte('=')
		qs.WriteString(url.QueryEscape(params[k]))
	}
	canonicalHeaders := "host:" + ep.Host + "\n"
	signedHeaders := "host"
	canonicalRequest := strings.Join([]string{
		method, canonicalURI, qs.String(), canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")
	sum := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := dateStamp + "/" + region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + hex.EncodeToString(sum[:])
	signingKey := deriveKey(r.cfg.SecretKey, dateStamp, region, "s3")
	sig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	return ep.Scheme + "://" + ep.Host + canonicalURI + "?" + qs.String() + "&X-Amz-Signature=" + sig, nil
}

// escapeKey percent-encodes a key segment-wise (slashes preserved, S3 rules:
// unreserved + sub-delims mostly kept; QueryEscape uppercases hex already).
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func deriveKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}
