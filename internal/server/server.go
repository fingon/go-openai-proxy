package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/fingon/go-openai-proxy/internal/codex"
	"github.com/fingon/go-openai-proxy/internal/config"
	"github.com/fingon/go-openai-proxy/internal/models"
	"github.com/fingon/go-openai-proxy/internal/sse"
)

const (
	defaultChatModel = "gpt-5.2"
	proxyOwner       = "codex-oauth"
)

var (
	corsHeaders = map[string]string{
		"access-control-allow-headers": "authorization,content-type",
		"access-control-allow-methods": "GET,POST,OPTIONS",
		"access-control-allow-origin":  "*",
	}
	jsonHeaders = map[string]string{
		"content-type": "application/json; charset=utf-8",
	}
	sseHeaders = map[string]string{
		"cache-control":     "no-cache, no-transform",
		"connection":        "keep-alive",
		"content-type":      "text/event-stream; charset=utf-8",
		"x-accel-buffering": "no",
	}
)

type Options struct {
	AuthFilePath string
	BaseURL      string
	ClientID     string
	CodexVersion string
	Host         string
	HTTPClient   *http.Client
	Models       []string
	Port         int
	TokenURL     string
}

type Running struct {
	Host   string
	Port   int
	Server *http.Server
	URL    string
}

type Handler struct {
	codexClient *codex.Client
	models      *models.Resolver
}

func NewHandler(options Options) (*Handler, error) {
	codexClient, err := codex.NewClient(codex.Options{
		AuthFilePath: options.AuthFilePath,
		BaseURL:      options.BaseURL,
		Client:       options.HTTPClient,
		ClientID:     options.ClientID,
		EnsureFresh:  true,
		TokenURL:     options.TokenURL,
	})
	if err != nil {
		return nil, err
	}

	return &Handler{
		codexClient: codexClient,
		models: models.NewResolver(codexClient, models.Options{
			CodexVersion: options.CodexVersion,
			HTTPClient:   options.HTTPClient,
			Models:       options.Models,
		}),
	}, nil
}

func Start(ctx context.Context, options Options) (Running, error) {
	host := options.Host
	if host == "" {
		host = config.DefaultHost
	}
	port := options.Port
	if port == 0 {
		port = config.DefaultPort
	}

	handler, err := NewHandler(options)
	if err != nil {
		return Running{}, err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return Running{}, fmt.Errorf("listen on %s:%d: %w", host, port, err)
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("server shutdown failed", "error", err)
		}
	}()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "error", err)
		}
	}()

	addr := listener.Addr().(*net.TCPAddr)
	resolvedHost := addr.IP.String()
	if resolvedHost == "::" || resolvedHost == "0.0.0.0" || resolvedHost == "<nil>" {
		resolvedHost = host
	}

	return Running{
		Host:   resolvedHost,
		Port:   addr.Port,
		Server: server,
		URL:    fmt.Sprintf("http://%s:%d/v1", resolvedHost, addr.Port),
	}, nil
}

func (handler *Handler) ServeHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	addHeaders(responseWriter.Header(), corsHeaders)
	if request.Method == http.MethodOptions {
		responseWriter.WriteHeader(http.StatusNoContent)
		return
	}

	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/health":
		writeJSON(responseWriter, http.StatusOK, map[string]any{"ok": true, "replay_state": "stateless"})
	case request.Method == http.MethodGet && request.URL.Path == "/v1/models":
		handler.handleModels(responseWriter, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/responses":
		handler.handleResponses(responseWriter, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/chat/completions":
		handler.handleChatCompletions(responseWriter, request)
	case strings.HasPrefix(request.URL.Path, "/v1/"):
		handler.handlePassthrough(responseWriter, request)
	default:
		writeError(responseWriter, http.StatusNotFound, "Route not found.", "not_found_error")
	}
}

func (handler *Handler) handleModels(responseWriter http.ResponseWriter, request *http.Request) {
	resolvedModels, err := handler.models.Resolve(request.Context())
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	data := make([]map[string]any, 0, len(resolvedModels))
	for _, model := range resolvedModels {
		data = append(data, map[string]any{
			"created":  0,
			"id":       model,
			"object":   "model",
			"owned_by": proxyOwner,
		})
	}

	writeJSON(responseWriter, http.StatusOK, map[string]any{
		"data":   data,
		"object": "list",
	})
}

