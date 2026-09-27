package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// regressedRemoteCatalog returns the embedded catalog with the pinned models
// rewritten to the pre-fix values still served by the remote catalog. It edits
// the raw JSON so every other field round-trips unchanged.
func regressedRemoteCatalog(t *testing.T) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(embeddedModelsJSON))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		t.Fatalf("decode embedded catalog: %v", err)
	}
	claude, _ := raw["claude"].([]any)
	seen := 0
	for _, entry := range claude {
		model, _ := entry.(map[string]any)
		thinking, _ := model["thinking"].(map[string]any)
		if thinking == nil {
			continue
		}
		switch model["id"] {
		case "claude-opus-5-5", "claude-sonnet-5-5":
			thinking["zero_allowed"] = true
		case "claude-fable-5", "claude-fable-5-1":
			thinking["min"] = 1024
			thinking["max"] = 128000
			thinking["zero_allowed"] = true
			delete(thinking, "dynamic_allowed")
		default:
			continue
		}
		seen++
	}
	if seen != len(claudeThinkingOverrides) {
		t.Fatalf("regressed %d catalog models, want %d", seen, len(claudeThinkingOverrides))
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("encode remote catalog: %v", err)
	}
	return payload
}

func assertPinnedClaudeThinking(t *testing.T, models []*ModelInfo) {
	t.Helper()
	found := 0
	for _, model := range models {
		if model == nil {
			continue
		}
		if _, ok := claudeThinkingOverrides[model.ID]; !ok {
			continue
		}
		found++
		th := model.Thinking
		if th == nil {
			t.Fatalf("%s: thinking support missing", model.ID)
		}
		if th.ZeroAllowed {
			t.Errorf("%s: zero_allowed = true, want false", model.ID)
		}
		if model.ID == "claude-opus-5-5" || model.ID == "claude-sonnet-5-5" {
			continue
		}
		if th.Min != 0 || th.Max != 0 {
			t.Errorf("%s: budget range = [%d, %d], want level-only", model.ID, th.Min, th.Max)
		}
		if !th.DynamicAllowed {
			t.Errorf("%s: dynamic_allowed = false, want true", model.ID)
		}
		if len(th.Levels) == 0 {
			t.Errorf("%s: levels missing", model.ID)
		}
	}
	if found != len(claudeThinkingOverrides) {
		t.Fatalf("found %d pinned models, want %d", found, len(claudeThinkingOverrides))
	}
}

func TestEmbeddedCatalogPinsClaudeThinking(t *testing.T) {
	assertPinnedClaudeThinking(t, GetClaudeModels())
}

func TestRemoteRefreshKeepsPinnedClaudeThinking(t *testing.T) {
	payload := regressedRemoteCatalog(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	origURLs := modelsURLs
	modelsURLs = []string{server.URL + "/models.json"}
	t.Cleanup(func() {
		modelsURLs = origURLs
		if errLoad := loadModelsFromBytes(embeddedModelsJSON, "restore-embed"); errLoad != nil {
			t.Errorf("restore embedded catalog: %v", errLoad)
		}
	})

	before := getModels()
	tryRefreshModels(context.Background(), "test refresh")

	assertPinnedClaudeThinking(t, GetClaudeModels())
	for _, provider := range detectChangedProviders(before, getModels()) {
		if provider == "claude" {
			t.Fatalf("refresh reported a claude change; overrides must apply before change detection")
		}
	}
}

func TestApplyModelCatalogOverridesSkipsUnsafeEntries(t *testing.T) {
	data := &staticModelsJSON{Claude: []*ModelInfo{
		nil,
		{ID: "claude-fable-5"},
		{ID: "claude-fable-5-1", Thinking: &ThinkingSupport{Min: 1024, Max: 128000, ZeroAllowed: true}},
		{ID: "claude-opus-5", Thinking: &ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low"}}},
	}}

	applyModelCatalogOverrides(data)
	applyModelCatalogOverrides(nil)

	if data.Claude[1].Thinking != nil {
		t.Fatalf("claude-fable-5: override created thinking support from nothing")
	}
	if got := *data.Claude[2].Thinking; got.Min != 1024 || got.Max != 128000 {
		t.Fatalf("claude-fable-5-1 without levels: budget range dropped: %+v", got)
	}
	if !data.Claude[3].Thinking.ZeroAllowed {
		t.Fatalf("claude-opus-5: unrelated model was modified")
	}
}
