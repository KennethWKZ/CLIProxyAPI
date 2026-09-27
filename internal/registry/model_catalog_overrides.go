package registry

import (
	"reflect"

	log "github.com/sirupsen/logrus"
)

// claudeThinkingOverrides pins thinking capabilities for Claude models whose
// entries in the remote catalog (router-for-me/models) still describe
// requests the Anthropic API rejects. The remote catalog replaces the embedded
// models.json at startup and on every refresh, so embedded edits alone do not
// reach runtime unless --local-model is set.
//
// Remove an entry once the remote catalog carries the same values; until then
// the override is a no-op for the embedded catalog.
var claudeThinkingOverrides = map[string]func(*ThinkingSupport){
	// thinking.type="disabled" returns 400.
	"claude-opus-5-5":   disallowThinkingDisable,
	"claude-sonnet-5-5": disallowThinkingDisable,
	// Adaptive only: budget_tokens and thinking.type="disabled" both return 400.
	"claude-fable-5":   makeThinkingLevelOnly,
	"claude-fable-5-1": makeThinkingLevelOnly,
}

func disallowThinkingDisable(t *ThinkingSupport) {
	t.ZeroAllowed = false
}

func makeThinkingLevelOnly(t *ThinkingSupport) {
	if len(t.Levels) == 0 {
		// Dropping the budget range without levels would leave no valid mode.
		return
	}
	t.Min, t.Max = 0, 0
	t.ZeroAllowed = false
	t.DynamicAllowed = true
}

// applyModelCatalogOverrides patches catalog data in place. Call it before the
// catalog is stored or compared so refresh change detection sees the
// effective values.
func applyModelCatalogOverrides(data *staticModelsJSON) {
	if data == nil {
		return
	}
	for _, model := range data.Claude {
		if model == nil || model.Thinking == nil {
			continue
		}
		override, ok := claudeThinkingOverrides[model.ID]
		if !ok {
			continue
		}
		before := *model.Thinking
		override(model.Thinking)
		if !reflect.DeepEqual(before, *model.Thinking) {
			log.WithField("model", model.ID).Debug("registry: pinned Claude thinking capabilities over catalog values")
		}
	}
}
