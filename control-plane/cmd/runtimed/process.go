package main

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	// A dev-server run shorter than this counts as a fast failure.
	fastFailWindow = 10 * time.Second
	// After this many consecutive fast failures the supervisor stops
	// restarting (a hopelessly broken app — reported down, not
	// crash-looped).
	maxFastFails = 5
	maxBackoff   = 30 * time.Second
)

// process.go supervises a single long-running child command inside the
// sandbox. runtimed manages one process per port it needs to keep alive
// (today: the user's dev server on 3000, the agents-ui Nuxt server on
// 3001). Semantics are unchanged from the original devServer supervisor:
// one child at a time, exponential backoff on unexpected exit, abandoned
// after repeated fast failures.
type process struct {
	name    string // short identifier used in /status and log fields
	appDir  string // working directory for the child
	command string // shell command, run via `bash -lc`
	port    int    // informational; used by /status and the wake probe loop
	logPath string
	log     *slog.Logger

	mu       sync.Mutex
	proc     *os.Process
	running  bool
	restarts int
}

func newProcess(name, appDir, command string, port int, logPath string, log *slog.Logger) *process {
	return &process{name: name, appDir: appDir, command: command, port: port, logPath: logPath, log: log}
}

// supervise is the dev server's whole lifecycle; it runs until ctx is
// cancelled (runtimed shutdown).
func (p *process) supervise(ctx context.Context) {
	fastFails := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		p.runOnce()
		if ctx.Err() != nil {
			return // intentional shutdown — do not restart
		}
		p.mu.Lock()
		p.restarts++
		restarts := p.restarts
		p.mu.Unlock()
		if time.Since(start) < fastFailWindow {
			fastFails++
		} else {
			fastFails = 0
		}
		if fastFails >= maxFastFails {
			p.log.Error("process failing repeatedly — giving up until next start",
				"process", p.name,
				"restarts", restarts)
			return
		}
		delay := backoff(fastFails)
		p.log.Warn("process exited; restarting after backoff",
			"process", p.name,
			"delay", delay.String(), "restarts", restarts)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

// runOnce starts the dev server, records it as live, and blocks until
// it exits.
func (p *process) runOnce() {
	// `bash -lc` so the login PATH (pnpm, node) is in scope.
	cmd := exec.Command("bash", "-lc", p.command)
	cmd.Dir = p.appDir
	// Own process group so the whole `bash → pnpm → node → vite` tree
	// can be signalled as a unit on shutdown.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if f, err := os.Create(p.logPath); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	} else {
		p.log.Warn("open process log", "process", p.name, "path", p.logPath, "err", err.Error())
	}
	if err := cmd.Start(); err != nil {
		p.log.Error("process start failed", "process", p.name, "err", err.Error())
		return
	}
	p.mu.Lock()
	p.proc = cmd.Process
	p.running = true
	p.mu.Unlock()
	p.log.Info("process started", "process", p.name, "pid", cmd.Process.Pid)

	_ = cmd.Wait()

	p.mu.Lock()
	p.proc = nil
	p.running = false
	p.mu.Unlock()
	p.log.Info("process exited", "process", p.name)
}

// stop terminates the dev server's process group: SIGTERM, then
// SIGKILL if it has not exited within the grace window.
func (p *process) stop() {
	p.mu.Lock()
	proc := p.proc
	p.mu.Unlock()
	if proc == nil {
		return
	}
	pgid := proc.Pid // == process group id (Setpgid made it the leader)
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	for i := 0; i < 50; i++ { // up to ~5s
		time.Sleep(100 * time.Millisecond)
		p.mu.Lock()
		running := p.running
		p.mu.Unlock()
		if !running {
			return
		}
	}
	p.log.Warn("process did not exit on SIGTERM; sending SIGKILL", "process", p.name)
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// status returns a snapshot of the process's runtime state for /status.
func (p *process) status() (running bool, restarts int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running, p.restarts
}

// snapshot returns the current dev-server state for GET /status.
func (p *process) snapshot() (pid, restarts int, running bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.proc != nil {
		pid = p.proc.Pid
	}
	return pid, p.restarts, p.running
}

// backoff is exponential in the consecutive-fast-failure count,
// capped at maxBackoff.
func backoff(fastFails int) time.Duration {
	if fastFails < 1 {
		fastFails = 1
	}
	d := time.Second << (fastFails - 1)
	if d <= 0 || d > maxBackoff {
		return maxBackoff
	}
	return d
}
