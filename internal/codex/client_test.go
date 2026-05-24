package codex

import (
	"encoding/json"
	"net/http"
	"testing"

	"gotest.tools/v3/assert"
)

func TestResolveTargetURL(t *testing.T) {
	client, err := NewClient(Options{BaseURL: "https://chatgpt.com/backend-api/codex"})
	assert.NilError(t, err)

	for _, testCase := range []struct {
		input string
		want  string
	}{
		{input: "responses", want: "https://chatgpt.com/backend-api/codex/responses"},
		{input: "/v1/responses?foo=bar", want: "https://chatgpt.com/backend-api/codex/responses?foo=bar"},
		{input: "https://chatgpt.com/backend-api/codex/responses?foo=bar", want: "https://chatgpt.com/backend-api/codex/responses?foo=bar"},
	} {
		target, err := client.ResolveTargetURL(testCase.input)
		assert.NilError(t, err)
		assert.Equal(t, target.String(), testCase.want)
	}
}

func TestNormalizeResponsesBody(t *testing.T) {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	body, err := NormalizeResponsesBody("/responses", header, []byte(`{"model":"gpt-5.2","max_output_tokens":5}`), NormalizeOptions{
		ForceStream:  true,
		Instructions: "server-instructions",
	})
	assert.NilError(t, err)

	var payload map[string]any
	assert.NilError(t, json.Unmarshal(body, &payload))
	assert.Equal(t, payload["model"], "gpt-5.2")
	assert.Equal(t, payload["stream"], true)
	assert.Equal(t, payload["store"], false)
	assert.Equal(t, payload["instructions"], "server-instructions")
	_, ok := payload["max_output_tokens"]
	assert.Assert(t, !ok)
}
