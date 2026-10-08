package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Claude Haiku 5.5 takes effort and adaptive thinking, unlike Haiku 4.5, so the
// generic Haiku checks must not gate the effort beta off it. It is not one of
// the progress-display models and accepts a mid-conversation role=system turn.
func TestClaudeHaiku55ModelChecks(t *testing.T) {
	for _, model := range []string{
		"claude-haiku-5-5",
		"claude-haiku-5-5-20261008",
		"claude-haiku-5-5[1m]",
		"anthropic/claude-haiku-5-5",
	} {
		if !isClaudeHaiku55Model(model) {
			t.Fatalf("isClaudeHaiku55Model(%q) = false, want true", model)
		}
		if claudeModelUsesProgressDisplay(model) {
			t.Fatalf("claudeModelUsesProgressDisplay(%q) = true, want false", model)
		}
	}
	for _, model := range []string{
		"claude-haiku-4-5",
		"claude-haiku-4-5-20251001",
		"claude-3-5-haiku-20241022",
		"claude-haiku-5-50",
		"claude-sonnet-5-5",
	} {
		if isClaudeHaiku55Model(model) {
			t.Fatalf("isClaudeHaiku55Model(%q) = true, want false", model)
		}
	}

	haiku55 := claudeCodeCLIBetas([]byte(`{"model":"claude-haiku-5-5","thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`), nil, false)
	for _, want := range []string{claudeEffortBeta, claudeMidConvSystemBeta} {
		if !strings.Contains(haiku55, want) {
			t.Fatalf("claudeCodeCLIBetas for claude-haiku-5-5 missing %s, got: %s", want, haiku55)
		}
	}
	if strings.Contains(haiku55, claudeThinkingDisplayUpdatesBeta) {
		t.Fatalf("claudeCodeCLIBetas for claude-haiku-5-5 unexpectedly includes %s, got: %s", claudeThinkingDisplayUpdatesBeta, haiku55)
	}

	haiku45 := claudeCodeCLIBetas([]byte(`{"model":"claude-haiku-4-5-20251001","thinking":{"type":"enabled","budget_tokens":2048}}`), nil, false)
	if strings.Contains(haiku45, claudeEffortBeta) {
		t.Fatalf("claudeCodeCLIBetas for claude-haiku-4-5 unexpectedly includes %s, got: %s", claudeEffortBeta, haiku45)
	}
}

// Haiku 5.5 returns 400 for any non-default sampling value with or without
// thinking: temperature must be 1, top_p must be 0.99, top_k must be unset, and
// temperature and top_p cannot both be sent.
func TestNormalizeClaudeSamplingForUpstreamHaiku55(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		nativeOwned bool
		keep        map[string]float64
		dropped     []string
	}{
		{
			name:    "translated caller without thinking loses every knob",
			payload: `{"model":"claude-haiku-5-5","temperature":0,"top_p":0.9,"top_k":40}`,
			dropped: []string{"temperature", "top_p", "top_k"},
		},
		{
			name:    "translated caller after forced tool_choice loses top_k",
			payload: `{"model":"claude-haiku-5-5","top_k":40,"tool_choice":{"type":"any"}}`,
			dropped: []string{"top_k"},
		},
		{
			name:        "native keeps the default temperature",
			payload:     `{"model":"claude-haiku-5-5","temperature":1,"thinking":{"type":"disabled"}}`,
			nativeOwned: true,
			keep:        map[string]float64{"temperature": 1},
		},
		{
			name:        "native drops a non-default temperature with thinking disabled",
			payload:     `{"model":"claude-haiku-5-5","temperature":0.5,"thinking":{"type":"disabled"}}`,
			nativeOwned: true,
			dropped:     []string{"temperature"},
		},
		{
			name:        "native keeps a lone default top_p",
			payload:     `{"model":"claude-haiku-5-5","top_p":0.99}`,
			nativeOwned: true,
			keep:        map[string]float64{"top_p": 0.99},
		},
		{
			name:        "native drops top_p other than 0.99",
			payload:     `{"model":"claude-haiku-5-5","top_p":0.95}`,
			nativeOwned: true,
			dropped:     []string{"top_p"},
		},
		{
			name:        "native drops top_p sent with temperature",
			payload:     `{"model":"claude-haiku-5-5","temperature":1,"top_p":0.99}`,
			nativeOwned: true,
			keep:        map[string]float64{"temperature": 1},
			dropped:     []string{"top_p"},
		},
		{
			name:        "native drops top_k without thinking",
			payload:     `{"model":"claude-haiku-5-5[1m]","top_k":40}`,
			nativeOwned: true,
			dropped:     []string{"top_k"},
		},
		{
			name:        "haiku 4.5 without thinking still keeps accepted knobs",
			payload:     `{"model":"claude-haiku-4-5-20251001","temperature":0.5,"top_k":40}`,
			nativeOwned: true,
			keep:        map[string]float64{"temperature": 0.5, "top_k": 40},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := normalizeClaudeSamplingForUpstream([]byte(tc.payload), tc.nativeOwned)

			for field, want := range tc.keep {
				got := gjson.GetBytes(out, field)
				if !got.Exists() || got.Num != want {
					t.Fatalf("%s = %q, want %v preserved: %s", field, got.Raw, want, out)
				}
			}
			for _, field := range tc.dropped {
				if got := gjson.GetBytes(out, field); got.Exists() {
					t.Fatalf("%s = %q, want dropped because Anthropic rejects it: %s", field, got.Raw, out)
				}
			}
		})
	}
}
