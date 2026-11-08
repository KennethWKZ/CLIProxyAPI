package helps

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	// unconfirmedClaudeCodeLogInterval bounds how often one detection signature
	// is logged, so a busy session cannot flood the log.
	unconfirmedClaudeCodeLogInterval = 10 * time.Minute
	// unconfirmedClaudeCodeMaxFieldLen and unconfirmedClaudeCodeMaxBetas cap the
	// caller-supplied strings that reach the log.
	unconfirmedClaudeCodeMaxFieldLen = 64
	unconfirmedClaudeCodeMaxBetas    = 24
)

// unconfirmedClaudeCodeLogLast maps a signature to the time it was last logged.
// Signatures are built only from booleans and a fixed model-family set, so the
// map stays small no matter what callers send.
var unconfirmedClaudeCodeLogLast sync.Map

// LogUnconfirmedClaudeCodeRequest records why a request that presents itself as
// Claude Code (X-App: cli and a claude-cli User-Agent) was not confirmed. Such a
// request is cloaked, including MCP tool aliasing, when the credential's cloak
// policy calls for it; that decision is made later. It logs only booleans,
// counts, sanitized model, entrypoint and beta names, and the system-block
// class: never header values that could carry credentials, and never message
// content.
func LogUnconfirmedClaudeCodeRequest(headers http.Header, payload []byte, detection ClaudeCodeRequestDetection, cfg *config.Config) {
	if detection.Confirmed || !detection.XAppCLI || !IsClaudeCodeUserAgent(headerValue(headers, "User-Agent")) {
		return
	}
	// One pass over the payload for what the signature needs; everything else
	// is computed only when the line is actually logged.
	top := ojson.GetManyBytesNoCopy(nil, payload, "model", "tools")
	model, tools := top[0].String(), top[1]
	toolCount := 0
	if tools.IsArray() {
		toolCount = int(tools.Get("#").Int())
	}
	signature := unconfirmedClaudeCodeSignature(detection, claudeModelFamily(model), toolCount > 0)
	if !claimUnconfirmedClaudeCodeLogSlot(signature, time.Now()) {
		return
	}
	fields := unconfirmedClaudeCodeFields(headers, payload, detection, cfg)
	fields["model"] = sanitizeLoggedToken(model)
	fields["tools"] = toolCount
	fields["has_tools"] = toolCount > 0
	log.WithFields(fields).Info("claude code detection: request presents as Claude Code but is unconfirmed; cloaking depends on the credential's cloak policy")
}

func unconfirmedClaudeCodeSignature(detection ClaudeCodeRequestDetection, modelFamily string, hasTools bool) string {
	var b strings.Builder
	b.WriteString(modelFamily)
	for _, flag := range []bool{
		hasTools, detection.UserAgent, detection.BetasPresent, detection.MetadataUserID,
		detection.NativeClient, detection.HelperProfile, detection.ConfirmedShape, detection.Subclient != "",
	} {
		b.WriteByte(':')
		b.WriteString(strconv.FormatBool(flag))
	}
	return b.String()
}

// claimUnconfirmedClaudeCodeLogSlot reports whether the caller may log this
// signature now, recording the claim atomically so concurrent requests with the
// same signature log once.
func claimUnconfirmedClaudeCodeLogSlot(signature string, now time.Time) bool {
	for {
		previous, loaded := unconfirmedClaudeCodeLogLast.LoadOrStore(signature, now)
		if !loaded {
			return true
		}
		if last, ok := previous.(time.Time); ok && now.Sub(last) < unconfirmedClaudeCodeLogInterval {
			return false
		}
		if unconfirmedClaudeCodeLogLast.CompareAndSwap(signature, previous, now) {
			return true
		}
	}
}

// claudeModelFamily buckets a caller-supplied model name into a fixed set.
func claudeModelFamily(model string) string {
	for _, family := range []string{"haiku", "sonnet", "opus", "fable"} {
		if strings.HasPrefix(model, "claude-"+family) {
			return family
		}
	}
	return "other"
}

func unconfirmedClaudeCodeFields(headers http.Header, payload []byte, detection ClaudeCodeRequestDetection, cfg *config.Config) log.Fields {
	return log.Fields{
		"messages":            ojson.GetBytesNoCopy(payload, "messages.#").Int(),
		"entrypoint":          sanitizeLoggedToken(detection.Entrypoint),
		"user_agent_ok":       detection.UserAgent,
		"betas_present":       detection.BetasPresent,
		"metadata_user_id_ok": detection.MetadataUserID,
		"native_entrypoint":   detection.NativeClient,
		"accept_encoding_ok":  measuredClaudeCodeHelperAcceptEncoding(headerValue(headers, "Accept-Encoding")),
		"haiku55_headers_ok":  measuredClaudeCodeHaiku55HelperHeadersMatch(headers, cfg),
		"haiku55_session_ok":  measuredClaudeCodeHaiku55HelperSessionMatches(headers, payload),
		"haiku55_body_ok":     measuredClaudeCodeHaiku55HelperBodyMatches(payload, detection.Entrypoint),
		"billing_identity_ok": measuredClaudeCodeBillingAndIdentityMatch(payload, detection.Entrypoint),
		"system_class":        claudeSystemClass(payload),
		"betas":               sanitizedClaudeBetaNames(headers),
	}
}

// sanitizedClaudeBetaNames returns at most unconfirmedClaudeCodeMaxBetas beta
// names, each reduced to a safe token.
func sanitizedClaudeBetaNames(headers http.Header) string {
	normalized := normalizedClaudeBetaHeader(headers)
	if normalized == "" {
		return ""
	}
	betas := strings.Split(normalized, ",")
	extra := 0
	if len(betas) > unconfirmedClaudeCodeMaxBetas {
		extra = len(betas) - unconfirmedClaudeCodeMaxBetas
		betas = betas[:unconfirmedClaudeCodeMaxBetas]
	}
	for i, beta := range betas {
		betas[i] = sanitizeLoggedToken(beta)
	}
	joined := strings.Join(betas, ",")
	if extra > 0 {
		joined += ",+" + strconv.Itoa(extra) + " more"
	}
	return joined
}

// sanitizeLoggedToken keeps a caller-supplied identifier loggable: it returns
// the value when it is a short token of [A-Za-z0-9._-], and "other" otherwise.
func sanitizeLoggedToken(value string) string {
	if value == "" {
		return ""
	}
	if len(value) > unconfirmedClaudeCodeMaxFieldLen {
		return "other"
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_') {
			return "other"
		}
	}
	return value
}

// claudeSystemClass names the shape of the first two system blocks without
// echoing caller text: "none", "billing" or "no-billing", plus "+identity" or
// "+custom" when a second block exists.
func claudeSystemClass(payload []byte) string {
	system := ojson.GetBytesNoCopy(payload, "system")
	if !system.IsArray() {
		return "none"
	}
	blocks := system.Array()
	if len(blocks) == 0 {
		return "none"
	}
	class := "no-billing"
	if strings.HasPrefix(blocks[0].Get("text").String(), "x-anthropic-billing-header:") {
		class = "billing"
	}
	if len(blocks) < 2 {
		return class
	}
	if isClaudeCodeIdentityText(blocks[1].Get("text").String()) {
		return class + "+identity"
	}
	return class + "+custom"
}

func isClaudeCodeIdentityText(text string) bool {
	switch strings.TrimSpace(text) {
	case "You are Claude Code, Anthropic's official CLI for Claude.",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.":
		return true
	}
	return false
}
