package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"gotest.tools/v3/assert"
)

func TestCLIReadsEnvironment(t *testing.T) {
	t.Setenv("GO_OPENAI_PROXY_BASE_URL", "https://codex.example.test")
	t.Setenv("GO_OPENAI_PROXY_CODEX_VERSION", "1.2.3")
	t.Setenv("GO_OPENAI_PROXY_HOST", "0.0.0.0")
	t.Setenv("GO_OPENAI_PROXY_MODELS", "gpt-5.2,gpt-5.3-codex")
	t.Setenv("GO_OPENAI_PROXY_NO_REFRESH", "true")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_CLIENT_ID", "client-1")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_FILE", "/codex/auth.json")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_TOKEN_URL", "https://auth.example.test/token")
	t.Setenv("GO_OPENAI_PROXY_PORT", "8080")
	t.Setenv("GO_OPENAI_PROXY_VERBOSE", "true")

	var commandLine cli
	parser := kong.Must(&commandLine)
	_, err := parser.Parse(nil)
	assert.NilError(t, err)

	assert.Equal(t, commandLine.BaseURL, "https://codex.example.test")
	assert.Equal(t, commandLine.CodexVersion, "1.2.3")
	assert.Equal(t, commandLine.Host, "0.0.0.0")
	assert.Equal(t, commandLine.Models, "gpt-5.2,gpt-5.3-codex")
	assert.Equal(t, commandLine.NoRefresh, true)
	assert.Equal(t, commandLine.OAuthClientID, "client-1")
	assert.Equal(t, commandLine.OAuthFile, "/codex/auth.json")
	assert.Equal(t, commandLine.OAuthTokenURL, "https://auth.example.test/token")
	assert.Equal(t, commandLine.Port, 8080)
	assert.Equal(t, commandLine.Verbose, true)
}
