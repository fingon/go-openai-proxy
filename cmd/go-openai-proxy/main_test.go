package main

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fingon/go-openai-proxy/internal/config"
	"github.com/fingon/go-openai-proxy/internal/server"
	"gotest.tools/v3/assert"
)

func TestCLIReadsEnvironment(t *testing.T) {
	t.Setenv("GO_OPENAI_PROXY_BASE_URL", "https://codex.example.test")
	t.Setenv("GO_OPENAI_PROXY_CODEX_VERSION", "1.2.3")
	excludedModels := config.DefaultExcludedModel + ",gpt-5"
	t.Setenv("GO_OPENAI_PROXY_EXCLUDE_MODELS", excludedModels)
	t.Setenv("GO_OPENAI_PROXY_HOST", "0.0.0.0")
	t.Setenv("GO_OPENAI_PROXY_MODELS", "gpt-5.2,gpt-5.3-codex")
	t.Setenv("GO_OPENAI_PROXY_NO_REFRESH", "true")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_CLIENT_ID", "client-1")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_FILE", "/codex/auth.json")
	t.Setenv("GO_OPENAI_PROXY_OAUTH_TOKEN_URL", "https://auth.example.test/token")
	t.Setenv("GO_OPENAI_PROXY_PORT", "8080")
	t.Setenv("GO_OPENAI_PROXY_VERBOSE", "true")

	var options server.Options
	parser := kong.Must(&options)
	_, err := parser.Parse(nil)
	assert.NilError(t, err)

	assert.Equal(t, options.BaseURL, "https://codex.example.test")
	assert.Equal(t, options.CodexVersion, "1.2.3")
	assert.DeepEqual(t, options.ExcludedModels, []string{config.DefaultExcludedModel, "gpt-5"})
	assert.Equal(t, options.Host, "0.0.0.0")
	assert.DeepEqual(t, options.Models, []string{"gpt-5.2", "gpt-5.3-codex"})
	assert.Equal(t, options.NoRefresh, true)
	assert.Equal(t, options.ClientID, "client-1")
	assert.Equal(t, options.AuthFilePath, "/codex/auth.json")
	assert.Equal(t, options.TokenURL, "https://auth.example.test/token")
	assert.Equal(t, options.Port, 8080)
	assert.Equal(t, options.Verbose, true)
}

func TestCLIDefaultExcludesAutoReview(t *testing.T) {
	var options server.Options
	parser := kong.Must(&options)
	_, err := parser.Parse(nil)
	assert.NilError(t, err)

	assert.DeepEqual(t, options.ExcludedModels, []string{config.DefaultExcludedModel})
}
