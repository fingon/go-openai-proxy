package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/fingon/go-openai-proxy/internal/auth"
	"github.com/fingon/go-openai-proxy/internal/config"
)

type HTTPClient interface {
	Do(request *http.Request) (*http.Response, error)
}

type Client struct {
	authLoader auth.Loader
	baseURL    *url.URL
	httpClient HTTPClient
	mu         sync.Mutex
	current    auth.Effective
}

type Options struct {
	AuthFilePath string
	BaseURL      string
	Client       HTTPClient
	ClientID     string
	EnsureFresh  bool
	Issuer       string
	NoRefresh    bool
	TokenURL     string
}

func NewClient(options Options) (*Client, error) {
	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = config.DefaultCodexBaseURL
	}

	parsedBaseURL, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse Codex base URL %q: %w", baseURL, err)
	}
	if parsedBaseURL.Scheme == "" || parsedBaseURL.Host == "" {
		return nil, fmt.Errorf("codex base URL must be absolute: %q", baseURL)
	}

	httpClient := options.Client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Client{
		authLoader: auth.Loader{
			AuthFilePath: options.AuthFilePath,
			Client:       httpClient,
			ClientID:     options.ClientID,
			EnsureFresh:  options.EnsureFresh,
			Issuer:       options.Issuer,
			NoRefresh:    options.NoRefresh,
			TokenURL:     options.TokenURL,
		},
		baseURL:    parsedBaseURL,
		httpClient: httpClient,
	}, nil
}

func (client *Client) Request(ctx context.Context, method, path string, header http.Header, body []byte) (*http.Response, error) {
	targetURL, err := client.ResolveTargetURL(path)
	if err != nil {
		return nil, err
	}

	requestBody, err := NormalizeResponsesBody(targetURL.Path, header, body, NormalizeOptions{})
	if err != nil {
		return nil, err
	}

	return client.do(ctx, method, targetURL, header, requestBody)
}

func (client *Client) RawRequest(ctx context.Context, method, path string, header http.Header, body []byte) (*http.Response, error) {
	targetURL, err := client.ResolveTargetURL(path)
	if err != nil {
		return nil, err
	}

	return client.do(ctx, method, targetURL, header, body)
}

func (client *Client) ResolveTargetURL(input string) (*url.URL, error) {
	parsed, err := url.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("parse target path %q: %w", input, err)
	}
	if parsed.Scheme == "" && !strings.HasPrefix(input, "/") {
		parsed, err = url.Parse("/" + input)
		if err != nil {
			return nil, fmt.Errorf("parse target path %q: %w", input, err)
		}
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}

	basePath := strings.TrimRight(client.baseURL.EscapedPath(), "/")
	switch {
	case path == basePath:
		path = "/"
	case basePath != "" && strings.HasPrefix(path, basePath+"/"):
		path = strings.TrimPrefix(path, basePath)
	}
	if path == "/v1" {
		path = "/"
	} else {
		path = strings.TrimPrefix(path, "/v1/")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}

	target := *client.baseURL
	target.Path = strings.TrimRight(client.baseURL.Path, "/") + path
	target.RawPath = ""
	target.RawQuery = parsed.RawQuery

	return &target, nil
}

func (client *Client) do(ctx context.Context, method string, targetURL *url.URL, header http.Header, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method, targetURL.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}

	copyHeaders(request.Header, header)
	request.Header.Del("Authorization")
	request.Header.Del("Chatgpt-Account-Id")
	request.Header.Del("Openai-Beta")

	effectiveAuth, err := client.ensureAuth(ctx)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+effectiveAuth.AccessToken)
	request.Header.Set("chatgpt-account-id", effectiveAuth.AccountID)
	request.Header.Set("OpenAI-Beta", config.OpenAIBetaResponsesHeader)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Codex upstream %s: %w", targetURL.String(), err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}

	retryAuth, retry, err := client.recoverAfterUnauthorized(ctx, effectiveAuth)
	if err != nil {
		closeBody(response.Body, "unauthorized upstream response body")
		return nil, err
	}
	if !retry {
		return response, nil
	}
	closeBody(response.Body, "unauthorized upstream response body")

	retryRequest, err := http.NewRequestWithContext(ctx, method, targetURL.String(), readerForBody(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream retry request: %w", err)
	}
	copyHeaders(retryRequest.Header, header)
	retryRequest.Header.Del("Authorization")
	retryRequest.Header.Del("Chatgpt-Account-Id")
	retryRequest.Header.Del("Openai-Beta")
	retryRequest.Header.Set("Authorization", "Bearer "+retryAuth.AccessToken)
	retryRequest.Header.Set("chatgpt-account-id", retryAuth.AccountID)
	retryRequest.Header.Set("OpenAI-Beta", config.OpenAIBetaResponsesHeader)

	retryResponse, err := client.httpClient.Do(retryRequest)
	if err != nil {
		return nil, fmt.Errorf("retry Codex upstream %s after auth recovery: %w", targetURL.String(), err)
	}

	return retryResponse, nil
}

