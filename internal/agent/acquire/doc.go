// Package acquire orchestrates game acquisition (Stage 5+).
// AcquisitionProvider implementations (archive/steamcmd/legendary/gog)
// reuse aria2c, 7-Zip, and installer tooling; no custom downloader or
// extractor is written here. Preparation is an explicit restartable state
// machine (protocol.PipelineState) persisted as JSON beside the data.
package acquire

// Provider is the acquisition abstraction.
type Provider string

// Known providers.
const (
	ProviderArchive   Provider = "archive"
	ProviderSteamCMD  Provider = "steamcmd"
	ProviderLegendary Provider = "legendary"
	ProviderGOG       Provider = "gog"
)
