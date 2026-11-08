package helps

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// unconfirmedDiagFixture is a plugin-runtime style call: billing header, custom
// system prompt, tools, and no claude-code beta, so it stays unconfirmed.
func unconfirmedDiagFixture(t *testing.T, model string) ([]byte, ClaudeCodeRequestDetection) {
	t.Helper()
	encodedUserID, _ := json.Marshal(validClaudeCodeMetadataUserID)
	encodedModel, _ := json.Marshal(model)
	payload := []byte(`{"max_tokens":1024,"messages":[{"role":"user","content":"secret prompt text"}],"metadata":{"user_id":` + string(encodedUserID) + `},"model":` + string(encodedModel) + `,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.319.2d4; cc_entrypoint=cli;"},{"type":"text","text":"You audit promises."}],"tools":[{"name":"SendMessage","input_schema":{"type":"object"}}]}`)
	detection := DetectClaudeCodeRequest(unconfirmedDiagHeaders(), payload, false, nil)
	if detection.Confirmed {
		t.Fatalf("fixture must be unconfirmed, got %#v", detection)
	}
	return payload, detection
}

func unconfirmedDiagHeaders() http.Header {
	headers := measuredClaudeCodeHelperHeaders("interleaved-thinking-2025-05-14,context-management-2025-06-27,thinking-token-count-2026-05-13")
	headers.Set("User-Agent", "claude-cli/2.1.319 (external, cli)")
	headers.Set("Authorization", "Bearer sk-ant-oat-should-never-be-logged")
	return headers
}

func resetUnconfirmedDiag(t *testing.T) *logtest.Hook {
	t.Helper()
	hook := logtest.NewGlobal()
	previous := log.GetLevel()
	log.SetLevel(log.InfoLevel)
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previous)
		unconfirmedClaudeCodeLogLast = sync.Map{}
	})
	unconfirmedClaudeCodeLogLast = sync.Map{}
	return hook
}

func unconfirmedDiagMapSize() int {
	n := 0
	unconfirmedClaudeCodeLogLast.Range(func(_, _ any) bool { n++; return true })
	return n
}

func TestLogUnconfirmedClaudeCodeRequest(t *testing.T) {
	hook := resetUnconfirmedDiag(t)
	headers := unconfirmedDiagHeaders()
	payload, detection := unconfirmedDiagFixture(t, "claude-haiku-5-5")

	LogUnconfirmedClaudeCodeRequest(headers, payload, detection, nil)
	LogUnconfirmedClaudeCodeRequest(headers, payload, detection, nil) // same signature: rate-limited

	if len(hook.AllEntries()) != 1 {
		t.Fatalf("log entries = %d, want 1 (second call rate-limited)", len(hook.AllEntries()))
	}
	entry := hook.LastEntry()
	if entry.Level != log.InfoLevel {
		t.Fatalf("level = %v, want info", entry.Level)
	}
	if strings.Contains(entry.Message, "will be cloaked") {
		t.Fatalf("message claims a cloak decision it cannot know: %q", entry.Message)
	}
	for key, want := range map[string]any{
		"model": "claude-haiku-5-5", "tools": 1, "has_tools": true, "messages": int64(1),
		"betas_present": false, "metadata_user_id_ok": true, "native_entrypoint": true,
		"system_class": "billing+custom", "billing_identity_ok": false, "entrypoint": "cli",
		"betas": "interleaved-thinking-2025-05-14,context-management-2025-06-27,thinking-token-count-2026-05-13",
	} {
		if got := entry.Data[key]; got != want {
			t.Fatalf("field %s = %#v, want %#v", key, got, want)
		}
	}
	rendered := entry.Message + fmt.Sprint(entry.Data)
	for _, leaked := range []string{"sk-ant-oat", "Bearer", "secret prompt text", "You audit promises"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("log entry leaked %q: %s", leaked, rendered)
		}
	}
}

