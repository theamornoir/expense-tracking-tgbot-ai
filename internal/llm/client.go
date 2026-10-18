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

const (
	maxRetries = 3
	retryDelay = 2 * time.Second
)

// LLMClient is the chat interface implemented by Client and mockClient.
type LLMClient interface {
	Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Choice, error)
}

// Client is an OpenAI-compatible HTTP client with tool calling.
type Client struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
}

// Message is a single message in the LLM conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a tool invocation requested by the LLM.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc is the function part of a ToolCall.
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Choice is one candidate response from the LLM.
type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ToolCalls returns the tool calls nested under message.tool_calls.
func (c *Choice) ToolCalls() []ToolCall {
	return c.Message.ToolCalls
}

// ToolDefinition describes a tool the LLM may call.
type ToolDefinition struct {
	Type     string `json:"type"`
	Function struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Parameters  interface{} `json:"parameters"`
	} `json:"function"`
}

// CompletionResponse is the payload returned by /chat/completions.
type CompletionResponse struct {
	ID      string   `json:"id"`
	Choices []Choice `json:"choices"`
}

// Response is kept as an alias for the mock layer.
type Response = CompletionResponse

func New(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		apiKey:     apiKey,
		model:      model,
		httpClient: &http.Client{Timeout: 75 * time.Second},
	}
}

func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Choice, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		choice, err := c.chatOnce(ctx, messages, tools)
		if err == nil {
			return choice, nil
		}
		lastErr = err

		if isPermanentErr(err) {
			return nil, err
		}

		if attempt < maxRetries {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(retryDelay):
			}
		}
	}
	return nil, fmt.Errorf("LLM API failed after %d retries: %w", maxRetries+1, lastErr)
}

func isPermanentErr(err error) bool {
	return contains(err, "unmarshal response") ||
		contains(err, "marshal request") ||
		contains(err, "create request")
}

func contains(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

func (c *Client) chatOnce(ctx context.Context, messages []Message, tools []ToolDefinition) (*Choice, error) {
	reqBody := map[string]any{
		"model":       c.model,
		"messages":    messages,
		"tools":       tools,
		"tool_choice": "auto",
		"temperature": 0.3,
	}

	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("LLM API rate limited (429): %s", string(body))
		}
		return nil, fmt.Errorf("LLM API error: %s: %s", resp.Status, string(body))
	}

	var completion CompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(completion.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	return &completion.Choices[0], nil
}

// MockResponses is the canned reply map used in MOCK_LLM mode.
var MockResponses = map[string]*Response{
	"кофе 350": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Трата добавлена успешно."}}},
	},
	"покажи траты": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Ваш список траты: 3 записи, сумма 15000 руб."}}},
	},
	"сколько потратил на еду в этом месяце": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Эта неделя: 3000 руб."}}},
	},
	"покажи траты за неделю": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Все траты за неделю: 2500 руб."}}},
	},
	"последнее было не 350, а 450": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Трата обновлена на 450 руб."}}},
	},
	"удали такси": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Трата удалена."}}},
	},
	"удали все": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Не можете удалить все траты. Уточните, что именно удалить."}}},
	},
	"привет": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Привет! Я бот для учёта семейных расходов."}}},
	},
	"спасибо": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Пожалуйста!"}}},
	},
	"unknown": {
		Choices: []Choice{{Message: Message{Role: "assistant", Content: "Не понял. Попробуйте снова."}}},
	},
}

type mockClient struct{}

func NewMockClient() LLMClient { return &mockClient{} }

func (m *mockClient) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (*Choice, error) {
	var last string
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			last = messages[i].Content
			break
		}
	}
	if r, ok := MockResponses[last]; ok && len(r.Choices) > 0 {
		return &r.Choices[0], nil
	}
	r := MockResponses["unknown"]
	return &r.Choices[0], nil
}
