package registry

import (
	"encoding/json"
	"slices"
	"testing"
)

// remoteCatalogWithoutHaiku55 mirrors router-for-me/models e11df62: the remote
// dropped claude-haiku-5-5. It also adds a remote-only model and changes a
// field on a model both catalogs list.
func remoteCatalogWithoutHaiku55(t *testing.T) []byte {
	t.Helper()
	var remote staticModelsJSON
	if errUnmarshal := json.Unmarshal(embeddedModelsJSON, &remote); errUnmarshal != nil {
		t.Fatalf("decode embedded catalog: %v", errUnmarshal)
	}
	remote.Claude = slices.DeleteFunc(remote.Claude, func(m *ModelInfo) bool { return m.ID == "claude-haiku-5-5" })
	for _, model := range remote.Claude {
		if model.ID == "claude-opus-5-5" {
			model.DisplayName = "Remote Opus 5.5"
		}
	}
	remote.Claude = append(remote.Claude, &ModelInfo{ID: "claude-remote-only", Object: "model", OwnedBy: "anthropic", Type: "claude"})
	data, errMarshal := json.Marshal(&remote)
	if errMarshal != nil {
		t.Fatalf("encode remote catalog: %v", errMarshal)
	}
	return data
}

func restoreEmbeddedCatalog(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		if errLoad := loadModelsFromBytes(embeddedModelsJSON, "restore-embed"); errLoad != nil {
			t.Errorf("restore embedded catalog: %v", errLoad)
		}
	})
}

func findModel(models []*ModelInfo, id string) *ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func TestPublishCatalogKeepsEmbeddedModelsRemoteDrops(t *testing.T) {
	restoreEmbeddedCatalog(t)

	if _, errPublish := publishCatalogBytes(remoteCatalogWithoutHaiku55(t)); errPublish != nil {
		t.Fatalf("publish remote catalog: %v", errPublish)
	}
	claude := GetClaudeModels()

	haiku := findModel(claude, "claude-haiku-5-5")
	if haiku == nil {
		t.Fatal("claude-haiku-5-5 dropped: embedded-only model was not kept")
	}
	if haiku.Thinking == nil || haiku.Thinking.ZeroAllowed {
		t.Fatalf("claude-haiku-5-5: thinking override not applied to kept model: %+v", haiku.Thinking)
	}
	if got := findModel(claude, "claude-opus-5-5"); got == nil || got.DisplayName != "Remote Opus 5.5" {
		t.Fatalf("claude-opus-5-5: remote fields must win for shared ids, got %+v", got)
	}
	if findModel(claude, "claude-remote-only") == nil {
		t.Fatal("claude-remote-only: remote-only model was dropped")
	}
	ids := make(map[string]int)
	for _, model := range claude {
		ids[model.ID]++
		if ids[model.ID] > 1 {
			t.Fatalf("%s listed twice after merge", model.ID)
		}
	}
}

func TestPublishCatalogMergedRefreshIsStable(t *testing.T) {
	restoreEmbeddedCatalog(t)
	remote := remoteCatalogWithoutHaiku55(t)

	changed, errPublish := publishCatalogBytes(remote)
	if errPublish != nil {
		t.Fatalf("first publish: %v", errPublish)
	}
	if !slices.Contains(changed, "claude") {
		t.Fatalf("first publish changed claude models but reported %v", changed)
	}
	changed, errPublish = publishCatalogBytes(remote)
	if errPublish != nil {
		t.Fatalf("second publish: %v", errPublish)
	}
	if len(changed) != 0 {
		t.Fatalf("identical refresh reported changes %v; merged result must be deterministic", changed)
	}
}

func TestMergeEmbeddedOnlyModelsReportsKeptIDs(t *testing.T) {
	embedded := []byte(`{"claude":[{"id":"a"},{"id":"b"}],"xai":[{"id":"x"}]}`)
	parsed := &staticModelsJSON{Claude: []*ModelInfo{{ID: "b", DisplayName: "remote"}, {ID: "c"}}}

	kept := mergeEmbeddedOnlyModels(parsed, embedded)

	if want := []string{"claude/a", "xai/x"}; !slices.Equal(kept, want) {
		t.Fatalf("kept = %v, want %v", kept, want)
	}
	if got := []string{parsed.Claude[0].ID, parsed.Claude[1].ID, parsed.Claude[2].ID}; !slices.Equal(got, []string{"b", "c", "a"}) {
		t.Fatalf("claude order = %v, want remote entries first, then embedded-only", got)
	}
	if parsed.Claude[0].DisplayName != "remote" {
		t.Fatal("embedded entry overwrote the remote entry for a shared id")
	}
	if kept := mergeEmbeddedOnlyModels(parsed, []byte("not json")); kept != nil {
		t.Fatalf("unreadable embedded catalog must leave the fetched catalog alone, kept %v", kept)
	}
}

func TestRefreshWithoutHaiku55StillRoutesToClaude(t *testing.T) {
	restoreEmbeddedCatalog(t)
	const clientID = "test-catalog-merge-claude"
	reg := GetGlobalRegistry()
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	if _, errPublish := publishCatalogBytes(remoteCatalogWithoutHaiku55(t)); errPublish != nil {
		t.Fatalf("publish remote catalog: %v", errPublish)
	}
	// A Claude credential registers whatever the static catalog lists
	// (sdk/cliproxy/service_models.go), which is what request routing reads.
	reg.RegisterClient(clientID, "claude", GetStaticModelDefinitionsByChannel("claude"))

	if providers := reg.GetModelProviders("claude-haiku-5-5"); !slices.Contains(providers, "claude") {
		t.Fatalf("claude-haiku-5-5 providers = %v, want claude", providers)
	}
}
