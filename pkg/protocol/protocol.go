// Package protocol defines the shared versioned wire models between the
// node agent, the control plane, and the client shell.
//
// These types are the contract for Stage 0 (foundation). They deliberately
// contain no I/O, no SQL, and no provider-specific logic so that LOCAL and
// KAGGLE adapters can share them. Provider differences are expressed as
// Capabilities, never as `if provider == ...` branches in business logic.
package protocol

import (
	"errors"
	"fmt"
	"time"
)

// Current schema versions. Bump when the corresponding model changes.
const (
	SessionSchemaVersion  = 1
	SaveSchemaVersion     = 1
	ManifestSchemaVersion = 3 // v3 adds explicit package_type + prebuilt modes
)

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// SessionState is the lifecycle state of one play session.
type SessionState string

const (
	SessionRequested    SessionState = "REQUESTED"
	SessionNodeAssigned SessionState = "NODE_ASSIGNED"
	SessionPreparing    SessionState = "PREPARING"
	SessionReady        SessionState = "READY"
	SessionStreaming    SessionState = "STREAMING"
	SessionDegraded     SessionState = "DEGRADED"
	SessionDraining     SessionState = "DRAINING"
	SessionFenced       SessionState = "FENCED"
	SessionClosed       SessionState = "CLOSED"
	SessionFailed       SessionState = "FAILED"
)

// ValidSessionTransitions is the authoritative transition table.
// Client disconnect: STREAMING -> DEGRADED -> DRAINING (grace period, game kept).
// Lease loss: NODE_ASSIGNED onward -> FENCED (any lease-bearing state).
var ValidSessionTransitions = map[SessionState][]SessionState{
	SessionRequested:    {SessionNodeAssigned, SessionFailed},
	SessionNodeAssigned: {SessionPreparing, SessionFenced, SessionFailed},
	SessionPreparing:    {SessionReady, SessionFenced, SessionFailed},
	SessionReady:        {SessionStreaming, SessionDraining, SessionFenced, SessionFailed},
	SessionStreaming:    {SessionDegraded, SessionDraining, SessionFenced, SessionFailed},
	SessionDegraded:     {SessionStreaming, SessionDraining, SessionFenced, SessionFailed},
	SessionDraining:     {SessionClosed, SessionFailed},
	SessionFenced:       {SessionClosed, SessionFailed},
	SessionClosed:       {},
	SessionFailed:       {},
}

// CanTransition reports whether from -> to is a legal session transition.
func (s SessionState) CanTransition(to SessionState) bool {
	for _, n := range ValidSessionTransitions[s] {
		if n == to {
			return true
		}
	}
	return false
}

// IsTerminal reports whether the state ends billing/playtime accumulation.
func (s SessionState) IsTerminal() bool {
	return s == SessionClosed || s == SessionFailed
}

// IsActive reports whether gameplay/streaming may be running.
func (s SessionState) IsActive() bool {
	switch s {
	case SessionPreparing, SessionReady, SessionStreaming, SessionDegraded:
		return true
	default:
		return false
	}
}

// SessionEndReason records why a session stopped.
type SessionEndReason string

const (
	EndGameExit       SessionEndReason = "GAME_EXIT"
	EndGameCrash      SessionEndReason = "GAME_CRASH"
	EndClientGone     SessionEndReason = "CLIENT_GONE"
	EndLeaseLost      SessionEndReason = "LEASE_LOST"
	EndNodeDead       SessionEndReason = "NODE_DEAD"
	EndAborted        SessionEndReason = "ABORTED"
	EndControlRestart SessionEndReason = "CONTROL_RESTART"
)

// Session is the versioned session record. PostgreSQL is authoritative.
type Session struct {
	SchemaVersion  int              `json:"schema_version"`
	SessionID      string           `json:"session_id"`
	UserID         string           `json:"user_id"`
	GameID         string           `json:"game_id"`
	NodeID         string           `json:"node_id"`
	FenceToken     uint64           `json:"fence_token"`
	State          SessionState     `json:"state"`
	CreatedAt      time.Time        `json:"created_at"`
	ReadyAt        *time.Time       `json:"ready_at,omitempty"`
	ClosedAt       *time.Time       `json:"closed_at,omitempty"`
	ActiveSeconds  int64            `json:"active_seconds"`
	WallSeconds    int64            `json:"wall_seconds"`
	PrepareSeconds int64            `json:"prepare_seconds"`
	EndReason      SessionEndReason `json:"end_reason,omitempty"`
	SaveGens       []uint64         `json:"save_generations,omitempty"`
	Stream         StreamConfig     `json:"stream"`
}

