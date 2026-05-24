package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fingon/go-openai-proxy/internal/auth"
	"gotest.tools/v3/assert"
)

type recordingTransport struct {
	requests []*http.Request
	bodies   []string
	handler  func(request *http.Request, body string) (*http.Response, error)
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	bodyBytes, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, request)
	transport.bodies = append(transport.bodies, string(bodyBytes))

	return transport.handler(request, string(bodyBytes))
}

func TestHealthAndModels(t *testing.T) {
	handler := testHandler(t, nil, []string{"gpt-5.2", "gpt-5.2", "gpt-5.3-codex"})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, health.Code, http.StatusOK)

	modelsResponse := httptest.NewRecorder()
	handler.ServeHTTP(modelsResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Equal(t, modelsResponse.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(modelsResponse.Body.String(), `"gpt-5.2"`))
	assert.Assert(t, strings.Contains(modelsResponse.Body.String(), `"gpt-5.3-codex"`))
}

func TestResponsesAggregatesSSE(t *testing.T) {
	transport := &recordingTransport{}
	transport.handler = func(request *http.Request, body string) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/responses")
		assert.Assert(t, strings.Contains(body, `"stream":true`))
		assert.Assert(t, !strings.Contains(body, "max_output_tokens"))
		return textResponse(http.StatusOK, strings.Join([]string{
			"event: response.created",
			`data: {"response":{"id":"resp_1","status":"in_progress"}}`,
			"",
			"event: response.completed",
			`data: {"response":{"id":"resp_1","status":"completed","output":[{"type":"message"}]}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5.2","stream":false,"max_output_tokens":5}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(response.Body.String(), `"id":"resp_1"`))
}

func TestRejectsStatelessReplay(t *testing.T) {
	transport := &recordingTransport{}
	handler := testHandler(t, transport, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"previous_response_id":"resp_1","input":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusBadRequest)
	assert.Equal(t, len(transport.requests), 0)
}

func TestPassthroughStripsV1(t *testing.T) {
	transport := &recordingTransport{}
	transport.handler = func(request *http.Request, _ string) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/embeddings")
		assert.Equal(t, request.URL.RawQuery, "x=1")
		assert.Equal(t, request.Header.Get("Authorization"), "Bearer access")
		assert.Equal(t, request.Header.Get("chatgpt-account-id"), "acct-1")
		return jsonResponse(http.StatusAccepted, map[string]any{"ok": true}), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings?x=1", strings.NewReader(`{"model":"ignored"}`))
	request.Header.Set("Authorization", "Bearer ignored")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusAccepted)
}

func testHandler(t *testing.T, transport *recordingTransport, configuredModels []string) *Handler {
	t.Helper()
	if transport == nil {
		transport = &recordingTransport{handler: func(_ *http.Request, _ string) (*http.Response, error) {
			return jsonResponse(http.StatusOK, map[string]any{"models": []any{map[string]any{"slug": "gpt-5.2"}}}), nil
		}}
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	content, err := json.Marshal(auth.File{Tokens: auth.StoredTokens{AccessToken: "access", AccountID: "acct-1"}})
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(authPath, content, 0o600))

	handler, err := NewHandler(Options{
		AuthFilePath: authPath,
		BaseURL:      "https://chatgpt.com/backend-api/codex",
		HTTPClient:   &http.Client{Transport: transport},
		Models:       configuredModels,
	})
	assert.NilError(t, err)

	return handler
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		StatusCode: status,
	}
}

func jsonResponse(status int, body any) *http.Response {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	response := textResponse(status, string(encoded))
	response.Header.Set("Content-Type", "application/json")
	return response
}
