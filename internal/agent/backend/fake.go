package backend

import (
	"fmt"
	"sync"
)

// FakeBackend is an in-memory GamingBackend for tests. It is explicitly
// MOCK: production constructors never return it, and Probe says so.
type FakeBackend struct {
	mu      sync.Mutex
	running map[string]bool
	// FailStart, when set, makes Start return an error (fault injection).
	FailStart error
	// AppearRunning makes Status report running without Start (e.g. to
	// simulate Moonlight connecting through a real server in live tests).
	AppearRunning map[string]bool
}

// NewFakeBackend builds a MOCK backend. Test-only by convention; Probe
// advertises MOCK so misuse is visible.
func NewFakeBackend() *FakeBackend {
	return &FakeBackend{running: map[string]bool{}, AppearRunning: map[string]bool{}}
}

// Name implements GamingBackend.
func (b *FakeBackend) Name() string { return "fake" }

// Probe implements GamingBackend.
func (b *FakeBackend) Probe() Availability {
	return Availability{Mode: ModeMock, Reason: "in-memory fake"}
}

// Start implements GamingBackend.
func (b *FakeBackend) Start(g Game) error {
	if b.FailStart != nil {
		return b.FailStart
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.running[g.Manifest.GameID] = true
	return nil
}

// Status implements GamingBackend.
func (b *FakeBackend) Status(g Game) (Status, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running[g.Manifest.GameID] || b.AppearRunning[g.Manifest.GameID] {
		return Status{Running: true, Detail: "fake running", BackendSessID: "fake-1", ExitCode: -1}, nil
	}
	return Status{Running: false, Detail: "fake stopped", ExitCode: 0}, nil
}

// Stop implements GamingBackend.
func (b *FakeBackend) Stop(g Game) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.running, g.Manifest.GameID)
	return nil
}

// Connection implements GamingBackend.
func (b *FakeBackend) Connection(g Game) (ConnInfo, error) {
	if g.Host == "" {
		return ConnInfo{}, fmt.Errorf("fake: no host")
	}
	return ConnInfo{Backend: "fake", Host: g.Host, App: AppTitle(g.Manifest),
		MoonlightArgs: MoonlightArgs(g.Host, AppTitle(g.Manifest))}, nil
}
