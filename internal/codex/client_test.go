package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestRefreshesExpiredCachedAuthOnceForConcurrentRequests(t *testing.T) {
	now := time.Now().UTC()
	expiredAccessToken := testJWT(map[string]any{"exp": now.Add(-time.Minute).Unix()})
	freshAccessToken := testJWT(map[string]any{"exp": now.Add(time.Hour).Unix()})
	freshIDToken := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"},
	})
	authPath := writeTestAuthFile(t, map[string]any{
		"last_refresh": "2020-01-01T00:00:00Z",
		"tokens": map[string]any{
			"access_token":  expiredAccessToken,
			"account_id":    "acct-1",
			"refresh_token": "refresh-1",
		},
	})

	var refreshCount atomic.Int64
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.String() {
		case "https://auth.example.test/token":
			refreshCount.Add(1)
			return jsonResponse(http.StatusOK, map[string]any{
				"access_token":  freshAccessToken,
				"id_token":      freshIDToken,
				"refresh_token": "refresh-2",
			}), nil
		case "https://codex.example.test/responses":
			assert.Equal(t, request.Header.Get("Authorization"), "Bearer "+freshAccessToken)
			assert.Equal(t, request.Header.Get("chatgpt-account-id"), "acct-2")
			return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
		default:
			t.Fatalf("unexpected request %s", request.URL.String())
			return nil, nil
		}
	})
	client, err := NewClient(Options{
		AuthFilePath: authPath,
		BaseURL:      "https://codex.example.test",
		Client:       &http.Client{Transport: transport},
		EnsureFresh:  true,
		TokenURL:     "https://auth.example.test/token",
	})
	assert.NilError(t, err)

	var wg sync.WaitGroup
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := client.RawRequest(context.Background(), http.MethodPost, "/responses", nil, nil)
			assert.NilError(t, err)
			defer func() {
				assert.NilError(t, response.Body.Close())
			}()
			_, err = io.ReadAll(response.Body)
			assert.NilError(t, err)
		}()
	}
	wg.Wait()

	assert.Equal(t, refreshCount.Load(), int64(1))
	content, err := os.ReadFile(authPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(content), `"access_token": "`+freshAccessToken+`"`))
	assert.Assert(t, strings.Contains(string(content), `"refresh_token": "refresh-2"`))
}

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func writeTestAuthFile(t *testing.T, body map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	encoded, err := json.MarshalIndent(body, "", "  ")
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(path, encoded, 0o600))
	return path
}

func testJWT(payload map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	body := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + body + ".signature"
}

func jsonResponse(status int, body any) *http.Response {
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return &http.Response{
		Body:       io.NopCloser(strings.NewReader(string(encoded))),
		Header:     make(http.Header),
		StatusCode: status,
	}
}
