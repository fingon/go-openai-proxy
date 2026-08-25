package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fingon/go-openai-proxy/internal/config"
	"gotest.tools/v3/assert"
)

func TestUpstreamRequestCarriesIdentityHeaders(t *testing.T) {
	var captured http.Header
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Header.Clone()
		return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
	})
	authPath := writeTestAuthFile(t, map[string]any{
		"tokens": map[string]any{
			"access_token":  "access",
			"account_id":    "acct-1",
			"refresh_token": "refresh-1",
		},
	})

	client, err := NewClient(Options{
		AuthFilePath:    authPath,
		BaseURL:         "https://codex.example.test",
		Client:          &http.Client{Transport: transport},
		VersionResolver: func(context.Context) string { return "0.150.1" },
	})
	assert.NilError(t, err)

	inbound := make(http.Header)
	inbound.Set("Accept-Encoding", "gzip")
	inbound.Set("Authorization", "Bearer client-token")
	inbound.Set("Session-Id", "client-session")

	response, err := client.RawRequest(context.Background(), http.MethodPost, "/responses", inbound, nil)
	assert.NilError(t, err)
	assert.NilError(t, response.Body.Close())

	assert.Equal(t, captured.Get("Originator"), config.CodexOriginator)
	assert.Equal(t, captured.Get("Version"), "0.150.1")
	assert.Assert(t, strings.HasPrefix(captured.Get("User-Agent"), config.CodexOriginator+"/0.150.1 ("))
	assert.Equal(t, captured.Get("Session-Id"), "client-session")
	assert.Equal(t, captured.Get("Authorization"), "Bearer access")
	assert.Equal(t, captured.Get("chatgpt-account-id"), "acct-1")
	assert.Equal(t, captured.Get("OpenAI-Beta"), config.OpenAIBetaResponsesHeader)
	_, hasAcceptEncoding := captured["Accept-Encoding"]
	assert.Assert(t, !hasAcceptEncoding)
}

func TestUpstreamRequestGeneratesSessionIDAndClampsVersion(t *testing.T) {
	var captured http.Header
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Header.Clone()
		return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
	})
	authPath := writeTestAuthFile(t, map[string]any{
		"tokens": map[string]any{
			"access_token":  "access",
			"account_id":    "acct-1",
			"refresh_token": "refresh-1",
		},
	})

	client, err := NewClient(Options{
		AuthFilePath:    authPath,
		BaseURL:         "https://codex.example.test",
		Client:          &http.Client{Transport: transport},
		VersionResolver: func(context.Context) string { return "0.20.0" },
	})
	assert.NilError(t, err)

	response, err := client.RawRequest(context.Background(), http.MethodGet, "/models", nil, nil)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, response.Body.Close())
	}()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("drain body: %v", err)
	}

	assert.Equal(t, captured.Get("Version"), config.MinCodexIdentityVersion)
	sessionID := captured.Get("Session-Id")
	assert.Equal(t, len(sessionID), 36, sessionID)
}

func TestRawRequestWithoutResolverUsesFallbackVersion(t *testing.T) {
	var captured http.Header
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Header.Clone()
		return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
	})
	authPath := writeTestAuthFile(t, map[string]any{
		"tokens": map[string]any{
			"access_token":  "access",
			"account_id":    "acct-1",
			"refresh_token": "refresh-1",
		},
	})

	client, err := NewClient(Options{
		AuthFilePath: authPath,
		BaseURL:      "https://codex.example.test",
		Client:       &http.Client{Transport: transport},
	})
	assert.NilError(t, err)

	response, err := client.RawRequest(context.Background(), http.MethodGet, "/models", nil, nil)
	assert.NilError(t, err)
	assert.NilError(t, response.Body.Close())

	assert.Equal(t, captured.Get("Version"), config.FallbackCodexIdentityVersion)
}
