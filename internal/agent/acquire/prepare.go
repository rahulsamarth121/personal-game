package acquire

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/personal-game/personal-game/pkg/protocol"
)

// Prepare drives one game's preparation end to end, dispatching on the
// effective package type. Every stage transition is persisted, so restarts
// resume instead of repeating verified work. Returns the READY pipeline.
func Prepare(ctx context.Context, d Dirs, m protocol.GameManifest, dl *Downloader, freeBytes, evictableBytes uint64) (*Pipeline, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := d.Ensure(); err != nil {
		return nil, err
	}
	p, err := LoadPipeline(d, m)
	if err != nil {
		return nil, err
	}
	if p.State == protocol.PipeReady {
		return p, nil // warm cache hit: never re-download
	}
	if p.State == protocol.PipeFailed {
		// Explicit retry starts a new plan rather than resuming failure.
		if err := p.Advance(protocol.PipePlanned); err != nil {
			return nil, err
		}
	}
	pt := EffectiveType(m)
	prov, err := ProviderForManifest(m, dl, d.Games)
	if err != nil {
		return nil, err
	}
	if pt.IsPrebuilt() {
		return preparePrebuilt(ctx, p, prov, freeBytes, evictableBytes)
	}
	return prepareInstaller(ctx, p, prov, freeBytes, evictableBytes)
}

// gate checks disk with the package-type-aware requirement before any byte
// is downloaded. Failure aborts cleanly with a user-facing reason.
func gate(p *Pipeline, freeBytes, evictableBytes uint64) error {
	report := CheckDiskFor(p.Manifest, freeBytes, evictableBytes)
	if !report.Fits && !report.FitsAfterEvict {
		return fmt.Errorf("acquire: %s", report.Message)
	}
	return p.Advance(protocol.PipeSpaceChecked)
}