// Validate performs cheap structural checks (not DB checks).
func (s *Session) Validate() error {
	if s.SchemaVersion != SessionSchemaVersion {
		return errors.New("protocol: unsupported session schema version")
	}
	if s.SessionID == "" || s.UserID == "" || s.GameID == "" {
		return errors.New("protocol: session requires session_id, user_id, game_id")
	}
	if s.State == "" {
		return errors.New("protocol: session state required")
	}
	return nil
}

// StreamConfig describes how the client should connect. The control plane
// issues it; it never relays video itself.
type StreamConfig struct {
	Provider string `json:"provider"` // e.g. "sunshine", "wolf"
	Host     string `json:"host"`     // Tailscale IP / MagicDNS name (phase 1)
	Port     int    `json:"port"`
	App      string `json:"app,omitempty"` // Moonlight app title (Wolf) or game name
}

// ---------------------------------------------------------------------------
// Nodes
// ---------------------------------------------------------------------------

// NodeState is the agent-side lifecycle state.
type NodeState string

const (
	NodeBoot        NodeState = "BOOT"
	NodeProbing     NodeState = "PROBING"
	NodeRegistering NodeState = "REGISTERING"
	NodeIdle        NodeState = "IDLE"
	NodePreparing   NodeState = "PREPARING"
	NodeBusy        NodeState = "BUSY"
	NodeDraining    NodeState = "DRAINING"
	NodeTerminated  NodeState = "TERMINATED"
)

// Control-side health overlay (control plane only).
type NodeHealth string

const (
	NodeHealthy NodeHealth = "HEALTHY"
	NodeSuspect NodeHealth = "SUSPECT"
	NodeDead    NodeHealth = "DEAD"
)

// ValidNodeTransitions is the agent-side transition table.
var ValidNodeTransitions = map[NodeState][]NodeState{
	NodeBoot:        {NodeProbing},
	NodeProbing:     {NodeRegistering, NodeTerminated},
	NodeRegistering: {NodeIdle, NodeTerminated},
	NodeIdle:        {NodePreparing, NodeDraining, NodeTerminated},
	NodePreparing:   {NodeBusy, NodeDraining, NodeTerminated},
	NodeBusy:        {NodeDraining, NodeIdle, NodeTerminated},
	NodeDraining:    {NodeIdle, NodeTerminated},
	NodeTerminated:  {},
}

