package acquire

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Cleanup removes stage inputs ONLY after the consuming stage verified.
// Policy comes from the manifest; an empty policy deletes nothing by
// default. Installed games, saves, and metadata are never touched here.
func Cleanup(d Dirs, m protocol.GameManifest, stage protocol.PipelineState) error {
	pol := m.Cleanup
	if m.PackagePipeline == nil {
		return nil
	}
	switch stage {
	case protocol.PipeArchiveExtracted:
		if !pol.DeletePartsAfterArchiveExtract {
			return nil
		}
		for _, s := range OrderedSources(m) {
			if err := os.Remove(partPath(d.Download, s)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("cleanup parts: %w", err)
			}
		}
	case protocol.PipeISOExtracted:
		if !pol.DeleteISOAfterExtract {
			return nil
		}
		if m.PackagePipeline.Result != nil && m.PackagePipeline.Result.Path != "" {
			iso := filepath.Join(d.Extract, m.GameID, m.PackagePipeline.Result.Path)
			if err := os.Remove(iso); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("cleanup iso: %w", err)
			}
		}
	case protocol.PipeInstallValidated:
		if !pol.DeleteInstallerAfterInstall {
			return nil
		}
		// Installer staging (extracted ISO contents + installer leftovers).
		stage := filepath.Join(d.Extract, m.GameID)
		if err := os.RemoveAll(stage); err != nil {
			return fmt.Errorf("cleanup installer staging: %w", err)
		}
	}
	return nil
}