func (handler *Handler) handleResponses(responseWriter http.ResponseWriter, request *http.Request) {
	body, err := readRequestBody(request)
	if err != nil {
		writeError(responseWriter, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(responseWriter, http.StatusBadRequest, "Request body must be a JSON object.", "invalid_request_error")
		return
	}
	if usesServerReplayState(payload) {
		writeError(responseWriter, http.StatusBadRequest, "Stateless Codex responses endpoint does not support `previous_response_id` or `item_reference`. Replay the full conversation history in `input` on each request.", "invalid_request_error")
		return
	}

	wantsStream := payload["stream"] == true
	normalized := codex.NormalizeResponsesPayload(payload, codex.NormalizeOptions{ForceStream: true})
	encoded, err := json.Marshal(normalized)
	if err != nil {
		writeError(responseWriter, http.StatusInternalServerError, "Failed to encode request.", "server_error")
		return
	}

	upstream, err := handler.codexClient.RawRequest(request.Context(), http.MethodPost, "/responses", jsonContentHeader(), encoded)
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	defer closeBody(upstream.Body, "upstream responses body")

	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		copyUpstreamResponse(responseWriter, upstream)
		return
	}
	if wantsStream {
		copyStreamResponse(responseWriter, upstream, sseHeaders)
		return
	}

	completed, err := sse.CollectCompletedResponse(upstream.Body)
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}

	writeJSON(responseWriter, http.StatusOK, completed)
}

func (handler *Handler) handlePassthrough(responseWriter http.ResponseWriter, request *http.Request) {
	body, err := readRequestBody(request)
	if err != nil {
		writeError(responseWriter, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}

	targetPath := strings.TrimPrefix(request.URL.RequestURI(), "/v1")
	if targetPath == "" {
		targetPath = "/"
	}

	upstream, err := handler.codexClient.RawRequest(request.Context(), request.Method, targetPath, request.Header, body)
	if err != nil {
		writeError(responseWriter, http.StatusBadGateway, err.Error(), "upstream_error")
		return
	}
	defer closeBody(upstream.Body, "upstream passthrough body")

	copyUpstreamResponse(responseWriter, upstream)
}

func readRequestBody(request *http.Request) ([]byte, error) {
	defer closeBody(request.Body, "request body")

	body, err := io.ReadAll(http.MaxBytesReader(nil, request.Body, config.MaxRequestBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read request body: %w", err)
	}

	return body, nil
}

func writeJSON(responseWriter http.ResponseWriter, status int, body any) {
	addHeaders(responseWriter.Header(), jsonHeaders)
	addHeaders(responseWriter.Header(), corsHeaders)
	responseWriter.WriteHeader(status)
	if err := json.NewEncoder(responseWriter).Encode(body); err != nil {
		slog.Error("write JSON response failed", "error", err)
	}
}

func writeError(responseWriter http.ResponseWriter, status int, message, errorType string) {
	writeJSON(responseWriter, status, map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
		},
	})
}

func copyUpstreamResponse(responseWriter http.ResponseWriter, upstream *http.Response) {
	copyHeader(responseWriter.Header(), upstream.Header)
	addHeaders(responseWriter.Header(), corsHeaders)
	if responseWriter.Header().Get("Content-Type") == "" {
		responseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	responseWriter.WriteHeader(upstream.StatusCode)
	if _, err := io.Copy(responseWriter, upstream.Body); err != nil {
		slog.Error("copy upstream response failed", "error", err)
	}
}

func copyStreamResponse(responseWriter http.ResponseWriter, upstream *http.Response, headers map[string]string) {
	addHeaders(responseWriter.Header(), headers)
	addHeaders(responseWriter.Header(), corsHeaders)
	responseWriter.WriteHeader(upstream.StatusCode)
	if _, err := io.Copy(responseWriter, upstream.Body); err != nil {
		slog.Error("copy stream response failed", "error", err)
	}
}

func addHeaders(header http.Header, values map[string]string) {
	for key, value := range values {
		header.Set(key, value)
	}
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func jsonContentHeader() http.Header {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return header
}

func usesServerReplayState(payload map[string]any) bool {
	if _, ok := payload["previous_response_id"].(string); ok {
		return true
	}

	input, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	for _, item := range input {
		itemMap, ok := item.(map[string]any)
		if ok && itemMap["type"] == "item_reference" {
			if _, ok := itemMap["id"].(string); ok {
				return true
			}
		}
	}

	return false
}

func closeBody(body io.Closer, name string) {
	if err := body.Close(); err != nil {
		slog.Warn("close body failed", "name", name, "error", err)
	}
}
