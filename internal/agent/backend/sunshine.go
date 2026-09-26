package backend

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

// SunshineBackend launches the game as a supervised host process under
// Sunshine's stream (Sunshine streams the desktop/session it is configured
// for). Process truth comes from the OS: running, exit code, crash.
type SunshineBackend struct {
	mu    sync.Mutex
	procs map[string]*trackedProc
}

type trackedProc struct {
	proc     *exec.Cmd
	done     chan struct{}
	exitCode int
}

// NewSunshineBackend builds a REAL host-process backend.
func NewSunshineBackend() *SunshineBackend {
	return &SunshineBackend{procs: map[string]*trackedProc{}}
}

// Name implements GamingBackend.
func (b *SunshineBackend) Name() string { return "sunshine" }

// Probe implements GamingBackend: the sunshine binary must exist.
func (b *SunshineBackend) Probe() Availability {
	if _, err := exec.LookPath("sunshine"); err != nil {
		return Availability{Mode: ModeUnavailable,
			Reason: "sunshine binary not found (install Sunshine: https://app.lizardbyte.dev/Sunshine)"}
	}
	return Availability{Mode: ModeReal}
}

// Start implements GamingBackend: validates the executable, launches it
// supervised, and verifies the process actually started. One game per id;
// an already-running game is left alone (idempotent start).
func (b *SunshineBackend) Start(g Game) error {
	if g.ExePath == "" {
		return fmt.Errorf("sunshine: no validated executable path (prepare the game first)")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.procs[g.Manifest.GameID]; ok {
		select {
		case <-t.done:
			delete(b.procs, g.Manifest.GameID)
		default:
			return nil // already running
		}
	}
	cmd := exec.Command(g.ExePath, g.Manifest.Launch.Arguments...)
	if g.WorkDir != "" {
		cmd.Dir = g.WorkDir
	}
	if g.GPUIndex >= 0 {
		// Pin the game's visible NVIDIA device (honors PG_GPU_INDEX).
		cmd.Env = append(os.Environ(),
			"CUDA_VISIBLE_DEVICES="+strconv.Itoa(g.GPUIndex),
			"NVIDIA_VISIBLE_DEVICES="+strconv.Itoa(g.GPUIndex))
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sunshine: game process failed to start: %w", err)
	}
	t := &trackedProc{proc: cmd, done: make(chan struct{}), exitCode: -1}
	b.procs[g.Manifest.GameID] = t
	go func() {
		defer close(t.done)
		if err := cmd.Wait(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				t.exitCode = ee.ExitCode()
				return
			}
			t.exitCode = -1
			return
		}
		t.exitCode = 0
	}()
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		delete(b.procs, g.Manifest.GameID)
		return fmt.Errorf("sunshine: process did not start")
	}
	return nil
}

// Status implements GamingBackend: true only while the OS process lives.
func (b *SunshineBackend) Status(g Game) (Status, error) {
	b.mu.Lock()
	t, ok := b.procs[g.Manifest.GameID]
	b.mu.Unlock()
	if !ok {
		return Status{Running: false, Detail: "no game process", ExitCode: -1}, nil
	}
	select {
	case <-t.done:
		return Status{Running: false, Detail: "game process exited",
			BackendSessID: fmt.Sprintf("pid=%d", t.proc.Process.Pid), ExitCode: t.exitCode}, nil
	default:
		return Status{Running: true, Detail: "game process running",
			BackendSessID: fmt.Sprintf("pid=%d", t.proc.Process.Pid), ExitCode: -1}, nil
	}
}

// Stop implements GamingBackend: kills the supervised process (no-op
// when absent). Fenced/terminated sessions always land here.
func (b *SunshineBackend) Stop(g Game) error {
	b.mu.Lock()
	t, ok := b.procs[g.Manifest.GameID]
	if ok {
		delete(b.procs, g.Manifest.GameID)
	}
	b.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case <-t.done:
		return nil
	default:
		if t.proc.Process == nil {
			return nil
		}
		if err := t.proc.Process.Kill(); err != nil {
			return fmt.Errorf("sunshine: kill failed: %w", err)
		}
		<-t.done
		return nil
	}
}

// Connection implements GamingBackend: Moonlight streams Sunshine's
// configured desktop/app; the game runs inside it.
func (b *SunshineBackend) Connection(g Game) (ConnInfo, error) {
	app := "Desktop"
	return ConnInfo{Backend: "sunshine", Host: g.Host, App: app, MoonlightArgs: MoonlightArgs(g.Host, app)}, nil
}
