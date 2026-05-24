package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	return response, nil
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
