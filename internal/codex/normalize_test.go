package codex

import (
	"encoding/json"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/golden"
)

const (
	testCodexModel             = "gpt-5.3-codex"
	testMinimumIdentityVersion = "0.144.0"
)

func normalizeToJSON(t *testing.T, payload string) string {
	t.Helper()

	var parsed map[string]any
	assert.NilError(t, json.Unmarshal([]byte(payload), &parsed))
	normalized := NormalizeResponsesPayload(parsed, NormalizeOptions{ForceStream: true})
	encoded, err := json.MarshalIndent(normalized, "", "  ")
	assert.NilError(t, err)

	return string(encoded) + "\n"
}

func TestNormalizeResponsesPayload(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload string
	}{
		{
			name:    "promotes-system-messages",
			payload: `{"model":"gpt-5.2","instructions":"base","input":[{"type":"message","role":"system","content":"You are terse."},{"type":"message","role":"user","content":"hi"}]}`,
		},
		{
			name:    "promotes-developer-messages",
			payload: `{"model":"gpt-5.2","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Rule one."}]},{"type":"message","role":"user","content":"hi"}]}`,
		},
		{
			name:    "keeps-mixed-content-system-as-developer",
			payload: `{"model":"gpt-5.2","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"Look at"},{"type":"input_image","image_url":"https://x.test/i.png"}]},{"type":"message","role":"user","content":"what is this"}]}`,
		},
		{
			name:    "strips-unsupported-params",
			payload: `{"model":"gpt-5.2","temperature":0.5,"top_p":0.9,"stop":["\n"],"max_output_tokens":10,"user":"u1","metadata":{"a":1},"stream_options":{"include_usage":true},"truncation":"auto","frequency_penalty":0.2,"presence_penalty":0.3,"input":[{"type":"message","role":"user","content":"hi"}],"prompt_cache_key":"cache-1"}`,
		},
		{
			name:    "coerces-string-input",
			payload: `{"model":"gpt-5.2","input":"just say hi"}`,
		},
		{
			name:    "normalizes-call-ids",
			payload: `{"model":"gpt-5.2","input":[{"type":"function_call","call_id":"call_abc123","name":"lookup","arguments":"{\"q\":1}"},{"type":"function_call_output","call_id":"call_abc123","output":"result"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}]}`,
		},
		{
			name:    "compacts-long-call-ids",
			payload: `{"model":"gpt-5.2","input":[{"type":"function_call","call_id":"call_aVeryLongIdentifierThatDefinitelyExceedsTheSixtyFourByteLimitImposedByUpstream0123456789","name":"lookup","arguments":"{}"}]}`,
		},
		{
			name:    "sanitizes-reasoning-items",
			payload: `{"model":"gpt-5.2","include":["reasoning.encrypted_content"],"input":[{"type":"reasoning","id":"rs_123","summary":null,"encrypted_content":"enc"},{"type":"message","role":"user","content":"hi"}]}`,
		},
		{
			name:    "adds-reasoning-include",
			payload: `{"model":"gpt-5.2","reasoning":{"effort":"high"},"input":[{"type":"message","role":"user","content":"hi"}]}`,
		},
		{
			name:    "effort-suffix-alias",
			payload: `{"model":"gpt-5.3-codex-high","input":[{"type":"message","role":"user","content":"hi"}]}`,
		},
		{
			name:    "converts-tool-role-messages",
			payload: `{"model":"gpt-5.2","input":[{"role":"tool","tool_call_id":"call_xyz","content":[{"type":"text","text":"42"}]},{"type":"message","role":"user","content":"thanks"}]}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			golden.Assert(t, normalizeToJSON(t, testCase.payload), testCase.name+".golden")
		})
	}
}

func TestSplitEffortSuffix(t *testing.T) {
	for _, testCase := range []struct {
		model      string
		wantModel  string
		wantEffort string
	}{
		{model: "gpt-5.3-codex-high", wantModel: testCodexModel, wantEffort: "high"},
		{model: "gpt-5.4-none", wantModel: "gpt-5.4", wantEffort: "minimal"},
		{model: "gpt-5.3-codex-xhigh", wantModel: testCodexModel, wantEffort: "xhigh"},
		{model: "gpt-5.2", wantModel: "gpt-5.2"},
		{model: testCodexModel, wantModel: testCodexModel},
		{model: "", wantModel: ""},
	} {
		gotModel, gotEffort := SplitEffortSuffix(testCase.model)
		assert.Equal(t, gotModel, testCase.wantModel, testCase.model)
		assert.Equal(t, gotEffort, testCase.wantEffort, testCase.model)
	}
}

func TestApplyEffortAliasKeepsExplicitReasoning(t *testing.T) {
	payload := map[string]any{
		"reasoning": map[string]any{"effort": "low"},
	}
	assert.Assert(t, !ApplyEffortAlias(payload, "gpt-5.3-codex-high"))
}

func TestClampIdentityVersion(t *testing.T) {
	for _, testCase := range []struct {
		version string
		want    string
	}{
		{version: "0.150.0", want: "0.150.0"},
		{version: testMinimumIdentityVersion, want: testMinimumIdentityVersion},
		{version: "0.100.0", want: testMinimumIdentityVersion},
		{version: "1.2.3-alpha.4", want: "1.2.3-alpha.4"},
		{version: "garbage", want: "0.144.0"},
		{version: "", want: testMinimumIdentityVersion},
	} {
		assert.Equal(t, clampIdentityVersion(testCase.version), testCase.want, testCase.version)
	}
}

func TestNewSessionIDFormat(t *testing.T) {
	sessionID, err := newSessionID()
	assert.NilError(t, err)
	assert.Assert(t, len(sessionID) == 36, sessionID)
	assert.Equal(t, sessionID[14], byte('4'))
	assert.Assert(t, sessionID[19] >= '8' && sessionID[19] <= 'b', sessionID)
}