func TestLogUnconfirmedClaudeCodeRequestSkipsConfirmedAndNonCLI(t *testing.T) {
	hook := resetUnconfirmedDiag(t)
	headers := unconfirmedDiagHeaders()
	payload, detection := unconfirmedDiagFixture(t, "claude-haiku-5-5")

	confirmed := detection
	confirmed.Confirmed = true
	LogUnconfirmedClaudeCodeRequest(headers, payload, confirmed, nil)
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("confirmed request logged %d entries, want 0", len(hook.AllEntries()))
	}

	// Recompute detection after removing X-App, so the X-App gate (not the rate
	// limiter, which is empty) is what keeps the request out of the log.
	headers.Del("X-App")
	nonCLI := DetectClaudeCodeRequest(headers, payload, false, nil)
	if nonCLI.XAppCLI {
		t.Fatalf("detection without X-App = %#v, want XAppCLI false", nonCLI)
	}
	LogUnconfirmedClaudeCodeRequest(headers, payload, nonCLI, nil)
	if len(hook.AllEntries()) != 0 || unconfirmedDiagMapSize() != 0 {
		t.Fatalf("non-CLI request logged %d entries / %d slots, want 0", len(hook.AllEntries()), unconfirmedDiagMapSize())
	}

	headers.Set("X-App", "cli")
	headers.Set("User-Agent", "python-httpx/0.28")
	nonCLIUA := DetectClaudeCodeRequest(headers, payload, false, nil)
	LogUnconfirmedClaudeCodeRequest(headers, payload, nonCLIUA, nil)
	if len(hook.AllEntries()) != 0 {
		t.Fatalf("non-claude-cli User-Agent logged %d entries, want 0", len(hook.AllEntries()))
	}
}

func TestLogUnconfirmedClaudeCodeRequestBoundsCallerControlledValues(t *testing.T) {
	hook := resetUnconfirmedDiag(t)
	headers := unconfirmedDiagHeaders()

	for i := 0; i < 50; i++ {
		payload, detection := unconfirmedDiagFixture(t, fmt.Sprintf("claude-haiku-variant-%d", i))
		LogUnconfirmedClaudeCodeRequest(headers, payload, detection, nil)
	}
	if size := unconfirmedDiagMapSize(); size != 1 {
		t.Fatalf("rate-limit slots = %d after 50 model names in one family, want 1", size)
	}
	if len(hook.AllEntries()) != 1 {
		t.Fatalf("log entries = %d, want 1", len(hook.AllEntries()))
	}

	unconfirmedClaudeCodeLogLast = sync.Map{}
	hook.Reset()
	payload, detection := unconfirmedDiagFixture(t, "claude-haiku-5-5\ninjected=1 "+strings.Repeat("x", 200))
	LogUnconfirmedClaudeCodeRequest(headers, payload, detection, nil)
	if got := hook.LastEntry().Data["model"]; got != "other" {
		t.Fatalf("unsafe model logged as %#v, want \"other\"", got)
	}
}

func TestClaimUnconfirmedClaudeCodeLogSlotIsAtomic(t *testing.T) {
	resetUnconfirmedDiag(t)
	now := time.Now()
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if claimUnconfirmedClaudeCodeLogSlot("sig", now) {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatalf("concurrent claims = %d, want 1", claimed.Load())
	}
	if claimUnconfirmedClaudeCodeLogSlot("sig", now.Add(unconfirmedClaudeCodeLogInterval)) != true {
		t.Fatal("claim after the interval = false, want true")
	}
}

func TestClaudeSystemClass(t *testing.T) {
	for _, tc := range []struct {
		system string
		want   string
	}{
		{system: `null`, want: "none"},
		{system: `[]`, want: "none"},
		{system: `"plain string"`, want: "none"},
		{system: `[{"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=cli;"}]`, want: "billing"},
		{system: `[{"type":"text","text":"Only a custom prompt."}]`, want: "no-billing"},
		{system: `[{"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}]`, want: "billing+identity"},
		{system: `[{"type":"text","text":"x-anthropic-billing-header: cc_entrypoint=cli;"},{"type":"text","text":"You audit promises."}]`, want: "billing+custom"},
	} {
		if got := claudeSystemClass([]byte(`{"system":` + tc.system + `}`)); got != tc.want {
			t.Errorf("claudeSystemClass(%s) = %q, want %q", tc.system, got, tc.want)
		}
	}
}

func TestSanitizedClaudeBetaNamesCapsCount(t *testing.T) {
	names := make([]string, unconfirmedClaudeCodeMaxBetas+3)
	for i := range names {
		names[i] = fmt.Sprintf("beta-%02d", i)
	}
	headers := measuredClaudeCodeHelperHeaders(strings.Join(names, ",") + ",bad value!")
	got := sanitizedClaudeBetaNames(headers)
	if !strings.HasSuffix(got, ",+4 more") {
		t.Fatalf("betas = %q, want a +4 more suffix", got)
	}
	if strings.Contains(got, "bad value") {
		t.Fatalf("betas = %q, unsafe name leaked", got)
	}
}
