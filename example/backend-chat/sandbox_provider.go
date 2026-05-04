package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	opensandbox "github.com/alibaba/OpenSandbox/sdks/sandbox/go"
	ab "github.com/everfaz/autobuild-sdk"
	sbprovider "github.com/everfaz/autobuild-sdk/providers/sandbox"
)

// sandboxManager manages one OpenSandbox sandbox per chat.
// Sandboxes are created lazily on first use and destroyed when the chat ends.
type sandboxManager struct {
	driver   *sbprovider.OpenSandboxDriver
	mu       sync.Mutex
	chatSandboxes map[int64]string // chatID → sandboxID
}

var globalSandboxManager *sandboxManager
var sandboxManagerOnce sync.Once

// getSandboxManager returns the global sandbox manager, initializing it once.
func getSandboxManager() *sandboxManager {
	sandboxManagerOnce.Do(func() {
		cfg := sbprovider.OpenSandboxConfig{
			Domain:   getenv("OPEN_SANDBOX_DOMAIN", ""),
			Protocol: getenv("OPEN_SANDBOX_PROTOCOL", "https"),
			APIKey:   os.Getenv("OPEN_SANDBOX_API_KEY"),
		}
		driver, err := sbprovider.NewOpenSandbox(cfg)
		if err != nil {
			globalSandboxManager = &sandboxManager{
				chatSandboxes: make(map[int64]string),
			}
			return
		}
		globalSandboxManager = &sandboxManager{
			driver:        driver,
			chatSandboxes: make(map[int64]string),
		}
	})
	return globalSandboxManager
}

// isSandboxAvailable returns true when OPEN_SANDBOX_API_KEY is set.
// Used to conditionally register sandbox tools.
func isSandboxAvailable() bool {
	return strings.TrimSpace(os.Getenv("OPEN_SANDBOX_API_KEY")) != ""
}

// getOrCreateSandbox returns the sandbox ID for a chat, creating one if needed.
func (m *sandboxManager) getOrCreateSandbox(ctx context.Context, chatID int64) (string, error) {
	m.mu.Lock()
	id, ok := m.chatSandboxes[chatID]
	m.mu.Unlock()
	if ok {
		return id, nil
	}

	newID, err := m.driver.Create(ctx, ab.SandboxConfig{
		Labels: map[string]string{
			"chat_id": fmt.Sprintf("%d", chatID),
			"source":  "backend-chat",
		},
	})
	if err != nil {
		return "", fmt.Errorf("create sandbox for chat %d: %w", chatID, err)
	}

	m.mu.Lock()
	m.chatSandboxes[chatID] = newID
	m.mu.Unlock()
	return newID, nil
}

// destroySandbox kills the sandbox for a chat and removes it from the cache.
func (m *sandboxManager) destroySandbox(ctx context.Context, chatID int64) {
	m.mu.Lock()
	id, ok := m.chatSandboxes[chatID]
	if ok {
		delete(m.chatSandboxes, chatID)
	}
	m.mu.Unlock()

	if ok {
		_ = m.driver.Destroy(ctx, id)
	}
}

// ── Tool builders ─────────────────────────────────────────────────────────────

// newBashTool returns a tool that runs bash commands in the chat's sandbox.
// The sandbox persists across turns — state (env vars, files, processes) is preserved.
func (r *agentRuntime) newBashTool(chatID int64) *ab.Tool {
	mgr := getSandboxManager()
	return &ab.Tool{
		Name:        "bash",
		Description: "Run a bash command in an isolated sandbox. The sandbox persists across turns — variables, files, and installed packages survive. Use for: running scripts, installing dependencies, manipulating files, testing code. Stdout and stderr are returned. Commands time out after 60s.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"command": {
					Type:        "string",
					Description: "The bash command to execute. Can be multi-line.",
				},
				"timeout_seconds": {
					Type:        "integer",
					Description: "Timeout in seconds. Default 60. Max 300.",
				},
			},
			Required: []string{"command"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			command := strings.TrimSpace(asString(args["command"]))
			if command == "" {
				return "", fmt.Errorf("command is required")
			}

			timeoutSecs := 60
			if v, ok := args["timeout_seconds"].(float64); ok && v > 0 && v <= 300 {
				timeoutSecs = int(v)
			}
			cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSecs)*time.Second)
			defer cancel()

			sbID, err := mgr.getOrCreateSandbox(cmdCtx, chatID)
			if err != nil {
				return "", fmt.Errorf("sandbox unavailable: %w", err)
			}

			result, err := mgr.driver.Exec(cmdCtx, sbID, command)
			if err != nil {
				return "", fmt.Errorf("exec: %w", err)
			}

			return formatExecResult(result), nil
		},
	}
}

