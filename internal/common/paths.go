package common

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ValidatePath ensures target resolves inside one of allowedRoots.
// It rejects absolute-escaping "..", drive changes, and symlink-agnostic
// lexical escapes. Callers must pass absolute allowedRoots.
//
// This is a load-bearing security control: manifests, save staging, and
// acquisition must never write outside their roots.
func ValidatePath(allowedRoots []string, target string) (string, error) {
	if len(allowedRoots) == 0 {
		return "", errors.New("common: no allowed roots configured")
	}
	if target == "" {
		return "", errors.New("common: empty target path")
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	for _, r := range allowedRoots {
		rabs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		rclean := filepath.Clean(rabs)
		// Exact match or strictly-inside match on a separator boundary.
		if clean == rclean || strings.HasPrefix(clean, rclean+string(os.PathSeparator)) {
			// Belt-and-braces: reject any ".." that survived Clean tricks
			// on Windows (e.g. mixed separators already normalized by Clean).
			rel, err := filepath.Rel(rclean, clean)
			if err != nil {
				continue
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			return clean, nil
		}
	}
	return "", errors.New("common: path escapes allowed roots")
}
