package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIClient implements Client against an OpenAI-compatible chat completions API.
type OpenAIClient struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewOpenAIClient returns a client configured for an OpenAI-compatible endpoint.
func NewOpenAIClient(baseURL, apiKey, model string) *OpenAIClient {
	return &OpenAIClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

func (c *OpenAIClient) endpoint() string {
	if strings.HasSuffix(c.BaseURL, "/chat/completions") {
		return c.BaseURL
	}
	return c.BaseURL + "/chat/completions"
}

type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      respMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error,omitempty"`
}

// respMessage tolerates a null content field from providers that omit it when
// returning tool calls.
type respMessage struct {
	Role      Role       `json:"role"`
	Content   *string    `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

func (c *OpenAIClient) Chat(ctx context.Context, messages []Message, tools []ToolDef) (Response, error) {
	reqBody := chatRequest{Model: c.Model, Messages: messages}
	if len(tools) > 0 {
		reqBody.Tools = tools
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("chat request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}

	var cr chatResponse
	if err := json.Unmarshal(data, &cr); err != nil {
		return Response{}, fmt.Errorf("decode response (status %d): %w: %s", resp.StatusCode, err, truncate(data, 400))
	}
	if cr.Error != nil {
		return Response{}, fmt.Errorf("api error (%s): %s", cr.Error.Type, cr.Error.Message)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("api status %d: %s", resp.StatusCode, truncate(data, 400))
	}
	if len(cr.Choices) == 0 {
		return Response{}, fmt.Errorf("api returned no choices")
	}

	m := cr.Choices[0].Message
	content := ""
	if m.Content != nil {
		content = *m.Content
	}
	return Response{
		Message: Message{
			Role:      RoleAssistant,
			Content:   content,
			ToolCalls: m.ToolCalls,
		},
		FinishReason: cr.Choices[0].FinishReason,
	}, nil
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
