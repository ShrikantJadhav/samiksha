package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ShrikantJadhav/samiksha/internal/llm"
	"github.com/ShrikantJadhav/samiksha/internal/tools"
)

// Pass is one narrow review question. Each pass is deliberately a single,
// simple task: small models handle one focused question far better than a
// broad "review this change" instruction.
type Pass struct {
	Name     string
	Question string
}

// DefaultPasses are the specialist questions run before the verdict.
var DefaultPasses = []Pass{
	{
		Name:     "intent-vs-diff",
		Question: "Does the change actually do what the stated intent claims? Identify any unrelated behavior, scope creep, or mismatch between the claim and the diff. Quote the lines that prove your conclusion.",
	},
	{
		Name:     "weakened-checks",
		Question: "Were any checks weakened or bypassed? Look for skipped or deleted tests, removed assertions, silenced linters, suppressed errors, relaxed CI, or unchecked errors. Quote the exact lines.",
	},
	{
		Name:     "capabilities-and-data",
		Question: "Did the change introduce new capabilities or data exposure? Look for secrets or env files, outbound URLs, process execution, filesystem deletion, new dependencies, and sensitive paths. Quote the exact lines.",
	},
}

// PassSystemPrompt constrains a specialist to one question and forces it to
// account for what it ruled out, not just what it flagged.
const PassSystemPrompt = `You are one specialist reviewer in Samiksha, an evidence-backed code review pipeline. You are given ONE narrow question. Answer only that question. Do not try to review the whole change and do not produce a final verdict.

Rules:
- Ground every claim in a short quoted snippet: cite file:line and include the actual code, diff, or tool output. Never quote something that is not in the provided material or a tool result.
- Never claim that something is "part of the commit message", "intentional", "documented", or "justified by the intent" unless that justification is literally present in the supplied material. An author's comment is not proof that a check is safe.
- Blast findings are deterministic facts. If a blast finding is relevant to your question, it MUST appear in Concerns with its quoted snippet. Never put a blast finding in Ruled out.
- These are always concerns when newly added, never Ruled out: skipped tests, removed assertions, suppressed errors or linters, committed secrets or env files, process execution, recursive deletion, outbound URLs, and new dependencies.
- Symmetric burden of proof: besides blast findings, neither a concern nor a dismissal is valid without concrete evidence from the available context. If you cannot prove a concern, move it to Ruled out and say what you checked.
- Use read_file, search, list_dir to inspect surrounding context when useful. Do not call run_command when command execution is disabled.
- Both sections are mandatory. Use "None" if a section is empty.

Respond with exactly this structure:

## Concerns
- file:line — what this shows
  (short quoted snippet here)

## Ruled out
- candidate concern — why the evidence rules it out (or what could not be verified)
`

// VerdictSystemPrompt is used by the consolidation step.
const VerdictSystemPrompt = `You are Samiksha (समीक्षा, "critical review"), an agent that produces a final code-review verdict from specialist reports.

You receive deterministic blast findings, the diff, and several specialist reports. Each specialist answered one narrow question and recorded both Concerns and Ruled out items.

Rules:
- Cross-check the reports. A concern is confirmed only if it has quoted evidence (file:line plus a code/diff/output snippet). A dismissal is accepted only if the report shows what was checked and why it is safe.
- Blast findings are deterministic facts. Every blast finding MUST appear in Findings with its quoted snippet. A blast finding can never appear in Ruled out, no matter what a specialist or the triage report said.
- Never claim something is "part of the commit message", "intentional", "documented", or "justified by the intent" unless that justification is literally present in the supplied material. An author's comment is not proof that a check is safe.
- If a specialist dismissed something without evidence, treat it as unresolved and say so in Ruled out. A dismissal is never valid merely because the author wrote an explanatory comment, or because a linter suppression says the risk is intentional. A skipped test, suppressed error, committed secret, or unrelated exec/delete/outbound call is guardrail erosion even if commented.
- The triage report's "Promoted" items override any specialist dismissal. Include them in Findings with their quoted evidence.
- Do not invent evidence, file paths, line numbers, or snippets that are not present in the material you were given. Keep snippets short and focused.
- Always compare stated intent with the actual changes. Unrelated behavior or weakened checks to appear green is a red flag.
- "Tests pass" is not sufficient evidence if the tests or checks were weakened.
- If something cannot be verified here, say so explicitly.
- Confidence measures certainty of the verdict, NOT severity of the risks. Direct quoted evidence of guardrail erosion (skipped tests, suppressed errors, committed secrets, unrelated capabilities) justifies HIGH confidence even when the impact is serious. Reserve LOW for thin, ambiguous, or unverifiable evidence — not merely because many bad things were found.
- Never suggest auto-merging. Your output is a review.

Respond with exactly this structure:

## Verdict: <SOUND | CONCERNS | UNSOUND>
<one-paragraph summary>

## Evidence
For each key observation, cite the location and include a short quoted snippet of the actual code, diff, or output in a fenced code block.

## Findings
- what is risky or weakened (or "None material" if sound), with severity/impact

## Ruled out
- concerns investigated and dismissed, with the evidence that rules them out (or "None")

## Recommendation
- approve / request changes / needs human review, and the specific actions required

## Confidence
- high / medium / low and why
`