func (client *Client) ensureAuth(ctx context.Context) (auth.Effective, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.current.AccessToken != "" && client.current.AccountID != "" && !client.current.ShouldRefresh(time.Now()) {
		current := client.current
		return current, nil
	}

	effectiveAuth, err := client.authLoader.Load(ctx)
	if err != nil {
		return auth.Effective{}, err
	}

	client.current = effectiveAuth

	return effectiveAuth, nil
}

func (client *Client) recoverAfterUnauthorized(ctx context.Context, failedAuth auth.Effective) (auth.Effective, bool, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	reloadedAuth, err := client.authLoader.LoadStored(ctx)
	if err != nil {
		return auth.Effective{}, false, err
	}
	if reloadedAuth.AccountID != failedAuth.AccountID {
		return auth.Effective{}, false, nil
	}
	if !authsEqualForRefresh(failedAuth, reloadedAuth) {
		client.current = reloadedAuth
		return reloadedAuth, true, nil
	}
	if client.authLoader.NoRefresh {
		return auth.Effective{}, false, nil
	}

	refreshedAuth, err := client.authLoader.Refresh(ctx)
	if err != nil {
		return auth.Effective{}, false, err
	}
	if refreshedAuth.AccountID != failedAuth.AccountID {
		return auth.Effective{}, false, errors.New("ChatGPT token refresh returned a different account; please sign in again")
	}
	client.current = refreshedAuth

	return refreshedAuth, true, nil
}

func authsEqualForRefresh(left, right auth.Effective) bool {
	return left.AccountID == right.AccountID &&
		left.AccessToken == right.AccessToken &&
		left.IDToken == right.IDToken &&
		left.RefreshToken == right.RefreshToken
}

type NormalizeOptions struct {
	ForceStream  bool
	Instructions string
	Store        *bool
}

func NormalizeResponsesBody(path string, header http.Header, body []byte, options NormalizeOptions) ([]byte, error) {
	if !strings.HasSuffix(path, "/responses") || len(body) == 0 {
		return body, nil
	}

	contentType := header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "application/json") {
		return body, nil
	}

	if !json.Valid(body) {
		return body, nil
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse responses request body: %w", err)
	}

	normalized := NormalizeResponsesPayload(payload, options)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("marshal normalized responses body: %w", err)
	}

	return encoded, nil
}

func NormalizeResponsesPayload(payload map[string]any, options NormalizeOptions) map[string]any {
	normalized := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		normalized[key] = value
	}

	if _, ok := normalized["instructions"].(string); !ok {
		normalized["instructions"] = options.Instructions
	}
	if _, ok := normalized["store"]; !ok {
		if options.Store != nil {
			normalized["store"] = *options.Store
		} else {
			normalized["store"] = false
		}
	}
	if options.ForceStream {
		normalized["stream"] = true
	}
	delete(normalized, "max_output_tokens")

	return normalized
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func readerForBody(body []byte) io.Reader {
	if body == nil {
		return nil
	}
	return bytes.NewReader(body)
}

func closeBody(body io.Closer, context string) {
	if body == nil {
		return
	}
	if err := body.Close(); err != nil {
		slog.Warn("close body failed", "context", context, "error", err)
	}
}
