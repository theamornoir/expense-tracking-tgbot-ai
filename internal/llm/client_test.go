package llm

import (
	"encoding/json"
	"testing"
)

// TestParseToolCallsNested ensures tool calls nested under
// choices[0].message.tool_calls (the real OpenAI-compatible shape) decode.
func TestParseToolCallsNested(t *testing.T) {
	raw := `{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"add_expense","arguments":"{\"category\":\"еда\",\"amount\":350}"}}]},"finish_reason":"tool_calls"}]}`
	var resp CompletionResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	calls := resp.Choices[0].ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Function.Name != "add_expense" {
		t.Fatalf("unexpected tool %q", calls[0].Function.Name)
	}
	if calls[0].ID != "call_1" {
		t.Fatalf("unexpected call id %q", calls[0].ID)
	}
}
