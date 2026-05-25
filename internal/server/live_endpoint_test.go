package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fingon/go-openai-proxy/internal/auth"
	"gotest.tools/v3/assert"
)

const (
	liveEndpointTestEnabled = "GO_OPENAI_PROXY_LIVE"
	liveChatCompletionsPath = "/chat/completions"
	liveExpectedText        = "proxy-ok"
	liveHost                = "127.0.0.1"
	livePrompt              = "Reply exactly with proxy-ok."
	liveResponsesPath       = "/responses"
)

func TestLiveOpenAICompatibleEndpoints(t *testing.T) {
	if os.Getenv(liveEndpointTestEnabled) != "1" {
		t.Skip(liveEndpointTestEnabled + "=1 is required")
	}

	authPath := liveAuthPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	running, err := Start(ctx, Options{
		AuthFilePath: authPath,
		Host:         liveHost,
		NoRefresh:    true,
		Port:         liveFreePort(t),
	})
	assert.NilError(t, err)

	client := &http.Client{Timeout: 180 * time.Second}
	baseURL := running.URL

	modelsBody, status := liveRequest(t, client, http.MethodGet, baseURL+"/models", nil)
	assert.Equal(t, status, http.StatusOK)
	model := liveModel(t, modelsBody)

	_, status = liveRequest(t, client, http.MethodGet, baseURL+"/models/"+model, nil)
	assert.Equal(t, status, http.StatusOK)

	responsePayload := map[string]any{
		"input":  []any{map[string]any{"role": "user", "content": livePrompt}},
		"model":  model,
		"stream": false,
	}
	responseBody, status := liveJSONRequest(t, client, baseURL+liveResponsesPath, responsePayload)
	assert.Equal(t, status, http.StatusOK)
	assert.Equal(t, liveResponseText(t, responseBody), liveExpectedText)

	responsePayload["stream"] = true
	streamBody, status := liveJSONRequest(t, client, baseURL+liveResponsesPath, responsePayload)
	assert.Equal(t, status, http.StatusOK)
	assert.Assert(t, strings.Contains(string(streamBody), liveExpectedText))

	chatPayload := map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": livePrompt}},
		"model":    model,
		"stream":   false,
	}
	chatBody, status := liveJSONRequest(t, client, baseURL+liveChatCompletionsPath, chatPayload)
	assert.Equal(t, status, http.StatusOK)
	assert.Equal(t, liveChatText(t, chatBody), liveExpectedText)

	chatPayload["stream"] = true
	chatStreamBody, status := liveJSONRequest(t, client, baseURL+liveChatCompletionsPath, chatPayload)
	assert.Equal(t, status, http.StatusOK)
	assert.Equal(t, liveChatStreamText(t, chatStreamBody), liveExpectedText)

	_, status = liveJSONRequest(t, client, baseURL+"/embeddings", map[string]any{"model": "text-embedding-3-small", "input": "proxy test"})
	assert.Equal(t, status, http.StatusNotFound)
}

func liveFreePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", liveHost+":0")
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, listener.Close())
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	assert.Assert(t, ok)

	return addr.Port
}

func liveAuthPath(t *testing.T) string {
	t.Helper()

	authPath, err := auth.ExistingPath(os.Getenv("GO_OPENAI_PROXY_OAUTH_FILE"))
	if err != nil {
		t.Fatalf("missing auth file; set CODEX_HOME or GO_OPENAI_PROXY_OAUTH_FILE: %v", err)
	}

	return authPath
}

func liveJSONRequest(t *testing.T, client *http.Client, url string, payload any) ([]byte, int) {
	t.Helper()

	encoded, err := json.Marshal(payload)
	assert.NilError(t, err)

	return liveRequest(t, client, http.MethodPost, url, encoded)
}

func liveRequest(t *testing.T, client *http.Client, method, url string, body []byte) ([]byte, int) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	assert.NilError(t, err)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	assert.NilError(t, err)
	defer func() {
		assert.NilError(t, response.Body.Close())
	}()

	responseBody, err := io.ReadAll(response.Body)
	assert.NilError(t, err)

	return responseBody, response.StatusCode
}

func liveModel(t *testing.T, body []byte) string {
	t.Helper()

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	assert.NilError(t, json.Unmarshal(body, &response))

	for _, model := range response.Data {
		if model.ID == "gpt-5.3-codex" {
			return model.ID
		}
	}
	for _, model := range response.Data {
		if strings.HasPrefix(model.ID, "gpt-") && model.ID != "codex-auto-review" {
			return model.ID
		}
	}

	t.Fatalf("no usable live model found in %s", string(body))
	return ""
}

func liveResponseText(t *testing.T, body []byte) string {
	t.Helper()

	var response map[string]any
	assert.NilError(t, json.Unmarshal(body, &response))

	return extractText(response)
}

func liveChatText(t *testing.T, body []byte) string {
	t.Helper()

	var response struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	assert.NilError(t, json.Unmarshal(body, &response))
	if len(response.Choices) == 0 {
		t.Fatalf("chat response had no choices: %s", string(body))
	}

	return response.Choices[0].Message.Content
}

func liveChatStreamText(t *testing.T, body []byte) string {
	t.Helper()

	var builder strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			continue
		}

		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		assert.NilError(t, json.Unmarshal([]byte(data), &chunk))
		for _, choice := range chunk.Choices {
			builder.WriteString(choice.Delta.Content)
		}
	}

	return builder.String()
}
