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
	identity   identityCache
	resolver   versionResolver
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
	// VersionResolver supplies the Codex CLI version advertised in identity
	// headers; empty results fall back to config.FallbackCodexIdentityVersion.
	VersionResolver versionResolver
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
		resolver:   options.VersionResolver,
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

// hopByHopHeaders are connection-scoped values that must not leak upstream;
// Accept-Encoding is included because Go's transport manages compression.
var hopByHopHeaders = []string{
	"Accept-Encoding",
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func (client *Client) newUpstreamRequest(ctx context.Context, method string, targetURL *url.URL, body []byte, inbound http.Header, effectiveAuth auth.Effective) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, targetURL.String(), readerForBody(body))
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}

	copyHeaders(request.Header, inbound)
	for _, key := range hopByHopHeaders {
		request.Header.Del(key)
	}
	request.Header.Del("Authorization")
	request.Header.Del("Chatgpt-Account-Id")
	request.Header.Del("Openai-Beta")

	request.Header.Set("Authorization", "Bearer "+effectiveAuth.AccessToken)
	request.Header.Set("chatgpt-account-id", effectiveAuth.AccountID)
	request.Header.Set("OpenAI-Beta", config.OpenAIBetaResponsesHeader)
	client.applyIdentity(ctx, request.Header)

	return request, nil
}

func (client *Client) applyIdentity(ctx context.Context, header http.Header) {
	resolver := client.resolver
	if resolver == nil {
		resolver = func(context.Context) string { return "" }
	}
	identity := client.identity.get(ctx, resolver)

	header.Set("Originator", config.CodexOriginator)
	header.Set("User-Agent", identity.userAgent)
	header.Set("Version", identity.version)
	if header.Get("Session-Id") == "" {
		header.Set("Session-Id", identity.sessionID)
	}
}

func (client *Client) do(ctx context.Context, method string, targetURL *url.URL, header http.Header, body []byte) (*http.Response, error) {
	effectiveAuth, err := client.ensureAuth(ctx)
	if err != nil {
		return nil, err
	}

	request, err := client.newUpstreamRequest(ctx, method, targetURL, body, header, effectiveAuth)
	if err != nil {
		return nil, err
	}

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

	retryRequest, err := client.newUpstreamRequest(ctx, method, targetURL, body, header, retryAuth)
	if err != nil {
		return nil, fmt.Errorf("create upstream retry request: %w", err)
	}

	retryResponse, err := client.httpClient.Do(retryRequest)
	if err != nil {
		return nil, fmt.Errorf("retry Codex upstream %s after auth recovery: %w", targetURL.String(), err)
	}

	return retryResponse, nil
}

// SetVersionResolver wires lazy Codex CLI version resolution used for identity
// headers; call once during handler construction, before serving traffic.
func (client *Client) SetVersionResolver(resolver versionResolver) {
	client.resolver = resolver
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
}

// codexOAuthUnsupportedParams are request fields the ChatGPT OAuth codex
// endpoint rejects outright, so they must not be forwarded.
var codexOAuthUnsupportedParams = []string{
	"chat_template_kwargs",
	"frequency_penalty",
	"max_completion_tokens",
	"max_output_tokens",
	"metadata",
	"presence_penalty",
	"prompt_cache_retention",
	"safety_identifier",
	"stop",
	"stop_sequences",
	"stream_options",
	"temperature",
	"top_p",
	"truncation",
	"user",
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

	sanitizeResponsesPayload(normalized)

	model, _ := normalized["model"].(string)
	ApplyModelAlias(normalized, model)

	for _, key := range codexOAuthUnsupportedParams {
		delete(normalized, key)
	}

	if _, ok := normalized["instructions"].(string); !ok {
		normalized["instructions"] = options.Instructions
	}
	// The ChatGPT internal endpoint refuses stored responses ("Store must be
	// set to false"), so the field is forced rather than defaulted.
	normalized["store"] = false
	if options.ForceStream {
		normalized["stream"] = true
	}

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