// CanTransition reports whether from -> to is legal for a node.
func (s NodeState) CanTransition(to NodeState) bool {
	for _, n := range ValidNodeTransitions[s] {
		if n == to {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Capabilities (provider-agnostic)
// ---------------------------------------------------------------------------

// Capabilities is the structured report the agent sends at startup and on
// change. The scheduler matches GameManifest.CapabilitiesRequired against it.
type Capabilities struct {
	OS               string   `json:"os"`
	Arch             string   `json:"arch"`
	CPUModel         string   `json:"cpu_model,omitempty"`
	CPUCores         int      `json:"cpu_cores"`
	RAMMB            uint64   `json:"ram_mb"`
	GPU              GPUCap   `json:"gpu"`
	Encoders         []string `json:"encoders,omitempty"`
	ContainerRuntime string   `json:"container_runtime,omitempty"` // e.g. "nvidia", "" = none detected
	Docker           bool     `json:"docker"`
	Podman           bool     `json:"podman"`
	Wolf             bool     `json:"wolf"`
	Sunshine         bool     `json:"sunshine"`
	Gamepad          bool     `json:"gamepad"`
	VirtualDisp      bool     `json:"virtual_display"`
	Tailscale        bool     `json:"tailscale"`
	// StreamingAllowed is an operator/policy decision (env
	// STREAMING_ALLOWED on the node): may this host serve interactive
	// game streaming at all. Nodes without it never receive sessions.
	StreamingAllowed bool       `json:"streaming_allowed"`
	Disk             DiskCap    `json:"disk"`
	Network          NetworkCap `json:"network"`
	Streaming        []string   `json:"streaming,omitempty"`
}

// GPUCap describes the primary GPU (best-effort; empty Model = unknown/none).
type GPUCap struct {
	Model  string `json:"model,omitempty"`
	VRAMMB uint64 `json:"vram_mb,omitempty"`
	Driver string `json:"driver,omitempty"`
}

// DiskCap describes the volume hosting /games and /cache.
type DiskCap struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	FSType     string `json:"fs_type,omitempty"`
}

// NetworkCap is intentionally coarse in phase 1 (Tailscale hides NAT).
// MediaNetwork carries the provider-neutral media path the node will serve
// sessions on; TailscaleIP remains for diagnostics/back-compat.
type NetworkCap struct {
	HasInbound   bool         `json:"has_inbound"`
	TailscaleIP  string       `json:"tailscale_ip,omitempty"`
	MediaNetwork MediaNetwork `json:"media_network,omitempty"`
}

// Media providers for the media path (Moonlight <-> backend). The control
// plane never implements these transports; it only relays the endpoint the
// node reports. Values are closed-set: unknown providers never schedule.
type MediaProvider string

const (
	MediaTailscale            MediaProvider = "tailscale"
	MediaCloudflarePrivateNet MediaProvider = "cloudflare_private_network"
	MediaDirect               MediaProvider = "direct"
)

// IsValid reports whether p is a known media provider.
func (p MediaProvider) IsValid() bool {
	switch p {
	case MediaTailscale, MediaCloudflarePrivateNet, MediaDirect:
		return true
	default:
		return false
	}
}

// MediaNetwork is the node's advertised media path. Endpoint must be
// usable by Moonlight (IP or DNS name); it is published to the client as
// the stream host. Reachable=false means "not usable", never guessed.
type MediaNetwork struct {
	Provider      MediaProvider `json:"provider"`
	Endpoint      string        `json:"endpoint"`
	AddressFamily string        `json:"address_family,omitempty"` // "ipv4" | "ipv6" | "dns"
	TCPOK         bool          `json:"tcp_ok"`
	UDPOK         bool          `json:"udp_ok"`
	Reachable     bool          `json:"reachable"`
	Detail        string        `json:"detail,omitempty"`
}

// Usable reports whether the node may serve Moonlight sessions on it.
func (m MediaNetwork) Usable() bool {
	return m.Provider.IsValid() && m.Endpoint != "" && m.Reachable && m.TCPOK && m.UDPOK
}

// Validate checks structural invariants.
func (m MediaNetwork) Validate() error {
	if !m.Provider.IsValid() {
		return fmt.Errorf("protocol: unknown media provider %q", string(m.Provider))
	}
	if m.Endpoint == "" {
		return fmt.Errorf("protocol: media network %s requires an endpoint", m.Provider)
	}
	return nil
}

// Satisfies reports whether caps meet the manifest's minimum requirements.
// A node without streaming_allowed can host nothing interactive, regardless
// of hardware.
func (c Capabilities) Satisfies(req CapabilityRequirements) error {
	if !c.StreamingAllowed {
		return ErrNoStreamingNode
	}
	if req.MinVRAMMB > 0 && c.GPU.VRAMMB < req.MinVRAMMB && c.GPU.Model != "" {
		return errors.New("protocol: insufficient VRAM")
	}
	if req.Gamepad && !c.Gamepad {
		return errors.New("protocol: gamepad required but unavailable")
	}
	if req.HDR && !supportsHDR(c.Encoders) {
		return errors.New("protocol: HDR required but no HDR-capable encoder reported")
	}
	return nil
}

func supportsHDR(encoders []string) bool {
	for _, e := range encoders {
		if e == "nvenc-hevc-hdr" || e == "amf-hevc-hdr" || e == "qsv-hevc-hdr" {
			return true
		}
	}
	return false
}

// CapabilityRequirements is part of the game manifest.
type CapabilityRequirements struct {
	Gamepad   bool   `json:"gamepad"`
	HDR       bool   `json:"hdr"`
	MinVRAMMB uint64 `json:"min_vram_mb"`
}

// ErrNoStreamingNode is returned by Satisfies when the node may not host
// interactive game streaming at all (operator/policy decision, not a
// hardware gap). Sessions must never land on such a node.
var ErrNoStreamingNode = errors.New("protocol: node does not allow interactive game streaming")

// ---------------------------------------------------------------------------
// Game manifest
// ---------------------------------------------------------------------------

// GameManifest is the versioned per-game recipe: how to acquire, how much
// disk it needs, how to launch, where saves live, what it requires.
type GameManifest struct {
	SchemaVersion        int                    `json:"schema_version"`
	GameID               string                 `json:"game_id"`
	Name                 string                 `json:"name"`
	Version              string                 `json:"version"`
	Acquisition          AcquisitionRef         `json:"acquisition"`
	Footprint            Footprint              `json:"footprint"`
	PackagePipeline      *PackagePipeline       `json:"package_pipeline,omitempty"`
	Cleanup              CleanupPolicy          `json:"cleanup,omitempty"`
	Runtime              RuntimeSpec            `json:"runtime"`
	Launch               LaunchSpec             `json:"launch"`
	Saves                SaveSpec               `json:"saves"`
	CapabilitiesRequired CapabilityRequirements `json:"capabilities_required"`
}

// PackageType declares how a downloaded package becomes a playable game.
// The manifest is authoritative; extension sniffing is only a helper.
// Providers (steamcmd/legendary/gog) manage installation themselves.
type PackageType string

const (
	PackageProvider         PackageType = "provider"
	PackageArchiveInstaller PackageType = "archive_installer"
	PackageArchivePrebuilt  PackageType = "archive_prebuilt"
	PackageDirectPrebuilt   PackageType = "direct_prebuilt"
	PackageISOInstaller     PackageType = "iso_installer"
)

// IsValid reports whether p is a known package type.
func (p PackageType) IsValid() bool {
	switch p {
	case PackageProvider, PackageArchiveInstaller, PackageArchivePrebuilt,
		PackageDirectPrebuilt, PackageISOInstaller:
		return true
	default:
		return false
	}
}

// IsPrebuilt reports whether the package is already a usable game
// (no installer stage may be invented for these modes).
func (p PackageType) IsPrebuilt() bool {
	return p == PackageArchivePrebuilt || p == PackageDirectPrebuilt
}

// IsSingleLink reports whether exactly one source is expected.
func (p PackageType) IsSingleLink() bool {
	return p == PackageArchivePrebuilt || p == PackageDirectPrebuilt
}

// DetectPackageType sniffs a package type from a filename. Helper only:
// the manifest's package_type always wins over this guess.
func DetectPackageType(filename string) PackageType {
	lower := ""
	for _, r := range filename {
		if r >= 'A' && r <= 'Z' {
			lower += string(r + ('a' - 'A'))
		} else {
			lower += string(r)
		}
	}
	switch {
	case hasSuffix(lower, ".iso"):
		return PackageISOInstaller
	case hasSuffix(lower, ".exe") || hasSuffix(lower, ".msi"):
		return PackageDirectPrebuilt
	case hasSuffix(lower, ".zip") || hasSuffix(lower, ".rar") || hasSuffix(lower, ".7z"):
		return PackageArchivePrebuilt
	default:
		return ""
	}
}

func hasSuffix(s, suf string) bool {
	if len(s) < len(suf) {
		return false
	}
	return s[len(s)-len(suf):] == suf
}

// AcquisitionRef names a provider; Reference is provider-specific opaque data.
// Since manifest v2 the ordered multipart source list lives in Sources.
// Since v3 the explicit PackageType selects the preparation mode.
type AcquisitionRef struct {
	Provider    string              `json:"provider"` // "archive" | "steamcmd" | "legendary" | "gog" | ...
	Reference   map[string]any      `json:"reference,omitempty"`
	Sources     []AcquisitionSource `json:"sources,omitempty"`
	PackageType PackageType         `json:"package_type,omitempty"`
}

// AcquisitionSource is one part of a dynamic 1..N authorized source list.
// Part numbers are explicit: filenames on hosts may be imperfect.
type AcquisitionSource struct {
	URL       string `json:"url"`
	Part      int    `json:"part"`
	Filename  string `json:"filename"`
	SHA256    string `json:"sha256,omitempty"`
	SizeBytes uint64 `json:"size_bytes,omitempty"`
}

// PackagePipeline describes archive -> ISO -> installer stages explicitly
// instead of hardcoding ZIP -> ISO -> EXE. Nil sections mean "skip stage".
// Prebuilt modes use Prebuilt and must NOT declare an Installer.
type PackagePipeline struct {
	Archive   *ArchiveSpec   `json:"archive,omitempty"`
	Result    *ResultSpec    `json:"result,omitempty"`
	ISO       *IsoSpec       `json:"iso,omitempty"`
	Installer *InstallerSpec `json:"installer,omitempty"`
	Prebuilt  *PrebuiltSpec  `json:"prebuilt,omitempty"`
}

// PrebuiltSpec describes an already-usable game: where its root lands after
// extraction (or copy for direct EXE) and which files must exist.
type PrebuiltSpec struct {
	GameRoot      string   `json:"game_root,omitempty"` // subdir under /games/<id>; "" = /games/<id>
	ExpectedFiles []string `json:"expected_files,omitempty"`
}

// ArchiveSpec describes the downloaded archive set.
type ArchiveSpec struct {
	Type      string `json:"type"` // "zip" | "rar" | "7z"
	Multipart bool   `json:"multipart"`
}

// ResultSpec describes what archive reconstruction must yield.
type ResultSpec struct {
	Type string `json:"type"` // "iso" | "dir"
	Path string `json:"path"`
}

// IsoSpec controls ISO handling.
type IsoSpec struct {
	Extract bool `json:"extract"`
}

// InstallerSpec describes a legitimately-executed installer. The manifest is
// data, never a shell script: path must resolve inside the staging root,
// arguments are a fixed argv (no shell), and execution has a timeout.
type InstallerSpec struct {
	Type        string   `json:"type"` // "exe" | "msi" | "inno" | ...
	Path        string   `json:"path"`
	Arguments   []string `json:"arguments,omitempty"`
	TimeoutSecs int      `json:"timeout_seconds,omitempty"`
	ExpectedDir string   `json:"expected_dir,omitempty"`
}

// CleanupPolicy gates which stage inputs may be deleted after the next stage
// verifies. Sources are only removed once their consumer stage is confirmed.
type CleanupPolicy struct {
	DeletePartsAfterArchiveExtract  bool `json:"delete_parts_after_archive_extract"`
	DeleteISOAfterExtract           bool `json:"delete_iso_after_extract"`
	DeleteInstallerAfterInstall     bool `json:"delete_installer_after_install"`
	DeleteArchiveAfterPrebuiltReady bool `json:"delete_archive_after_prebuilt_ready"`
}

// Footprint sizes in bytes (0 = unknown).
type Footprint struct {
	DownloadBytes  uint64 `json:"download_bytes"`
	PeakTempBytes  uint64 `json:"peak_temp_bytes"`
	InstalledBytes uint64 `json:"installed_bytes"`
	SafetyHeadroom uint64 `json:"safety_headroom"`
	ISOBytes       uint64 `json:"iso_bytes,omitempty"`
	InstallerBytes uint64 `json:"installer_bytes,omitempty"`
}

// RequiredBytes = download + peak temp + installed + headroom.
// Legacy aggregate; prefer RequiredFor for package-type-aware planning.
func (f Footprint) RequiredBytes() uint64 {
	return f.DownloadBytes + f.PeakTempBytes + f.InstalledBytes + f.SafetyHeadroom +
		f.ISOBytes + f.InstallerBytes
}

// RequiredFor plans disk per package type instead of blindly applying the
// multipart formula to every mode.
func (f Footprint) RequiredFor(pt PackageType) uint64 {
	base := f.DownloadBytes + f.PeakTempBytes + f.InstalledBytes + f.SafetyHeadroom
	switch pt {
	case PackageArchiveInstaller, PackageISOInstaller:
		return base + f.ISOBytes + f.InstallerBytes
	case PackageArchivePrebuilt:
		return base
	case PackageDirectPrebuilt:
		// Single file: download, plus final storage if staged elsewhere.
		return f.DownloadBytes + f.InstalledBytes + f.SafetyHeadroom
	case PackageProvider:
		// Provider-managed: installation dominates; download only if declared.
		return f.DownloadBytes + f.InstalledBytes + f.SafetyHeadroom
	default:
		return f.RequiredBytes()
	}
}

// RuntimeSpec constrains where the game may run.
type RuntimeSpec struct {
	OS            string   `json:"os"`
	Compatibility string   `json:"compatibility,omitempty"` // e.g. "wine-ge-8", "native"
	Dependencies  []string `json:"dependencies,omitempty"`
}

// LaunchSpec is deliberately narrow: one supervised process, no shell.
// WolfApp names the Wolf application (config.toml title) that serves this
// game when the Wolf backend is used; empty means host-process launch.
type LaunchSpec struct {
	Executable       string   `json:"executable"`
	Arguments        []string `json:"arguments,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	WolfApp          string   `json:"wolf_app,omitempty"`
}

// SaveSpec points at Ludusavi by default with optional overrides.
// LudusaviTitle names the game as Ludusavi knows it; when set and the
// ludusavi binary is present, snapshot/restore shell out to
// `ludusavi backup|restore --force --path <stage> <title>` and our
// generation/fencing pipeline wraps the result. Overrides remain the
// fallback (and the only path when Ludusavi is absent).
type SaveSpec struct {
	Provider      string         `json:"provider"` // "ludusavi"
	Overrides     []SaveOverride `json:"overrides,omitempty"`
	LudusaviTitle string         `json:"ludusavi_title,omitempty"`
}

// SaveOverride handles games with unusual save layouts.
type SaveOverride struct {
	Platform string   `json:"platform"`
	Paths    []string `json:"paths"`
}

// Validate checks structural invariants of the manifest.
// Accepts schema_version 1 (Stage 0), 2 (pipeline), 3 (package types).
func (m *GameManifest) Validate() error {
	if m.SchemaVersion != 1 && m.SchemaVersion != 2 && m.SchemaVersion != ManifestSchemaVersion {
		return errors.New("protocol: unsupported manifest schema version")
	}
	if m.GameID == "" || m.Name == "" {
		return errors.New("protocol: manifest requires game_id and name")
	}
	if m.Acquisition.Provider == "" {
		return errors.New("protocol: manifest requires acquisition.provider")
	}
	if m.Launch.Executable == "" {
		return errors.New("protocol: manifest requires launch.executable")
	}
	if m.SchemaVersion >= 2 {
		if err := validateSources(m); err != nil {
			return err
		}
		if m.PackagePipeline != nil && m.PackagePipeline.Installer != nil {
			if m.PackagePipeline.Installer.Path == "" {
				return errors.New("protocol: installer requires path")
			}
		}
	}
	if m.SchemaVersion >= 3 {
		if err := validatePackageType(m); err != nil {
			return err
		}
	}
	return nil
}

// validateSources checks the 1..N source list (shared by v2 and v3).
func validateSources(m *GameManifest) error {
	if len(m.Acquisition.Sources) == 0 && m.Acquisition.Provider == "archive" {
		return errors.New("protocol: archive acquisition requires at least one source")
	}
	seen := map[int]bool{}
	for _, s := range m.Acquisition.Sources {
		if s.URL == "" || s.Filename == "" || s.Part < 1 {
			return errors.New("protocol: each source needs url, filename, part >= 1")
		}
		if seen[s.Part] {
			return errors.New("protocol: duplicate source part number")
		}
		seen[s.Part] = true
	}
	return nil
}

// validatePackageType enforces v3 mode rules. The manifest stays data:
// modes select pipeline behavior, never shell commands.
func validatePackageType(m *GameManifest) error {
	pt := m.Acquisition.PackageType
	if pt == "" {
		// v3 manifests migrated from v2 without an explicit mode default to
		// the classic multipart-installer flow only when it is coherent.
		if m.Acquisition.Provider == "archive" {
			return errors.New("protocol: v3 archive manifests require explicit package_type")
		}
		return nil
	}
	if !pt.IsValid() {
		return errors.New("protocol: unknown package_type " + string(pt))
	}
	if pt.IsSingleLink() && len(m.Acquisition.Sources) != 1 {
		return errors.New("protocol: single-link package_type requires exactly one source")
	}
	pipe := m.PackagePipeline
	if pt.IsPrebuilt() {
		// Never invent an installer stage for prebuilt packages.
		if pipe != nil && pipe.Installer != nil {
			return errors.New("protocol: prebuilt package_type must not declare an installer")
		}
		if pipe != nil && (pipe.ISO != nil || (pipe.Result != nil && pipe.Result.Type == "iso")) {
			return errors.New("protocol: prebuilt package_type must not declare ISO stages")
		}
		if pt == PackageDirectPrebuilt && pipe != nil && pipe.Archive != nil {
			return errors.New("protocol: direct_prebuilt takes a single file, not an archive stage")
		}
		return nil
	}
	switch pt {
	case PackageArchiveInstaller, PackageISOInstaller:
		if pipe == nil || pipe.Installer == nil {
			return errors.New("protocol: installer package_type requires package_pipeline.installer")
		}
	case PackageProvider:
		// Provider-managed installation: no local pipeline required.
	}
	return nil
}

// ---------------------------------------------------------------------------
// Saves
// ---------------------------------------------------------------------------

// SaveState is the lifecycle of one immutable save generation.
type SaveState string

const (
	SavePending    SaveState = "PENDING"
	SaveValid      SaveState = "VALID"
	SaveCheckpoint SaveState = "CHECKPOINT"
	SaveOrphaned   SaveState = "ORPHANED"
	SaveCorrupt    SaveState = "CORRUPT"
)

// SaveSnapshot is the metadata row (PostgreSQL authoritative). The blob lives
// at saves/{user}/{game}/{generation}/blob in object storage.
type SaveSnapshot struct {
	SchemaVersion    int       `json:"schema_version"`
	UserID           string    `json:"user_id"`
	GameID           string    `json:"game_id"`
	Generation       uint64    `json:"generation"`
	SHA256           string    `json:"sha256"`
	SizeBytes        uint64    `json:"size_bytes"`
	FileCount        int       `json:"file_count"`
	NodeID           string    `json:"node_id"`
	SessionID        string    `json:"session_id"`
	CreatedAt        time.Time `json:"created_at"`
	State            SaveState `json:"state"`
	ParentGeneration *uint64   `json:"parent_generation,omitempty"`
}

// Validate checks structural invariants.
func (s *SaveSnapshot) Validate() error {
	if s.SchemaVersion != SaveSchemaVersion {
		return errors.New("protocol: unsupported save schema version")
	}
	if s.UserID == "" || s.GameID == "" {
		return errors.New("protocol: save requires user_id and game_id")
	}
	if s.Generation == 0 {
		return errors.New("protocol: save generation must be >= 1")
	}
	if len(s.SHA256) != 64 {
		return errors.New("protocol: save sha256 must be 64 hex chars")
	}
	return nil
}

// ObjectKey returns the canonical blob key (no user input interpolated raw;
// callers must still validate user/game ids against an allowlist pattern).
func (s *SaveSnapshot) ObjectKey() string {
	return "saves/" + s.UserID + "/" + s.GameID + "/" + uitoa(s.Generation) + "/blob"
}

// ManifestKey returns the canonical manifest key for the generation.
func (s *SaveSnapshot) ManifestKey() string {
	return "saves/" + s.UserID + "/" + s.GameID + "/" + uitoa(s.Generation) + "/manifest.json"
}

func uitoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---------------------------------------------------------------------------
// Node control channel: fixed command enum (no arbitrary EXEC).
// ---------------------------------------------------------------------------

// NodeCommand is the closed set of instructions the control plane may send.
type NodeCommand string

const (
	CmdAcquire   NodeCommand = "ACQUIRE"
	CmdRestore   NodeCommand = "RESTORE"
	CmdLaunch    NodeCommand = "LAUNCH"
	CmdSnapshot  NodeCommand = "SNAPSHOT"
	CmdTerminate NodeCommand = "TERMINATE"
	CmdEvict     NodeCommand = "EVICT"
)

// IsValid reports whether c is a known command.
func (c NodeCommand) IsValid() bool {
	switch c {
	case CmdAcquire, CmdRestore, CmdLaunch, CmdSnapshot, CmdTerminate, CmdEvict:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Node enrollment / heartbeat (Stage 1: outbound from node, leases + fence)
// ---------------------------------------------------------------------------

// EnrollRequest is sent once at startup with fresh capabilities.
type EnrollRequest struct {
	NodeID      string       `json:"node_id"`
	EnrollToken string       `json:"enroll_token,omitempty"`
	Caps        Capabilities `json:"caps"`
}

// EnrollResponse carries the node's first fence token and lease TTL.
type EnrollResponse struct {
	NodeID       string `json:"node_id"`
	FenceToken   uint64 `json:"fence_token"`
	LeaseTTLSecs int    `json:"lease_ttl_secs"`
}

// HeartbeatRequest refreshes the lease.
type HeartbeatRequest struct {
	NodeID     string        `json:"node_id"`
	FenceToken uint64        `json:"fence_token"`
	State      NodeState     `json:"state"`
	Caps       *Capabilities `json:"caps,omitempty"`
}

// HeartbeatResponse renews the lease or reports fencing.
type HeartbeatResponse struct {
	FenceToken   uint64 `json:"fence_token"`
	LeaseTTLSecs int    `json:"lease_ttl_secs"`
	Fenced       bool   `json:"fenced"`
}

// StreamEndpoint tells the client shell (and Moonlight) where to connect.
// The control plane issues it; video never flows through the control plane.
type StreamEndpoint struct {
	Backend string `json:"backend"` // "sunshine" | "wolf"
	Host    string `json:"host"`
	Port    int    `json:"port"`
}

// ---------------------------------------------------------------------------
// Acquisition pipeline states (restartable, observable, persisted as JSON)
// ---------------------------------------------------------------------------

// PipelineState is one explicit stage of game preparation. Any stage may
// transition to FAILED with a structured reason; success flows linearly.
type PipelineState string

const (
	PipePlanned          PipelineState = "PLANNED"
	PipeSpaceChecked     PipelineState = "SPACE_CHECKED"
	PipeDownloading      PipelineState = "DOWNLOADING"
	PipeDownloadVerified PipelineState = "DOWNLOAD_VERIFIED"
	PipeArchiveReady     PipelineState = "ARCHIVE_READY"
	PipeArchiveExtracted PipelineState = "ARCHIVE_EXTRACTED"
	PipeSourceCleaned    PipelineState = "SOURCE_CLEANED"
	PipeISOReady         PipelineState = "ISO_READY"
	PipeISOExtracted     PipelineState = "ISO_EXTRACTED"
	PipeISOCleaned       PipelineState = "ISO_CLEANED"
	PipeInstallerReady   PipelineState = "INSTALLER_READY"
	PipeInstalling       PipelineState = "INSTALLING"
	PipeInstallValidated PipelineState = "INSTALL_VALIDATED"
	PipeInstallerCleaned PipelineState = "INSTALLER_CLEANED"
	PipeReady            PipelineState = "READY"
	PipeFailed           PipelineState = "FAILED"
)

// ValidPipelineTransitions is the authoritative acquisition transition table.
// Skippable stages (no ISO in manifest, no installer) may jump forward to
// the next applicable stage; FAILED is reachable from any non-terminal state.
var ValidPipelineTransitions = map[PipelineState][]PipelineState{
	PipePlanned:          {PipeSpaceChecked, PipeFailed},
	PipeSpaceChecked:     {PipeDownloading, PipeFailed},
	PipeDownloading:      {PipeDownloadVerified, PipeFailed},
	PipeDownloadVerified: {PipeArchiveReady, PipeInstallerReady, PipeReady, PipeFailed},
	PipeArchiveReady:     {PipeArchiveExtracted, PipeFailed},
	PipeArchiveExtracted: {PipeSourceCleaned, PipeFailed},
	PipeSourceCleaned:    {PipeISOReady, PipeInstallerReady, PipeReady, PipeFailed},
	PipeISOReady:         {PipeISOExtracted, PipeFailed},
	PipeISOExtracted:     {PipeISOCleaned, PipeFailed},
	PipeISOCleaned:       {PipeInstallerReady, PipeReady, PipeFailed},
	PipeInstallerReady:   {PipeInstalling, PipeFailed},
	PipeInstalling:       {PipeInstallValidated, PipeFailed},
	PipeInstallValidated: {PipeInstallerCleaned, PipeReady, PipeFailed},
	PipeInstallerCleaned: {PipeReady, PipeFailed},
	PipeReady:            {},
	PipeFailed:           {PipePlanned}, // explicit retry re-plans
}

// CanTransition reports whether from -> to is a legal pipeline transition.
func (s PipelineState) CanTransition(to PipelineState) bool {
	for _, n := range ValidPipelineTransitions[s] {
		if n == to {
			return true
		}
	}
	return false
}

// IsTerminal reports whether preparation stopped (success or failure).
func (s PipelineState) IsTerminal() bool {
	return s == PipeReady || s == PipeFailed
}
