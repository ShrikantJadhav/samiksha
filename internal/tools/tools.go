package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShrikantJadhav/samiksha/internal/llm"
)

const maxOutput = 100 * 1024 // 100 KiB per tool result

// Env holds shared state and configuration for tools.
type Env struct {
	Diff       string
	DiffLabel  string
	WorkDir    string
	BlastBin   string
	AllowRun   bool
	RunTimeout time.Duration
}

type tool struct {
	def llm.ToolDef
	run func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry exposes tool definitions to the model and dispatches executions.
type Registry struct {
	tools map[string]tool
	defs  []llm.ToolDef
}

// New builds a registry with the standard samiksha tools.
func New(env *Env) *Registry {
	r := &Registry{tools: map[string]tool{}}

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "get_diff",
			Description: "Return the unified diff under review.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			if env.Diff == "" {
				return "(no diff content)", nil
			}
			return capOutput(env.Diff), nil
		},
	})

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "blast_scan",
			Description: "Run `blast scan --json` on the diff under review to get deterministic findings (guardrail erosion, secrets, sensitive paths, test integrity, etc.) and a risk score. Use this early.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			return runBlast(ctx, env)
		},
	})

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "read_file",
			Description: "Read a text file from the working directory. Returns content prefixed with line numbers.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "File path, relative to the working directory or absolute."},
					"start_line": map[string]any{"type": "integer", "description": "1-based line to start from (default 1)."},
					"end_line":   map[string]any{"type": "integer", "description": "1-based inclusive line to end at (default: start_line+200)."},
				},
				"required": []string{"path"},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path      string `json:"path"`
				StartLine int    `json:"start_line"`
				EndLine   int    `json:"end_line"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return readFile(env, a.Path, a.StartLine, a.EndLine)
		},
	})

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "list_dir",
			Description: "List entries in a directory (one per line, directories suffixed with /).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory path (default \".\")."},
				},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal(args, &a)
			if a.Path == "" {
				a.Path = "."
			}
			return listDir(env, a.Path)
		},
	})

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "search",
			Description: "Search file contents for a pattern (regex). Uses ripgrep if available, otherwise grep. Returns matching lines as path:line:content.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pattern": map[string]any{"type": "string", "description": "Regular expression to search for."},
					"path":    map[string]any{"type": "string", "description": "File or directory to search (default \".\")."},
					"glob":    map[string]any{"type": "string", "description": "Glob filter for files (ripgrep only), e.g. \"*.go\"."},
				},
				"required": []string{"pattern"},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Pattern string `json:"pattern"`
				Path    string `json:"path"`
				Glob    string `json:"glob"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			if a.Path == "" {
				a.Path = "."
			}
			return search(ctx, env, a.Pattern, a.Path, a.Glob)
		},
	})

	r.add(tool{
		def: llm.ToolDef{Type: "function", Function: llm.FunctionDef{
			Name:        "run_command",
			Description: "Run a shell command in the working directory (e.g. to execute tests). Returns combined stdout/stderr and exit status. May be disabled by configuration.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{"type": "string", "description": "Shell command to execute."},
					"workdir": map[string]any{"type": "string", "description": "Working directory (default: review working directory)."},
				},
				"required": []string{"command"},
			},
		}},
		run: func(ctx context.Context, args json.RawMessage) (string, error) {
			var a struct {
				Command string `json:"command"`
				Workdir string `json:"workdir"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			return runCommand(ctx, env, a.Command, a.Workdir)
		},
	})

	return r
}

func (r *Registry) add(t tool) {
	r.tools[t.def.Function.Name] = t
	r.defs = append(r.defs, t.def)
}

// Defs returns tool definitions for the model.
func (r *Registry) Defs() []llm.ToolDef { return r.defs }

// Call executes a tool by name and returns its result as text for the model.
func (r *Registry) Call(ctx context.Context, name, arguments string) string {
	t, ok := r.tools[name]
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", name)
	}
	res, err := t.run(ctx, json.RawMessage(arguments))
	if err != nil {
		return "error: " + err.Error()
	}
	return capOutput(res)
}

func capOutput(s string) string {
	if len(s) <= maxOutput {
		return s
	}
	return s[:maxOutput] + fmt.Sprintf("\n… [truncated, %d bytes total]", len(s))
}

func resolve(env *Env, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	base := env.WorkDir
	if base == "" {
		base = "."
	}
	return filepath.Join(base, p)
}

func runBlast(ctx context.Context, env *Env) (string, error) {
	bin := env.BlastBin
	if bin == "" {
		bin = "blast"
	}
	if _, err := exec.LookPath(bin); err != nil {
		// Also try common sibling location.
		if env.WorkDir != "" {
			candidate := filepath.Join(filepath.Dir(env.WorkDir), "blast", "blast")
			if _, err2 := os.Stat(candidate); err2 == nil {
				bin = candidate
			} else {
				return "", fmt.Errorf("blast binary not found (%q). Install blast or set --blast to its path", env.BlastBin)
			}
		} else {
			return "", fmt.Errorf("blast binary not found (%q). Install blast or set --blast to its path", env.BlastBin)
		}
	}
	cmd := exec.CommandContext(ctx, bin, "scan", "--json", "--patch", "-")
	cmd.Stdin = strings.NewReader(env.Diff)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		if out != "" {
			return out, nil // blast may emit JSON even on non-zero in some cases; prefer it
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("blast scan failed: %s", msg)
	}
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	if out == "" {
		out = `{"note":"blast produced no output"}`
	}
	return out, nil
}

func readFile(env *Env, path string, start, end int) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	b, err := os.ReadFile(resolve(env, path))
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if start < 1 {
		start = 1
	}
	if end < 1 {
		end = start + 199
	}
	if start > len(lines) {
		return fmt.Sprintf("(file has %d lines; start_line %d is past end)", len(lines), start), nil
	}
	if end > len(lines) {
		end = len(lines)
	}
	if end < start {
		end = start
	}
	var buf strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&buf, "%6d\t%s\n", i, lines[i-1])
	}
	return buf.String(), nil
}

func listDir(env *Env, path string) (string, error) {
	entries, err := os.ReadDir(resolve(env, path))
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintln(&buf, name)
	}
	if buf.Len() == 0 {
		return "(empty directory)", nil
	}
	return buf.String(), nil
}

func search(ctx context.Context, env *Env, pattern, path, glob string) (string, error) {
	if pattern == "" {
		return "", errors.New("pattern is required")
	}
	dir := env.WorkDir
	if dir == "" {
		dir = "."
	}
	target := path
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}

	if rg, err := exec.LookPath("rg"); err == nil {
		args := []string{"--no-heading", "-n", "--color", "never", "-S", "-e", pattern}
		if glob != "" {
			args = append(args, "--glob", glob)
		}
		args = append(args, target)
		return runCmd(ctx, dir, rg, args, 30*time.Second)
	}

	// Fallback: grep -RInE (no glob support)
	return runCmd(ctx, dir, "grep", []string{"-RInE", pattern, target}, 30*time.Second)
}

func runCommand(ctx context.Context, env *Env, command, workdir string) (string, error) {
	if !env.AllowRun {
		return "command execution is disabled (start samiksha with --allow-run to enable running tests/commands)", nil
	}
	if strings.TrimSpace(command) == "" {
		return "", errors.New("command is required")
	}
	dir := env.WorkDir
	if workdir != "" {
		dir = resolve(env, workdir)
	}
	if dir == "" {
		dir = "."
	}
	timeout := env.RunTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return runCmd(ctx, dir, "sh", []string{"-c", command}, timeout)
}

func runCmd(ctx context.Context, dir, name string, args []string, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		if out != "" {
			out += "\n"
		}
		out += fmt.Sprintf("[command timed out after %s]", timeout)
		return out, nil
	}
	if err != nil {
		if out == "" {
			out = err.Error()
		}
		// Include exit status context; not a tool-execution failure, the agent
		// needs to see non-zero exits (e.g. failing tests).
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			out = fmt.Sprintf("%s\n[exit status %d]", strings.TrimRight(out, "\n"), ee.ExitCode())
		}
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return out, nil
}
