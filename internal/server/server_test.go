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

const (
	testEmptyCompletedResponseData      = `data: {"response":{"id":"resp_1","status":"completed","output":[]}}`
	testEventFunctionCallArgumentsDelta = "event: response.function_call_arguments.delta"
	testEventOutputItemDone             = "event: response.output_item.done"
	testEventResponseCompleted          = "event: response.completed"
)

type recordingTransport struct {
	requests []*http.Request
	bodies   []string
	handler  func(request *http.Request, body string) (*http.Response, error)
}

func (transport *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	var bodyBytes []byte
	if request.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	transport.requests = append(transport.requests, request)
	transport.bodies = append(transport.bodies, string(bodyBytes))

	return transport.handler(request, string(bodyBytes))
}

func TestHealthAndModels(t *testing.T) {
	handler := testHandler(t, nil, []string{defaultChatModel, defaultChatModel, defaultCodexModel})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, health.Code, http.StatusOK)

	modelsResponse := httptest.NewRecorder()
	handler.ServeHTTP(modelsResponse, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Equal(t, modelsResponse.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(modelsResponse.Body.String(), `"gpt-5.2"`))
	assert.Assert(t, strings.Contains(modelsResponse.Body.String(), `"gpt-5.3-codex"`))
}

func TestModelRetrieve(t *testing.T) {
	handler := testHandler(t, nil, []string{defaultChatModel, defaultCodexModel})

	found := httptest.NewRecorder()
	handler.ServeHTTP(found, httptest.NewRequest(http.MethodGet, "/v1/models/gpt-5.3-codex", nil))
	assert.Equal(t, found.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(found.Body.String(), `"id":"gpt-5.3-codex"`))

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/v1/models/not-real", nil))
	assert.Equal(t, missing.Code, http.StatusNotFound)
}

func TestExcludedEffortsAffectModelListAndLookup(t *testing.T) {
	transport := &recordingTransport{}
	transport.handler = func(request *http.Request, _ string) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/models")
		return jsonResponse(http.StatusOK, map[string]any{
			"models": []any{map[string]any{
				"slug":                       "gpt-5.4",
				"supported_reasoning_levels": []any{map[string]any{"effort": "low"}, map[string]any{"effort": "high"}},
				"service_tiers":              []any{map[string]any{"id": "priority"}},
			}},
		}), nil
	}
	handler := testHandlerWithOptions(t, transport, Options{
		CodexVersion:    "1.2.3",
		ExcludedEfforts: []string{" low "},
	})

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Equal(t, list.Code, http.StatusOK)

	var listPayload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	assert.NilError(t, json.Unmarshal(list.Body.Bytes(), &listPayload))
	ids := make([]string, 0, len(listPayload.Data))
	for _, model := range listPayload.Data {
		ids = append(ids, model.ID)
	}
	assert.DeepEqual(t, ids, []string{
		"gpt-5.4",
		"gpt-5.4-high",
		"gpt-5.4-fast",
		"gpt-5.4-high-fast",
	})

	for _, testCase := range []struct {
		model string
		code  int
	}{
		{model: "gpt-5.4-high", code: http.StatusOK},
		{model: "gpt-5.4-low", code: http.StatusNotFound},
	} {
		t.Run(testCase.model, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/models/"+testCase.model, nil))
			assert.Equal(t, response.Code, testCase.code)
		})
	}
}

func TestInferenceKeepsHiddenAliasesAndExplicitEfforts(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path string
		body string
	}{
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"gpt-5.4-high","reasoning":{"effort":"low"},"input":[]}`,
		},
		{
			name: "chat completions",
			path: "/v1/chat/completions",
			body: `{"model":"gpt-5.4-high","reasoning_effort":"low","messages":[]}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var upstreamBody string
			transport := &recordingTransport{}
			transport.handler = func(_ *http.Request, body string) (*http.Response, error) {
				upstreamBody = body
				return textResponse(http.StatusOK, strings.Join([]string{
					testEventResponseCompleted,
					testEmptyCompletedResponseData,
					"",
				}, "\n")), nil
			}
			handler := testHandlerWithOptions(t, transport, Options{
				ExcludedEfforts: []string{"high"},
			})

			request := httptestPostJSON(testCase.path, testCase.body)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			assert.Equal(t, response.Code, http.StatusOK)
			var payload map[string]any
			assert.NilError(t, json.Unmarshal([]byte(upstreamBody), &payload))
			assert.Equal(t, payload["model"], "gpt-5.4")
			reasoning, ok := payload["reasoning"].(map[string]any)
			assert.Assert(t, ok)
			assert.Equal(t, reasoning["effort"], "low")
		})
	}
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
			testEventOutputItemDone,
			`data: {"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"proxy-ok"}],"role":"assistant"}}`,
			"",
			testEventResponseCompleted,
			testEmptyCompletedResponseData,
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
	assert.Assert(t, strings.Contains(response.Body.String(), `"proxy-ok"`))
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

func TestChatCompletionsAggregatesSSE(t *testing.T) {
	transport := &recordingTransport{}
	transport.handler = func(request *http.Request, _ string) (*http.Response, error) {
		assert.Equal(t, request.URL.Path, "/backend-api/codex/responses")
		return textResponse(http.StatusOK, strings.Join([]string{
			testEventOutputItemDone,
			`data: {"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","content":[{"type":"output_text","text":"proxy-ok"}],"role":"assistant"}}`,
			"",
			testEventResponseCompleted,
			`data: {"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":2}}}`,
			"",
		}, "\n")), nil
	}
	handler := testHandler(t, transport, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.2","messages":[{"role":"user","content":"say proxy-ok"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusOK)
	assert.Assert(t, strings.Contains(response.Body.String(), `"content":"proxy-ok"`))
}

func TestUnsupportedV1RouteDoesNotPassthrough(t *testing.T) {
	transport := &recordingTransport{}
	handler := testHandler(t, transport, nil)

	request := httptest.NewRequest(http.MethodPost, "/v1/embeddings?x=1", strings.NewReader(`{"model":"ignored"}`))
	request.Header.Set("Authorization", "Bearer ignored")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	assert.Equal(t, response.Code, http.StatusNotFound)
	assert.Equal(t, len(transport.requests), 0)
}

func testHandler(t *testing.T, transport *recordingTransport, configuredModels []string) *Handler {
	return testHandlerWithOptions(t, transport, Options{Models: configuredModels})
}

func testHandlerWithOptions(t *testing.T, transport *recordingTransport, options Options) *Handler {
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

	options.AuthFilePath = authPath
	options.BaseURL = "https://chatgpt.com/backend-api/codex"
	options.HTTPClient = &http.Client{Transport: transport}
	handler, err := NewHandler(options)
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
