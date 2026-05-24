package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fingon/go-openai-proxy/internal/codex"
	"github.com/fingon/go-openai-proxy/internal/config"
)

const (
	codexRegistryURL = "https://registry.npmjs.org/@openai/codex/latest"
)

var versionRegexp = regexp.MustCompile(`\b\d+\.\d+\.\d+\b`)

type Resolver struct {
	client             *codex.Client
	codexVersion       string
	configuredModels   []string
	httpClient         *http.Client
	modelsCache        []string
	modelsCacheExpiry  time.Time
	modelsMu           sync.Mutex
	versionCache       string
	versionCacheExpiry time.Time
	versionMu          sync.Mutex
}

type Options struct {
	CodexVersion string
	HTTPClient   *http.Client
	Models       []string
}

type catalogResponse struct {
	Detail string `json:"detail"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
	Models []struct {
		Slug string `json:"slug"`
	} `json:"models"`
}

type registryResponse struct {
	Version string `json:"version"`
}

func NewResolver(client *codex.Client, options Options) *Resolver {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Resolver{
		client:           client,
		codexVersion:     strings.TrimSpace(options.CodexVersion),
		configuredModels: uniqueStrings(options.Models),
		httpClient:       httpClient,
	}
}

func (resolver *Resolver) Resolve(ctx context.Context) ([]string, error) {
	if len(resolver.configuredModels) > 0 {
		return append([]string(nil), resolver.configuredModels...), nil
	}

	resolver.modelsMu.Lock()
	if len(resolver.modelsCache) > 0 && time.Now().Before(resolver.modelsCacheExpiry) {
		models := append([]string(nil), resolver.modelsCache...)
		resolver.modelsMu.Unlock()
		return models, nil
	}
	resolver.modelsMu.Unlock()

	models, err := resolver.fetchAvailableModels(ctx)
	if err != nil {
		return nil, err
	}

	resolver.modelsMu.Lock()
	resolver.modelsCache = append([]string(nil), models...)
	resolver.modelsCacheExpiry = time.Now().Add(config.CodexModelCacheTTLSeconds * time.Second)
	resolver.modelsMu.Unlock()

	return models, nil
}

func (resolver *Resolver) CodexClientVersion(ctx context.Context) string {
	if resolver.codexVersion != "" {
		return resolver.codexVersion
	}

	resolver.versionMu.Lock()
	if resolver.versionCache != "" && time.Now().Before(resolver.versionCacheExpiry) {
		version := resolver.versionCache
		resolver.versionMu.Unlock()
		return version
	}
	resolver.versionMu.Unlock()

	version := resolver.resolveRegistryVersion(ctx)
	if version == "" {
		version = config.FallbackCodexClientVersion
		slog.Warn("could not determine Codex API version automatically", "fallback", version)
	}

	resolver.versionMu.Lock()
	resolver.versionCache = version
	resolver.versionCacheExpiry = time.Now().Add(config.CodexVersionCacheTTLSeconds * time.Second)
	resolver.versionMu.Unlock()

	return version
}

func (resolver *Resolver) fetchAvailableModels(ctx context.Context) ([]string, error) {
	version := resolver.CodexClientVersion(ctx)
	response, err := resolver.client.RawRequest(ctx, http.MethodGet, "/models?client_version="+url.QueryEscape(version), nil, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("close models response body failed", "error", err)
		}
	}()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read models response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", upstreamErrorMessage(body))
	}

	var parsed catalogResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("codex returned an invalid models response: %w", err)
	}

	models := make([]string, 0, len(parsed.Models))
	for _, model := range parsed.Models {
		if model.Slug != "" {
			models = append(models, model.Slug)
		}
	}
	models = uniqueStrings(models)
	if len(models) == 0 {
		return nil, errors.New("codex returned an empty models list")
	}

	return models, nil
}

func (resolver *Resolver) resolveRegistryVersion(ctx context.Context) string {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, codexRegistryURL, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Accept", "application/json")

	response, err := resolver.httpClient.Do(request)
	if err != nil {
		return ""
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("close registry response body failed", "error", err)
		}
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ""
	}

	var parsed registryResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return ""
	}

	return normalizeVersion(parsed.Version)
}

func upstreamErrorMessage(body []byte) string {
	if len(body) == 0 {
		return "failed to load models from Codex"
	}

	var parsed catalogResponse
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Detail != "" {
			return parsed.Detail
		}
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
	}

	return string(body)
}

func normalizeVersion(value string) string {
	return versionRegexp.FindString(strings.TrimSpace(value))
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}

	return result
}
