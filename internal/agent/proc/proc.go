// Package proc supervises OS processes for the node agent: game processes,
// installers, and streaming backends. One supervised process per session,
// fixed argv (never a shell), enforced timeouts, captured output, and
// verified exit codes. No result is reported successful merely because a
// command was launched.
package proc

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Result records a completed process.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
}

// Run starts argv[0] with args (no shell expansion), waits up to timeout,
// kills the process group on expiry, and returns the observed result.
// A zero timeout means no timeout.
func Run(ctx context.Context, name string, args []string, dir string, timeout time.Duration) Result {
	var res Result
	if name == "" {
		res.ExitCode = -1
		res.Stderr = "proc: empty executable"
		return res
	}
	cctx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		cctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(cctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	// Bound memory: keep only the tail of huge outputs.
	cmd.Stdout = &limitWriter{W: &stdout, N: 256 * 1024}
	cmd.Stderr = &limitWriter{W: &stderr, N: 256 * 1024}
	err := cmd.Run()
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	switch {
	case cctx.Err() == context.DeadlineExceeded:
		res.TimedOut = true
		res.ExitCode = -1
	case err == nil:
		res.ExitCode = 0
	default:
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		} else {
			res.ExitCode = -1
			if res.Stderr == "" {
				res.Stderr = fmt.Sprintf("proc: start failed: %v", err)
			}
		}
	}
	return res
}

// Supervised is a started, still-tracked process (game sessions).
type Supervised struct {
	cmd *exec.Cmd
}

// Start launches argv[0] with args in dir without waiting. The caller owns
// Wait/Kill. Stdout/stderr tails are captured for crash reports.
func Start(name string, args []string, dir string) (*Supervised, error) {
	if name == "" {
		return nil, fmt.Errorf("proc: empty executable")
	}
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = &limitWriter{W: &bytes.Buffer{}, N: 256 * 1024}
	cmd.Stderr = &limitWriter{W: &bytes.Buffer{}, N: 256 * 1024}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Supervised{cmd: cmd}, nil
}

// Pid returns the OS process id, or -1 if not started.
func (s *Supervised) Pid() int {
	if s.cmd == nil || s.cmd.Process == nil {
		return -1
	}
	return s.cmd.Process.Pid
}

// Kill terminates the process.
func (s *Supervised) Kill() error {
	if s.cmd == nil || s.cmd.Process == nil {
		return nil
	}
	return s.cmd.Process.Kill()
}

// Wait reaps the process and reports its exit code.
func (s *Supervised) Wait() int {
	if s.cmd == nil {
		return -1
	}
	if err := s.cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return -1
	}
	return 0
}

// limitWriter keeps only the last N bytes written.
type limitWriter struct {
	W *bytes.Buffer
	N int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	n, _ := l.W.Write(p)
	if l.W.Len() > l.N {
		l.W.Next(l.W.Len() - l.N)
	}
	return n, nil
}
