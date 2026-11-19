package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/ShrikantJadhav/pramana/internal/llm"
	"github.com/ShrikantJadhav/pramana/internal/tools"
)

// Options controls an agent run.
type Options struct {
	MaxSteps int
	Log      func(string) // optional progress messages
}

// Run executes the review loop until the model produces a final answer or the
// step limit is reached.
func Run(ctx context.Context, client llm.Client, reg *tools.Registry, userPrompt string, opts Options) (string, error) {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 12
	}
	logf := func(format string, args ...any) {
		if opts.Log != nil {
			opts.Log(fmt.Sprintf(format, args...))
		}
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: SystemPrompt},
		{Role: llm.RoleUser, Content: userPrompt},
	}

	for step := 1; step <= opts.MaxSteps; step++ {
		logf("step %d/%d: thinking…", step, opts.MaxSteps)
		resp, err := client.Chat(ctx, messages, reg.Defs())
		if err != nil {
			return "", fmt.Errorf("model call (step %d): %w", step, err)
		}

		messages = append(messages, resp.Message)

		if len(resp.Message.ToolCalls) == 0 {
			logf("step %d/%d: verdict ready", step, opts.MaxSteps)
			return resp.Message.Content, nil
		}

		names := make([]string, 0, len(resp.Message.ToolCalls))
		for _, tc := range resp.Message.ToolCalls {
			names = append(names, tc.Function.Name)
		}
		logf("step %d/%d: calling %s", step, opts.MaxSteps, strings.Join(names, ", "))

		for _, tc := range resp.Message.ToolCalls {
			result := reg.Call(ctx, tc.Function.Name, tc.Function.Arguments)
			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				ToolCallID: tc.ID,
				Content:    result,
			})
		}
	}

	// Step limit reached: ask for a best-effort verdict without more tools.
	logf("step limit reached (%d); requesting final verdict", opts.MaxSteps)
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