// newCodeInterpreterTool returns a tool that executes code in a specific language.
// Results include text/plain and text/html outputs (for charts, tables, etc.).
// State persists across turns — variables defined in one turn are available in the next.
func (r *agentRuntime) newCodeInterpreterTool(chatID int64) *ab.Tool {
	mgr := getSandboxManager()
	return &ab.Tool{
		Name:        "code_interpreter",
		Description: "Execute code in an isolated sandbox with persistent state across turns. Use for: data analysis, generating charts/visualizations, mathematical computations, file processing. Supported languages: python, javascript, bash. Results include stdout and rich outputs (HTML, images as base64). The context persists — variables and imports defined in one turn are available in the next.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"language": {
					Type:        "string",
					Description: "Programming language: python, javascript, or bash.",
					Enum:        []string{"python", "javascript", "bash"},
				},
				"code": {
					Type:        "string",
					Description: "The code to execute.",
				},
			},
			Required: []string{"language", "code"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			language := strings.TrimSpace(asString(args["language"]))
			code := strings.TrimSpace(asString(args["code"]))
			if language == "" || code == "" {
				return "", fmt.Errorf("language and code are required")
			}

			execCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()

			sbID, err := mgr.getOrCreateSandbox(execCtx, chatID)
			if err != nil {
				return "", fmt.Errorf("sandbox unavailable: %w", err)
			}

			exec, err := mgr.driver.ExecCode(execCtx, sbID, language, code)
			if err != nil {
				return "", fmt.Errorf("code execution failed: %w", err)
			}

			return formatExecResult(exec), nil
		},
	}
}

// newFileWriteTool returns a tool that writes a file into the sandbox.
// The file persists for the lifetime of the sandbox (per-chat).
func (r *agentRuntime) newFileWriteTool(chatID int64) *ab.Tool {
	mgr := getSandboxManager()
	return &ab.Tool{
		Name:        "file_write",
		Description: "Write a file to the sandbox filesystem. The file persists across turns and can be read, executed, or served by subsequent tool calls. Use for: saving scripts, data files, HTML pages, config files.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"path":    {Type: "string", Description: "Absolute or relative file path (e.g. /workspace/script.py or data.csv)."},
				"content": {Type: "string", Description: "File content to write."},
			},
			Required: []string{"path", "content"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := strings.TrimSpace(asString(args["path"]))
			content := asString(args["content"])
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			sbID, err := mgr.getOrCreateSandbox(ctx, chatID)
			if err != nil {
				return "", fmt.Errorf("sandbox unavailable: %w", err)
			}

			if err := mgr.driver.WriteFile(ctx, sbID, path, content); err != nil {
				return "", fmt.Errorf("write file: %w", err)
			}
			return fmt.Sprintf("file written: %s (%d bytes)", path, len(content)), nil
		},
	}
}

// newFileReadTool returns a tool that reads a file from the sandbox.
func (r *agentRuntime) newFileReadTool(chatID int64) *ab.Tool {
	mgr := getSandboxManager()
	return &ab.Tool{
		Name:        "file_read",
		Description: "Read a file from the sandbox filesystem.",
		Category:    ab.ToolCategoryWorkspace,
		Parameters: ab.ToolFuncParams{
			Type: "object",
			Properties: map[string]ab.ToolParam{
				"path": {Type: "string", Description: "File path to read."},
			},
			Required: []string{"path"},
		},
		Execute: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := strings.TrimSpace(asString(args["path"]))
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			sbID, err := mgr.getOrCreateSandbox(ctx, chatID)
			if err != nil {
				return "", fmt.Errorf("sandbox unavailable: %w", err)
			}

			content, err := mgr.driver.ReadFile(ctx, sbID, path)
			if err != nil {
				return "", fmt.Errorf("read file: %w", err)
			}
			return content, nil
		},
	}
}

