package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ShrikantJadhav/pramana/internal/agent"
	"github.com/ShrikantJadhav/pramana/internal/diffsource"
	"github.com/ShrikantJadhav/pramana/internal/llm"
	"github.com/ShrikantJadhav/pramana/internal/tools"
)

var version = "0.1.0-dev"

const usageText = `pramana — evidence-backed review for code changes.

Usage:
  pramana review [flags]    review a diff and produce an evidence-backed verdict
  pramana version
  pramana help

Diff source (review):
  --base <ref>       diff <ref>...HEAD (default: origin/HEAD, main, master, or HEAD~1)
  --staged           diff the index against HEAD
  --patch <file>     read a unified diff from a file, or - for stdin
  --dir <path>       working directory / repository root (default: current directory)

Review context:
  --intent <text>    what the change claims to do (e.g. PR title/description)

Model (OpenAI-compatible chat completions):
  --model <id>       model name (env: PRAMANA_MODEL, OPENAI_MODEL)
  --base-url <url>   API base URL (env: PRAMANA_BASE_URL, OPENAI_BASE_URL; default: https://api.openai.com/v1)
  --api-key <key>    API key (env: PRAMANA_API_KEY, OPENAI_API_KEY)

Tools:
  --blast <path>     path to blast binary (env: PRAMANA_BLAST; default: blast)
  --allow-run        allow the agent to execute shell commands (e.g. run tests)
  --run-timeout <d>  timeout per command when --allow-run is set (default: 60s)
  --pass-steps <n>   max tool rounds per specialist pass (default: 3)
  --single-agent     use the original single-loop reviewer instead of specialist passes
  --show-passes      print each specialist report to stderr (debug)
  --max-steps <n>    max steps for --single-agent mode (default: 12)

Other:
  -q, --quiet        suppress progress messages (verdict still printed to stdout)

Exit codes: 0 verdict produced, 2 error.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usageText)
		return 2
	}
	switch args[0] {
	case "review":
		return cmdReview(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("pramana %s\n", version)
		return 0
	case "help", "-h", "--help":
		fmt.Print(usageText)
		return 0
	default:
		// Convenience: treat flags or unknown as implicit "review" if it looks like flags.
		if strings.HasPrefix(args[0], "-") {
			return cmdReview(args)
		}
		fmt.Fprintf(os.Stderr, "pramana: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

func cmdReview(args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var src diffsource.Source
	fs.StringVar(&src.Base, "base", "", "base ref to diff against")
	fs.StringVar(&src.Patch, "patch", "", "read a unified diff from a file, or - for stdin")
	fs.BoolVar(&src.Staged, "staged", false, "diff the index against HEAD")
	fs.StringVar(&src.Dir, "dir", "", "working directory / repository root")

	intent := fs.String("intent", "", "what the change claims to do")
	model := fs.String("model", "", "model name")
	baseURL := fs.String("base-url", "", "API base URL")
	apiKey := fs.String("api-key", "", "API key")
	blastBin := fs.String("blast", "", "path to blast binary")
	allowRun := fs.Bool("allow-run", false, "allow the agent to execute shell commands")
	runTimeout := fs.Duration("run-timeout", 60*time.Second, "timeout per command")
	maxSteps := fs.Int("max-steps", 12, "maximum agent steps (single-agent mode)")
	passSteps := fs.Int("pass-steps", 3, "maximum tool rounds per specialist pass")
	singleAgent := fs.Bool("single-agent", false, "use the original single-loop reviewer")
	showPasses := fs.Bool("show-passes", false, "print specialist reports to stderr")
	quiet := fs.Bool("quiet", false, "suppress progress messages")
	fs.BoolVar(quiet, "q", false, "suppress progress messages")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	workDir := src.Dir
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		} else {
			workDir = "."
		}
	}
	src.Dir = workDir

	diffText, diffLabel, err := src.Read()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pramana: reading diff: %v\n", err)
		return 2
	}
	if strings.TrimSpace(diffText) == "" {
		fmt.Println("pramana: no changes detected")
		return 0
	}

	resolvedModel := firstNonEmpty(*model, os.Getenv("PRAMANA_MODEL"), os.Getenv("OPENAI_MODEL"))
	resolvedBaseURL := firstNonEmpty(*baseURL, os.Getenv("PRAMANA_BASE_URL"), os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_API_BASE"), "https://api.openai.com/v1")
	resolvedKey := firstNonEmpty(*apiKey, os.Getenv("PRAMANA_API_KEY"), os.Getenv("OPENAI_API_KEY"))
	resolvedBlast := firstNonEmpty(*blastBin, os.Getenv("PRAMANA_BLAST"), "blast")

	if resolvedModel == "" {
		fmt.Fprintln(os.Stderr, "pramana: no model configured (set --model or PRAMANA_MODEL / OPENAI_MODEL)")
		return 2
	}

	client := llm.NewOpenAIClient(resolvedBaseURL, resolvedKey, resolvedModel)
	env := &tools.Env{
		Diff:       diffText,
		DiffLabel:  diffLabel,
		WorkDir:    workDir,
		BlastBin:   resolvedBlast,
		AllowRun:   *allowRun,
		RunTimeout: *runTimeout,
	}
	reg := tools.New(env)

	const maxDiff = 120 * 1024
	reviewDiff := diffText
	if len(reviewDiff) > maxDiff {
		reviewDiff = reviewDiff[:maxDiff] + fmt.Sprintf("\n… [diff truncated, %d bytes total; use get_diff for full text]", len(diffText))
	}
	review := agent.Review{
		DiffLabel: diffLabel,
		Diff:      reviewDiff,
		Intent:    *intent,
		WorkDir:   workDir,
		AllowRun:  *allowRun,
	}

	var logFn func(string)
	if !*quiet {
		logFn = func(msg string) { fmt.Fprintln(os.Stderr, "pramana:", msg) }
	}

	logf := func(format string, args ...any) {
		if logFn != nil {
			logFn(fmt.Sprintf(format, args...))
		}
	}
	logf("reviewing %s (model=%s, blast=%s, run=%v)", diffLabel, resolvedModel, resolvedBlast, *allowRun)

	ctx := context.Background()
	verdict, err := agent.Run(ctx, client, reg, review, agent.Options{
		MaxSteps:    *maxSteps,
		PassSteps:   *passSteps,
		SingleAgent: *singleAgent,
		ShowPasses:  *showPasses,
		Log:         logFn,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pramana: %v\n", err)
		return 2
	}

	fmt.Println()
	fmt.Println(verdict)
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
