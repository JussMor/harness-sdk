package main

// OpenSandbox startup health-check + auto-launch.
//
// Goal: when the backend boots, verify the sandbox HTTP API is reachable.
// If not, optionally spawn the local start.sh script in the background and
// poll until /health responds. If it never comes up, log a clear warning so
// sandbox-dependent tools fail loudly with a helpful message instead of an
// opaque "connection refused".
//
// Behaviour matrix:
//
//	OPEN_SANDBOX_API_KEY unset            → skip everything (sandbox disabled).
//	OPEN_SANDBOX_AUTO_START=0             → only health-check, never launch.
//	OPEN_SANDBOX_AUTO_START=1 (default)   → if /health fails, launch start.sh.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ensureOpenSandbox blocks for up to ~30s trying to reach the sandbox HTTP
// server. When the API key is missing it is a no-op. Errors are logged but
// not returned: a missing sandbox should not stop the backend from booting,
// since chat-only flows (no tool calls) still work.
func ensureOpenSandbox() {
	apiKey := strings.TrimSpace(os.Getenv("OPEN_SANDBOX_API_KEY"))
	if apiKey == "" {
		return
	}

	domain := getenv("OPEN_SANDBOX_DOMAIN", "localhost:8080")
	protocol := getenv("OPEN_SANDBOX_PROTOCOL", "http")
	healthURL := fmt.Sprintf("%s://%s/health", protocol, domain)

	if probeSandboxHealth(healthURL, 1500*time.Millisecond) {
		logf("opensandbox: healthy at %s", healthURL)
		return
	}

	autoStart := getenv("OPEN_SANDBOX_AUTO_START", "1") != "0"
	if !autoStart {
		logf("opensandbox: NOT REACHABLE at %s — sandbox tools will fail (set OPEN_SANDBOX_AUTO_START=1 to launch automatically)", healthURL)
		return
	}

	scriptPath := findStartScript()
	if scriptPath == "" {
		logf("opensandbox: NOT REACHABLE at %s and start.sh not found — sandbox tools will fail", healthURL)
		return
	}

	logf("opensandbox: down — launching %s", scriptPath)
	if err := launchStartScript(scriptPath); err != nil {
		logf("opensandbox: failed to launch start.sh: %v", err)
		return
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if probeSandboxHealth(healthURL, 1*time.Second) {
			logf("opensandbox: healthy at %s (started by backend)", healthURL)
			return
		}
		time.Sleep(1 * time.Second)
	}
	logf("opensandbox: still NOT REACHABLE at %s after 30s — sandbox tools will fail. Inspect logs at /tmp/opensandbox.log", healthURL)
}

// probeSandboxHealth issues a GET /health and returns true on 2xx.
func probeSandboxHealth(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// findStartScript locates sandbox-files-init/start.sh relative to the binary.
// Walks up from the working directory so it works whether the backend is run
// from example/backend-chat or from the repo root.
func findStartScript() string {
	candidates := []string{
		"sandbox-files-init/start.sh",
		"example/backend-chat/sandbox-files-init/start.sh",
	}
	for _, c := range candidates {
		if abs, err := filepath.Abs(c); err == nil {
			if info, err := os.Stat(abs); err == nil && !info.IsDir() {
				return abs
			}
		}
	}
	return ""
}

// launchStartScript runs start.sh detached, redirecting output to a log file.
// We do not wait for it to exit; ensureOpenSandbox polls /health instead.
func launchStartScript(path string) error {
	logFile, err := os.OpenFile("/tmp/opensandbox.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	cmd := exec.Command("bash", path)
	cmd.Dir = filepath.Dir(path)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start: %w", err)
	}
	// Detach: we intentionally do not Wait(). The OS will reap it.
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
	}()
	return nil
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[backend-chat] "+format+"\n", args...)
}

// _ keeps the context import live in case future callers need a deadline.
var _ = context.Background
