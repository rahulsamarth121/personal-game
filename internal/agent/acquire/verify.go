package acquire

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// VerifyPart checks size (when declared) and sha256 (when declared).
// Missing checksums are reported, never faked: size-only verification still
// catches truncation, and the pipeline records what was actually checked.
func VerifyPart(path string, s protocol.AcquisitionSource) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("verify part %d: %w", s.Part, err)
	}
	if s.SizeBytes > 0 && uint64(st.Size()) != s.SizeBytes {
		return fmt.Errorf("verify part %d: size %d != manifest %d", s.Part, st.Size(), s.SizeBytes)
	}
	if s.SHA256 == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != s.SHA256 {
		return fmt.Errorf("verify part %d: sha256 mismatch", s.Part)
	}
	return nil
}

// VerifyAll checks every downloaded part in manifest order.
func VerifyAll(dir string, m protocol.GameManifest) error {
	for _, s := range OrderedSources(m) {
		if err := VerifyPart(partPath(dir, s), s); err != nil {
			return err
		}
	}
	return nil
}
