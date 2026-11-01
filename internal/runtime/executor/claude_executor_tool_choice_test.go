package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

// Haiku 5.5 accepts forced tool use, so it stays out of the downgrade that
// Claude 5.5 and Fable 5.1/5.2 need; only the response skips thinking.
func TestNormalizeClaudeToolChoiceKeepsHaiku55Forced(t *testing.T) {
	cases := []struct {
		name     string
		model    string
		body     string
		wantType string
		wantName string
	}{
		{
			name:     "haiku-5-5 keeps forced any",
			model:    "claude-haiku-5-5",
			body:     `{"tool_choice":{"type":"any"}}`,
			wantType: "any",
		},
		{
			name:     "haiku-5-5 1m variant keeps forced tool",
			model:    "claude-haiku-5-5[1m]",
			body:     `{"tool_choice":{"type":"tool","name":"get_weather"}}`,
			wantType: "tool",
			wantName: "get_weather",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := helps.NormalizeClaudeToolChoice("claude", tc.model, []byte(tc.body))
			if got := ojson.GetBytes(out, "tool_choice.type").String(); got != tc.wantType {
				t.Fatalf("tool_choice.type = %q, want %q: %s", got, tc.wantType, out)
			}
			if got := ojson.GetBytes(out, "tool_choice.name").String(); got != tc.wantName {
				t.Fatalf("tool_choice.name = %q, want %q: %s", got, tc.wantName, out)
			}
		})
	}
}