// ── Format helpers ────────────────────────────────────────────────────────────

func formatExecResult(r ab.ExecResult) string {
	var b strings.Builder
	if r.Stdout != "" {
		b.WriteString(r.Stdout)
	}
	if r.Stderr != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[stderr]\n")
		b.WriteString(r.Stderr)
	}
	if r.ExitCode != 0 {
		b.WriteString(fmt.Sprintf("\n[exit code: %d]", r.ExitCode))
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		return "[no output]"
	}
	return result
}

func formatCodeExecution(exec *opensandbox.Execution) string {
	if exec == nil {
		return "[no output]"
	}

	var parts []string

	// stdout
	if text := exec.Text(); text != "" {
		parts = append(parts, strings.TrimSpace(text))
	}

	// execution results (MIME-keyed)
	for _, res := range exec.Results {
		if res.Results == nil {
			continue
		}
		// Prefer text/plain for LLM context
		if plain := res.Results["text/plain"]; plain != "" {
			parts = append(parts, strings.TrimSpace(plain))
		}
		// Annotate HTML presence — frontend will render it
		if html, ok := res.Results["text/html"]; ok && html != "" {
			parts = append(parts, "[html_output available — frontend will render]")
		}
		// Images come as base64 — annotate but don't dump to LLM
		if _, ok := res.Results["image/png"]; ok {
			parts = append(parts, "[image output available — frontend will render]")
		}
		if _, ok := res.Results["image/svg+xml"]; ok {
			parts = append(parts, "[svg output available — frontend will render]")
		}
	}

	// error
	if exec.Error != nil {
		errMsg := fmt.Sprintf("[error: %s: %s]", exec.Error.Name, exec.Error.Value)
		if len(exec.Error.Traceback) > 0 {
			errMsg += "\n" + strings.Join(exec.Error.Traceback, "\n")
		}
		parts = append(parts, errMsg)
	}

	// execution time
	if exec.Complete != nil && exec.Complete.ExecutionTime > 0 {
		parts = append(parts, fmt.Sprintf("[execution_time: %dms]", exec.Complete.ExecutionTime))
	}

	result := strings.Join(parts, "\n")
	if result == "" {
		return "[no output]"
	}
	return result
}

// formatCodeExecutionForSSE formats an Execution as JSON for the sandbox_output SSE event.
func formatCodeExecutionForSSE(exec *opensandbox.Execution, language string) map[string]any {
	out := map[string]any{
		"language": language,
	}
	if exec == nil {
		return out
	}

	if text := exec.Text(); text != "" {
		out["stdout"] = text
	}
	var stderr strings.Builder
	for _, m := range exec.Stderr {
		stderr.WriteString(m.Text)
		stderr.WriteByte('\n')
	}
	if s := strings.TrimSpace(stderr.String()); s != "" {
		out["stderr"] = s
	}

	// Rich outputs keyed by MIME type
	var results []map[string]string
	for _, res := range exec.Results {
		if len(res.Results) > 0 {
			results = append(results, res.Results)
		}
	}
	if len(results) > 0 {
		out["results"] = results
	}

	if exec.Error != nil {
		errOut := map[string]any{
			"name":  exec.Error.Name,
			"value": exec.Error.Value,
		}
		if len(exec.Error.Traceback) > 0 {
			errOut["traceback"] = exec.Error.Traceback
		}
		out["error"] = errOut
	}
	if exec.ExitCode != nil {
		out["exit_code"] = *exec.ExitCode
	}
	if exec.Complete != nil {
		out["execution_time_ms"] = exec.Complete.ExecutionTime
	}
	return out
}

// sandboxOutputJSON serializes a sandbox output event for SSE.
func sandboxOutputJSON(sandboxID string, chatID int64, output map[string]any) ([]byte, error) {
	event := map[string]any{
		"sandbox_id": sandboxID,
		"chat_id":    chatID,
	}
	for k, v := range output {
		event[k] = v
	}
	return json.Marshal(event)
}
