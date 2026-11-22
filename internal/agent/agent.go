package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ShrikantJadhav/pramana/internal/llm"
	"github.com/ShrikantJadhav/pramana/internal/tools"
)

// Review is the shared context for a single review. The diff may already be
// truncated by the caller; tools can still fetch the full text via get_diff.
type Review struct {
	DiffLabel string
	Diff      string
	Intent    string
	WorkDir   string
	AllowRun  bool
}

// Options controls an agent run.
type Options struct {
	// MaxSteps caps the whole loop in single-agent mode (default 12).
	MaxSteps int
	// PassSteps caps each specialist pass in structured mode (default 3).
	PassSteps int
	// SingleAgent keeps the original one-loop behaviour for A/B comparisons.
	SingleAgent bool
	// Passes overrides DefaultPasses (mainly for tests/experiments).
	Passes []Pass
	// ShowPasses prints each specialist report to the progress stream.
	ShowPasses bool
	Log        func(string)
}

// Run executes a structured review: a deterministic blast scan, narrow
// specialist passes that each record concerns and ruled-out candidates, then
// one consolidation call that produces the final verdict.
func Run(ctx context.Context, client llm.Client, reg *tools.Registry, review Review, opts Options) (string, error) {
	logFn := opts.Log
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(fmt.Sprintf(format, args...))
		}
	}

	// Blast is deterministic: run it ourselves rather than spending a model
	// round-trip asking the model to call it.
	logf("running blast scan")
	blast := reg.Call(ctx, "blast_scan", "")
	logf("blast scan complete")

	if opts.SingleAgent {
		steps := opts.MaxSteps
		if steps <= 0 {
			steps = 12
		}
		return runSingle(ctx, client, reg, review, blast, steps, logf)
	}

	passes := opts.Passes
	if len(passes) == 0 {
		passes = DefaultPasses
	}
	steps := opts.PassSteps
	if steps <= 0 {
		steps = 3
	}

	reports := make([]string, 0, len(passes))
	for i, p := range passes {
		logf("pass %d/%d: %s", i+1, len(passes), p.Name)
		report, err := runPass(ctx, client, reg, p, review, blast, steps, logf)
		if err != nil {
			return "", fmt.Errorf("pass %s: %w", p.Name, err)
		}
		reports = append(reports, fmt.Sprintf("### %s\n%s", p.Name, report))
		if opts.ShowPasses && logFn != nil {
			logFn("--- " + p.Name + " report ---\n" + report + "\n--- end " + p.Name + " ---")
		}
	}

	logf("triaging dismissals")
	triageReport, err := triage(ctx, client, review, blast, reports)
	if err != nil {
		return "", fmt.Errorf("triage: %w", err)
	}
	reports = append(reports, "### triage\n"+triageReport)
	if opts.ShowPasses && logFn != nil {
		logFn("--- triage report ---\n" + triageReport + "\n--- end triage ---")
	}

	logf("consolidating verdict")
	return consolidate(ctx, client, review, blast, reports)
}

// contextPrompt builds the material shared by every pass and by consolidation.
func contextPrompt(review Review, blast string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Diff source: %s\n", review.DiffLabel)
	fmt.Fprintf(&b, "Working directory: %s\n", review.WorkDir)
	if review.Intent != "" {
		fmt.Fprintf(&b, "Stated intent: %s\n", review.Intent)
	} else {
		b.WriteString("Stated intent: (none provided)\n")
	}
	if review.AllowRun {
		b.WriteString("Command execution: enabled.\n")
	} else {
		b.WriteString("Command execution: disabled. Do not call run_command.\n")
	}
	b.WriteString("\n## Blast scan (deterministic findings)\n")
	b.WriteString(blast)
	b.WriteString("\n\n## Diff under review\n```diff\n")
	b.WriteString(review.Diff)
	if !strings.HasSuffix(review.Diff, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// runSingle is the original single-agent tool loop, kept for comparison.
func runSingle(ctx context.Context, client llm.Client, reg *tools.Registry, review Review, blast string, maxSteps int, logf func(string, ...any)) (string, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: SystemPrompt},
		{Role: llm.RoleUser, Content: contextPrompt(review, blast) + "\nBegin by examining the diff and comparing it to the stated intent. Run blast_scan if you need the scan again, then investigate and deliver your verdict."},
	}

	for step := 1; step <= maxSteps; step++ {
		logf("step %d/%d: thinking…", step, maxSteps)
		resp, err := client.Chat(ctx, messages, reg.Defs())
		if err != nil {
			return "", fmt.Errorf("model call (step %d): %w", step, err)
		}
		messages = append(messages, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			logf("step %d/%d: verdict ready", step, maxSteps)
			return resp.Message.Content, nil
		}

		names := make([]string, 0, len(resp.Message.ToolCalls))
		for _, tc := range resp.Message.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		logf("step %d/%d: calling %s", step, maxSteps, strings.Join(names, ", "))
		for _, tc := range resp.Message.ToolCalls {
			result := reg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	logf("step limit reached (%d); requesting final verdict", maxSteps)
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "You have reached the step limit. Provide your best verdict now based on the evidence gathered so far. State any gaps explicitly.",
	})
	resp, err := client.Chat(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("final verdict request: %w", err)
	}
	if resp.Message.Content == "" {
		return "(no verdict produced; step limit reached)", nil
	}
	return resp.Message.Content, nil
}
