package object

import (
	"bytes"
	"context"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"
)

// RFC 4231 test case 1 pins the HMAC-SHA256 primitive our SigV4 signing
// is built on: key = 20x 0x0b, data = "Hi There".
func TestHMACVector(t *testing.T) {
	key := bytes.Repeat([]byte{0x0b}, 20)
	got := hex.EncodeToString(hmacSHA256(key, []byte("Hi There")))
	want := "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"
	if got != want {
		t.Fatalf("hmac mismatch: got %s", got)
	}
}

func contextBG() context.Context { return context.Background() }

func testR2() *R2Store {
	r := NewR2Store(R2Config{
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "saves",
		AccessKey: "AKID", SecretKey: "SECRET", Region: "auto",
	})
	r.SetNow(func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) })
	return r
}

func parsePresigned(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "acct.r2.cloudflarestorage.com" {
		t.Fatalf("bad endpoint: %s", raw)
	}
	if !strings.HasPrefix(u.Path, "/saves/saves/u1/g1/42/blob") {
		t.Fatalf("bad key path: %s", u.Path)
	}
	return u.Query()
}

func TestPresignStructure(t *testing.T) {
	r := testR2()
	ctx := contextBG()
	up, err := r.PresignUpload(ctx, "saves/u1/g1/42/blob", "", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	q := parsePresigned(t, up)
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		t.Fatalf("bad algorithm: %+v", q)
	}
	if !strings.Contains(q.Get("X-Amz-Credential"), "AKID/20260925/auto/s3/aws4_request") {
		t.Fatalf("bad credential scope: %s", q.Get("X-Amz-Credential"))
	}
	if q.Get("X-Amz-Date") != "20260925T120000Z" || q.Get("X-Amz-Expires") != "900" {
		t.Fatalf("bad date/expiry: %+v", q)
	}
	if len(q.Get("X-Amz-Signature")) != 64 {
		t.Fatalf("bad signature: %+v", q)
	}
	// Deterministic: same inputs, same URL.
	up2, _ := r.PresignUpload(ctx, "saves/u1/g1/42/blob", "", 15*time.Minute)
	if up != up2 {
		t.Fatal("presign must be deterministic")
	}
	// Method separation: PUT, GET, and HEAD authorizations all differ.
	down, _ := r.PresignDownload(ctx, "saves/u1/g1/42/blob", 15*time.Minute)
	if down == up {
		t.Fatal("upload and download URLs must differ")
	}
	head, err := r.PresignHead(ctx, "saves/u1/g1/42/blob", 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if head == up || head == down {
		t.Fatal("head URL must differ from put/get")
	}
	// Tamper-evidence: different secret, different signature.
	r2 := testR2()
	r2.cfg.SecretKey = "OTHER"
	up3, _ := r2.PresignUpload(ctx, "saves/u1/g1/42/blob", "", 15*time.Minute)
	if up3 == up {
		t.Fatal("different secret must change signature")
	}
}

func TestPresignRejects(t *testing.T) {
	r := testR2()
	ctx := contextBG()
	if _, err := r.PresignUpload(ctx, "", "", time.Minute); err == nil {
		t.Fatal("empty key must fail")
	}
	if _, err := r.PresignUpload(ctx, "k", "", 0); err == nil {
		t.Fatal("zero ttl must fail")
	}
	if _, err := r.PresignDownload(ctx, "k", 8*24*time.Hour); err == nil {
		t.Fatal(">7d ttl must fail")
	}
	if _, err := NewR2Store(R2Config{}).PresignDownload(ctx, "k", time.Minute); err == nil {
		t.Fatal("unconfigured store must fail, never sign unsigned")
	}
}