// preparePrebuilt: download -> verify -> place+validate -> clean source -> READY.
// No installer stage exists on this path by construction.
func preparePrebuilt(ctx context.Context, p *Pipeline, prov AcquisitionProvider, freeBytes, evictableBytes uint64) (*Pipeline, error) {
	m := p.Manifest
	sources := OrderedSources(m)
	fail := func(err error) (*Pipeline, error) {
		_ = p.Fail("prebuilt preparation failed", err)
		return p, err
	}
	if p.State == protocol.PipePlanned {
		if err := gate(p, freeBytes, evictableBytes); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeSpaceChecked {
		if err := p.Advance(protocol.PipeDownloading); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeDownloading {
		for _, s := range sources {
			if err := prov.Fetch(ctx, p.Dirs.Download, s); err != nil {
				return fail(err)
			}
		}
		if err := p.Advance(protocol.PipeDownloadVerified); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeDownloadVerified {
		if err := VerifyAll(p.Dirs.Download, m); err != nil {
			return fail(err)
		}
		if err := p.Advance(protocol.PipeArchiveReady); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeArchiveReady {
		if _, err := PlacePrebuilt(ctx, p.Dirs, m, sources[0]); err != nil {
			return fail(err)
		}
		if err := p.Advance(protocol.PipeArchiveExtracted); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeArchiveExtracted {
		// Game validated: the source archive is now redundant.
		if err := CleanupPrebuilt(p.Dirs, m); err != nil {
			return fail(err)
		}
		if err := p.Advance(protocol.PipeSourceCleaned); err != nil {
			return fail(err)
		}
	}
	if p.State == protocol.PipeSourceCleaned {
		if err := p.Advance(protocol.PipeReady); err != nil {
			return fail(err)
		}
	}
	return p, nil
}

// prepareInstaller: the classic multipart/archive -> ISO -> installer flow,
// each input deleted only after its consumer stage verifies.
func prepareInstaller(ctx context.Context, p *Pipeline, prov AcquisitionProvider, freeBytes, evictableBytes uint64) (*Pipeline, error) {
	m := p.Manifest
	fail := func(err error) (*Pipeline, error) {
		_ = p.Fail("installer preparation failed", err)
		return p, err
	}
	step := func(from, to protocol.PipelineState, fn func() error) error {
		if p.State != from {
			return nil
		}
		if fn != nil {
			if err := fn(); err != nil {
				return err
			}
		}
		return p.Advance(to)
	}
	if p.State == protocol.PipePlanned {
		if err := gate(p, freeBytes, evictableBytes); err != nil {
			return fail(err)
		}
	}
	if err := step(protocol.PipeSpaceChecked, protocol.PipeDownloading, nil); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeDownloading, protocol.PipeDownloadVerified, func() error {
		for _, s := range OrderedSources(m) {
			if err := prov.Fetch(ctx, p.Dirs.Download, s); err != nil {
				return err
			}
		}
		return VerifyAll(p.Dirs.Download, m)
	}); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeDownloadVerified, protocol.PipeArchiveReady, nil); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeArchiveReady, protocol.PipeArchiveExtracted, func() error {
		return ExtractArchive(ctx, p.Dirs, m)
	}); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeArchiveExtracted, protocol.PipeSourceCleaned, func() error {
		return Cleanup(p.Dirs, m, protocol.PipeArchiveExtracted)
	}); err != nil {
		return fail(err)
	}
	// ISO stage (skipped when the manifest declares none).
	if hasISOStage(m) {
		if err := step(protocol.PipeSourceCleaned, protocol.PipeISOReady, nil); err != nil {
			return fail(err)
		}
		if err := step(protocol.PipeISOReady, protocol.PipeISOExtracted, func() error {
			return extractISOStep(ctx, p)
		}); err != nil {
			return fail(err)
		}
		if err := step(protocol.PipeISOExtracted, protocol.PipeISOCleaned, func() error {
			return Cleanup(p.Dirs, m, protocol.PipeISOExtracted)
		}); err != nil {
			return fail(err)
		}
	}
	beforeInstall := protocol.PipeSourceCleaned
	if hasISOStage(m) {
		beforeInstall = protocol.PipeISOCleaned
	}
	if err := step(beforeInstall, protocol.PipeInstallerReady, nil); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeInstallerReady, protocol.PipeInstalling, nil); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeInstalling, protocol.PipeInstallValidated, func() error {
		if m.PackagePipeline == nil || m.PackagePipeline.Installer == nil {
			return fmt.Errorf("installer stage declared but no package_pipeline.installer in manifest")
		}
		ins := m.PackagePipeline.Installer
		stage := filepath.Join(p.Dirs.Extract, m.GameID)
		return RunInstaller(ctx, stage, *ins, p.Dirs.Games, m.GameID)
	}); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeInstallValidated, protocol.PipeInstallerCleaned, func() error {
		if _, err := ValidateLaunchTarget(p.Dirs.Games, m.GameID, m.Launch); err != nil {
			return err
		}
		return Cleanup(p.Dirs, m, protocol.PipeInstallValidated)
	}); err != nil {
		return fail(err)
	}
	if err := step(protocol.PipeInstallerCleaned, protocol.PipeReady, nil); err != nil {
		return fail(err)
	}
	// Resume path: a persisted mid-pipeline state re-enters above at the
	// right stage; anything already READY returns immediately in Prepare.
	if p.State != protocol.PipeReady {
		return fail(fmt.Errorf("pipeline stalled at %s", p.State))
	}
	return p, nil
}

func hasISOStage(m protocol.GameManifest) bool {
	return m.PackagePipeline != nil && m.PackagePipeline.ISO != nil && m.PackagePipeline.ISO.Extract
}

func extractISOStep(ctx context.Context, p *Pipeline) error {
	m := p.Manifest
	stage := filepath.Join(p.Dirs.Extract, m.GameID)
	iso, err := FindResult(stage, *m.PackagePipeline.Result)
	if err != nil {
		return err
	}
	dest := filepath.Join(stage, "iso")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	return ExtractISO(ctx, iso, dest)
}

// CleanupPrebuilt deletes the source archive after the placed game
// validated. The policy must opt in; without it nothing is deleted.
func CleanupPrebuilt(d Dirs, m protocol.GameManifest) error {
	if !m.Cleanup.DeleteArchiveAfterPrebuiltReady {
		return nil
	}
	for _, s := range OrderedSources(m) {
		if err := os.Remove(partPath(d.Download, s)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("cleanup prebuilt source: %w", err)
		}
	}
	return nil
}
