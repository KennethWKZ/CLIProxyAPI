package test

import (
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Claude Haiku 5.5 thinks adaptively by default and rejects budget_tokens with
// a 400. thinking.type="disabled" is rejected at effort xhigh and max, so the
// catalog marks it as not disableable. Budgets map to the nearest effort level,
// no-thinking requests get the lowest adaptive effort, and auto keeps the
// upstream default effort.
func TestThinkingE2EClaudeHaiku55(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	uid := fmt.Sprintf("thinking-e2e-claude-haiku-5-5-%d", time.Now().UnixNano())

	const model = "claude-haiku-5-5"
	var haiku55 *registry.ModelInfo
	for _, m := range registry.GetClaudeModels() {
		if m.ID == model {
			haiku55 = m
			break
		}
	}
	if haiku55 == nil {
		t.Fatalf("expected %s in the embedded registry", model)
	}
	reg.RegisterClient(uid, "test", []*registry.ModelInfo{haiku55})
	defer reg.UnregisterClient(uid)

	adaptive := func(name, from, model, input, effort string) thinkingTestCase {
		tc := thinkingTestCase{
			name:         name,
			from:         from,
			to:           "claude",
			model:        model,
			inputJSON:    input,
			expectField:  "thinking.type",
			expectValue:  "adaptive",
			expectAbsent: []string{"thinking.budget_tokens"},
		}
		if effort != "" {
			tc.expectField2 = "output_config.effort"
			tc.expectValue2 = effort
		} else {
			tc.expectAbsent = append(tc.expectAbsent, "output_config.effort")
		}
		return tc
	}

	cases := []thinkingTestCase{
		adaptive("claude-disabled", "claude", model,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`, "low"),
		adaptive("openai-none", "openai", model,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"none"}`, "low"),
		adaptive("suffix-none", "claude", model+"(none)",
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, "low"),
		adaptive("claude-budget", "claude", model,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"thinking":{"type":"enabled","budget_tokens":16384}}`, "high"),
		adaptive("gemini-budget", "gemini", model,
			`{"model":"`+model+`","contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":1024}}}`, "low"),
		adaptive("openai-effort", "openai", model,
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}],"reasoning_effort":"medium"}`, "medium"),
		adaptive("suffix-max", "claude", model+"(max)",
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, "max"),
		adaptive("suffix-auto", "claude", model+"(auto)",
			`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`, ""),
	}

	runThinkingTests(t, cases)
}
