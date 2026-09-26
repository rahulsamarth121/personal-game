// Live object-storage test: runs only when PG_LIVE_OBJECT_TEST=1.
// Defaults target local MinIO from deploy/compose (bucket created by
// minio-init); set R2_* for live Cloudflare R2. CI skips safely.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/personal-game/personal-game/internal/store/object"
)

func liveObjectStore(t *testing.T) *object.R2Store {
	t.Helper()
	if os.Getenv("PG_LIVE_OBJECT_TEST") != "1" {
		t.Skip("PG_LIVE_OBJECT_TEST!=1: live object test skipped")
	}
	endpoint := os.Getenv("R2_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:9000"
	}
	bucket := os.Getenv("R2_BUCKET")
	if bucket == "" {
		bucket = "personal-game-saves"
	}
	access := os.Getenv("R2_ACCESS_KEY_ID")
	if access == "" {
		access = "minioadmin"
	}
	secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	if secret == "" {
		secret = "minioadmin"
	}
	region := os.Getenv("R2_REGION")
	if region == "" {
		region = "us-east-1"
	}
	return object.NewR2Store(object.R2Config{
		Endpoint: endpoint, Bucket: bucket,
		AccessKey: access, SecretKey: secret, Region: region,
	})
}

func TestLiveObjectRoundTrip(t *testing.T) {
	st := liveObjectStore(t)
	ctx := context.Background()
	key := fmt.Sprintf("smoke/%d/blob", time.Now().UnixNano())
	want := []byte("personal-game live object smoke " + key)

	putURL, err := st.PresignUpload(ctx, key, "", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("put (is MinIO/R2 up?): %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("put status %d", resp.StatusCode)
	}

	getURL, err := st.PresignDownload(ctx, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	gresp, err := http.Get(getURL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, err := io.ReadAll(gresp.Body)
	gresp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("round-trip bytes differ")
	}
}