// TriageSystemPrompt re-checks dismissed candidates. Sashiko's key insight is
// that a specialist can talk itself out of a real bug; we apply that rule to
// guardrail erosion specifically.
const TriageSystemPrompt = `You are the triage reviewer in Samiksha. Specialist reviewers have recorded concerns and dismissed candidates. Your ONLY job is to audit the dismissals.

A dismissal is valid only if the specialist showed concrete evidence that the concern does not apply. A dismissal is SHALLOW if it rests on an author's comment, an assumption about unseen code, or the mere presence of a suppression.

Blast findings are deterministic facts and can never be validly dismissed. Promote any blast finding that a specialist put in Ruled out.

In particular, these are NEVER safely dismissed just because a comment says they are intentional:
- a skipped test (t.Skip, SkipNow, xit, it.skip)
- a removed assertion or weakened expectation
- a silenced linter or suppressed error (//nolint, #nosec, eslint-disable, _ = err)
- committed secrets or environment files
- process execution, recursive deletion, or outbound calls unrelated to the stated intent

Respond with exactly:

## Promoted
- dismissed item that is actually a concern — quote the file:line and snippet that proves it (or "None")

## Valid dismissals
- item with concrete evidence that the concern does not apply (or "None")
`

// triage audits the Ruled out sections and returns findings to consolidation.
func triage(ctx context.Context, client llm.Client, review Review, blast string, reports []string) (string, error) {
	var b strings.Builder
	b.WriteString(contextPrompt(review, blast))
	b.WriteString("\n## Specialist reports (audit their 'Ruled out' sections)\n")
	for _, r := range reports {
		b.WriteString(r)
		b.WriteString("\n\n")
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: TriageSystemPrompt},
		{Role: llm.RoleUser, Content: b.String()},
	}
	resp, err := client.Chat(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("triage: %w", err)
	}
	if resp.Message.Content == "" {
		return "## Promoted\n- None\n\n## Valid dismissals\n- None", nil
	}
	return resp.Message.Content, nil
}

// runPass runs one specialist question with a small tool-using loop.
func runPass(ctx context.Context, client llm.Client, reg *tools.Registry, p Pass, review Review, blast string, maxSteps int, logf func(string, ...any)) (string, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: PassSystemPrompt},
		{Role: llm.RoleUser, Content: contextPrompt(review, blast) + "\n## Your question\n" + p.Question},
	}

	for step := 1; step <= maxSteps; step++ {
		logf("  step %d/%d: thinking…", step, maxSteps)
		resp, err := client.Chat(ctx, messages, reg.Defs())
		if err != nil {
			return "", fmt.Errorf("model call (step %d): %w", step, err)
		}
		messages = append(messages, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			if resp.Message.Content != "" {
				return resp.Message.Content, nil
			}
		}

		names := make([]string, 0, len(resp.Message.ToolCalls))
		for _, tc := range resp.Message.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		if len(names) > 0 {
			logf("  step %d/%d: calling %s", step, maxSteps, strings.Join(names, ", "))
			for _, tc := range resp.Message.ToolCalls {
				result := reg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
				messages = append(messages, llm.Message{
					Role:       llm.RoleTool,
					ToolCallID: tc.ID,
					Content:    result,
				})
			}
			continue
		}

		// A pass may answer with an empty body on the first turn for some
		// providers. Give it one explicit chance to write the report.
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: "Write your report now, using the mandatory ## Concerns and ## Ruled out sections.",
		})
	}

	logf("  pass step limit reached; requesting report")
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "Answer your assigned question now with ## Concerns and ## Ruled out. State explicitly anything you could not verify.",
	})
	resp, err := client.Chat(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("final pass request: %w", err)
	}
	if resp.Message.Content == "" {
		return "## Concerns\n- None (pass produced no report)\n\n## Ruled out\n- Nothing could be verified", nil
	}
	return resp.Message.Content, nil
}

// consolidate turns the specialist reports into the user-facing verdict.
func consolidate(ctx context.Context, client llm.Client, review Review, blast string, reports []string) (string, error) {
	var b strings.Builder
	b.WriteString(contextPrompt(review, blast))
	b.WriteString("\n## Specialist reports\n")
	for _, r := range reports {
		b.WriteString(r)
		b.WriteString("\n\n")
	}
	b.WriteString("Produce the final verdict using the required structure. Base every claim only on the material above.")

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: VerdictSystemPrompt},
		{Role: llm.RoleUser, Content: b.String()},
	}
	resp, err := client.Chat(ctx, messages, nil)
	if err != nil {
		return "", fmt.Errorf("consolidation: %w", err)
	}
	if resp.Message.Content == "" {
		return "", fmt.Errorf("consolidation returned an empty verdict")
	}
	return resp.Message.Content, nil
}
