package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/fingon/go-openai-proxy/internal/auth"
	"github.com/fingon/go-openai-proxy/internal/config"
	"github.com/fingon/go-openai-proxy/internal/server"
	"gotest.tools/v3/assert"
)

const testLowEffort = "low"

func TestCLIReadsEnvironment(t *testing.T) {
	t.Setenv("GO_OPENAI_PROXY_BASE_URL", "https://codex.example.test")
	t.Setenv("GO_OPENAI_PROXY_CODEX_VERSION", "1.2.3")
	t.Setenv("GO_OPENAI_PROXY_EXCLUDE_EFFORTS", " HIGH, low, , HIGH ")
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
	assert.DeepEqual(t, options.ExcludedEfforts, []string{"high", testLowEffort})
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
	assert.DeepEqual(t, options.ExcludedEfforts, []string(nil))
}

func TestCLIRejectsUnknownExcludedEffort(t *testing.T) {
	t.Setenv("GO_OPENAI_PROXY_EXCLUDE_EFFORTS", " high, unlimited ")

	var options server.Options
	parser := kong.Must(&options)
	_, err := parser.Parse(nil)
	assert.ErrorContains(t, err, `invalid reasoning effort "unlimited"`)
	assert.ErrorContains(t, err, "valid choices: none, minimal, low, medium, high, xhigh, max")
}

func TestCLIReadsExcludedEffortsFlag(t *testing.T) {
	t.Setenv("GO_OPENAI_PROXY_EXCLUDE_EFFORTS", "")

	var options server.Options
	parser := kong.Must(&options)
	_, err := parser.Parse([]string{"--exclude-efforts", " HIGH, ,low,high "})
	assert.NilError(t, err)
	assert.DeepEqual(t, options.ExcludedEfforts, []string{"high", testLowEffort})
}

func TestStartupModelsHonorsExcludedEfforts(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	content, err := json.Marshal(auth.File{Tokens: auth.StoredTokens{AccessToken: "access", AccountID: "acct-1"}})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(authPath, content, 0o600))

	client := &http.Client{Transport: startupRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/models")
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.4","supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],"service_tiers":[{"id":"priority"}]}]}`)),
			Header:     make(http.Header),
			StatusCode: http.StatusOK,
		}, nil
	})}

	resolved, err := startupModels(context.Background(), server.Options{
		AuthFilePath:    authPath,
		BaseURL:         "https://chatgpt.com/backend-api/codex",
		CodexVersion:    "1.2.3",
		ExcludedEfforts: []string{"low"},
		HTTPClient:      client,
	})
	assert.NilError(t, err)
	assert.DeepEqual(t, resolved, []string{
		"gpt-5.4",
		"gpt-5.4-high",
		"gpt-5.4-fast",
		"gpt-5.4-high-fast",
	})
}

type startupRoundTripFunc func(request *http.Request) (*http.Response, error)

func (fn startupRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
