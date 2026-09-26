// Package backend abstracts the gaming backend behind one narrow interface.
//
// Implementations: WolfBackend (real, Unix-socket API), SunshineBackend
// (real, host-process launch under Sunshine's stream), FakeBackend
// (explicit MOCK for tests). Production code always reports which mode it
// runs in: REAL, MOCK, or UNAVAILABLE with an actionable reason. Fakes are
// never silently substituted.
package backend

import (
	"github.com/personal-game/personal-game/pkg/protocol"
)

// Mode classifies an implementation instance.
type Mode string

const (
	// ModeReal performs actual operations against real software.
	ModeReal Mode = "REAL"
	// ModeMock simulates for tests; never used in production paths.
	ModeMock Mode = "MOCK"
	// ModeUnavailable means the backend cannot run here (with reason).
	ModeUnavailable Mode = "UNAVAILABLE"
)

// Availability is a probed capability statement.
type Availability struct {
	Mode   Mode
	Reason string // empty when REAL/MOCK and healthy
}

// Game describes what to run: the manifest plus resolved paths.
type Game struct {
	Manifest protocol.GameManifest
	ExePath  string // validated launch target (host-process modes)
	WorkDir  string
	Host     string // node address Moonlight connects to
	// GPUIndex pins the visible NVIDIA device (-1 = unset, all visible).
	GPUIndex int
}

// ConnInfo is everything Moonlight needs: backend, host, app title, and
// the exact verified CLI invocation (moonlight stream <host> "<app>").
type ConnInfo struct {
	Backend       string
	Host          string
	App           string
	MoonlightArgs []string
}

// Status reports backend-observed runtime state.
type Status struct {
	Running       bool
	Detail        string
	BackendSessID string // Wolf session id / process id, for stop/crash reports
	ExitCode      int    // process exit code when known (-1 = unknown/running)
}

// GamingBackend is the narrow seam between orchestration and streaming.
// The session layer never touches Wolf/Sunshine specifics through this.
type GamingBackend interface {
	Name() string
	// Probe reports REAL/MOCK/UNAVAILABLE without side effects.
	Probe() Availability
	// Start makes the game streamable (Wolf: verify app+pairing, connection
	// info; Sunshine: launch supervised process). Verifies readiness.
	Start(Game) error
	// Status observes actual runtime state (Wolf sessions / process alive).
	Status(Game) (Status, error)
	// Stop terminates streaming/playing. Idempotent: absent game is success.
	Stop(Game) error
	// Connection returns Moonlight connection info. No admin APIs leak.
	Connection(Game) (ConnInfo, error)
}

// MoonlightArgs builds the verified CLI invocation:
// moonlight stream <host> "<app>" (see moonlight-qt commandlineparser).
func MoonlightArgs(host, app string) []string {
	return []string{"stream", host, app}
}

// AppTitle resolves the Moonlight-visible app name: explicit Wolf app,
// else the manifest game name.
func AppTitle(m protocol.GameManifest) string {
	if m.Launch.WolfApp != "" {
		return m.Launch.WolfApp
	}
	return m.Name
}
