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
	"os/exec"
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
	excludedModels     []string
	excludedEfforts    []string
	excludedEffortsErr error
	httpClient         *http.Client
	modelsCache        []string
	modelsCacheExpiry  time.Time
	modelsMu           sync.Mutex
	versionCache       string
	versionCacheExpiry time.Time
	versionMu          sync.Mutex
}

type Options struct {
	CodexVersion    string
	ExcludedEfforts []string
	ExcludedModels  []string
	HTTPClient      *http.Client
	Models          []string
}

type catalogResponse struct {
	Detail string `json:"detail"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
	Models []catalogModel `json:"models"`
}

type catalogModel struct {
	AdditionalSpeedTiers []string `json:"additional_speed_tiers"`
	ServiceTiers         []struct {
		ID string `json:"id"`
	} `json:"service_tiers"`
	Slug                     string `json:"slug"`
	SupportedReasoningLevels []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

type registryResponse struct {
	Version string `json:"version"`
}

func NewResolver(client *codex.Client, options Options) *Resolver {
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	excludedEfforts, excludedEffortsErr := codex.NormalizeReasoningEfforts(options.ExcludedEfforts)

	return &Resolver{
		client:             client,
		codexVersion:       strings.TrimSpace(options.CodexVersion),
		configuredModels:   uniqueStrings(options.Models),
		excludedEfforts:    excludedEfforts,
		excludedEffortsErr: excludedEffortsErr,
		excludedModels:     uniqueStrings(options.ExcludedModels),
		httpClient:         httpClient,
	}
}

func (resolver *Resolver) Resolve(ctx context.Context) ([]string, error) {
	if resolver.excludedEffortsErr != nil {
		return nil, resolver.excludedEffortsErr
	}
	if len(resolver.configuredModels) > 0 {
		return excludeStrings(resolver.configuredModels, resolver.excludedModels), nil
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

func (resolver *Resolver) CodexClientVersion(ctx context.Context) (string, error) {
	if resolver.codexVersion != "" {
		return resolver.codexVersion, nil
	}

	resolver.versionMu.Lock()
	if resolver.versionCache != "" && time.Now().Before(resolver.versionCacheExpiry) {
		version := resolver.versionCache
		resolver.versionMu.Unlock()
		return version, nil
	}
	resolver.versionMu.Unlock()

	version, err := resolver.resolveInstalledCodexVersion(ctx)
	if err != nil {
		if !errors.Is(err, exec.ErrNotFound) {
			return "", err
		}
		version, err = resolver.resolveRegistryVersion(ctx)
		if err != nil {
			return "", err
		}
	}

	resolver.versionMu.Lock()
	resolver.versionCache = version
	resolver.versionCacheExpiry = time.Now().Add(config.CodexVersionCacheTTLSeconds * time.Second)
	resolver.versionMu.Unlock()

	return version, nil
}

func (resolver *Resolver) fetchAvailableModels(ctx context.Context) ([]string, error) {
	version, err := resolver.CodexClientVersion(ctx)
	if err != nil {
		return nil, err
	}
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

	models, foundModel := catalogModels(parsed.Models, resolver.excludedModels, resolver.excludedEfforts)
	if !foundModel {
		return nil, errors.New("codex returned an empty models list")
	}

	return models, nil
}

func catalogModels(catalog []catalogModel, excluded []string, excludedEfforts ...[]string) ([]string, bool) {
	models := make([]string, 0, len(catalog))
	foundModel := false
	var effortsExcluded []string
	if len(excludedEfforts) > 0 {
		effortsExcluded = excludedEfforts[0]
	}
	for _, model := range catalog {
		model.Slug = strings.TrimSpace(model.Slug)
		if model.Slug == "" {
			continue
		}
		foundModel = true
		if containsString(excluded, model.Slug) {
			continue
		}
		models = append(models, modelAliases(model, effortsExcluded)...)
	}

	return excludeStrings(uniqueStrings(models), excluded), foundModel
}

func modelAliases(model catalogModel, excludedEfforts ...[]string) []string {
	var effortsExcluded []string
	if len(excludedEfforts) > 0 {
		effortsExcluded = excludedEfforts[0]
	}

	efforts := make([]string, 0, len(model.SupportedReasoningLevels))
	for _, level := range model.SupportedReasoningLevels {
		effort := strings.ToLower(strings.TrimSpace(level.Effort))
		if codex.IsAPIReasoningEffort(effort) && !containsString(effortsExcluded, effort) {
			efforts = append(efforts, effort)
		}
	}
	efforts = uniqueStrings(efforts)

	aliases := make([]string, 0, 2*(len(efforts)+1))
	aliases = append(aliases, model.Slug)
	for _, effort := range efforts {
		aliases = append(aliases, model.Slug+"-"+effort)
	}
	if !modelSupportsFast(model) {
		return aliases
	}

	aliases = append(aliases, model.Slug+"-"+codex.FastModelSuffix)
	for _, effort := range efforts {
		aliases = append(aliases, model.Slug+"-"+effort+"-"+codex.FastModelSuffix)
	}

	return aliases
}

func modelSupportsFast(model catalogModel) bool {
	for _, tier := range model.ServiceTiers {
		if tier.ID == codex.PriorityServiceTier {
			return true
		}
	}
	return containsString(model.AdditionalSpeedTiers, codex.FastModelSuffix)
}

func (resolver *Resolver) resolveInstalledCodexVersion(ctx context.Context) (string, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return "", err
	}

	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("determine Codex CLI version from %s: %w: %s", path, err, strings.TrimSpace(string(output)))
	}

	version := normalizeVersion(string(output))
	if version == "" {
		return "", fmt.Errorf("determine Codex CLI version from %s: unrecognized version output %q", path, strings.TrimSpace(string(output)))
	}

	return version, nil
}

func (resolver *Resolver) resolveRegistryVersion(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, codexRegistryURL, nil)
	if err != nil {
		return "", fmt.Errorf("create Codex registry request: %w", err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := resolver.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("load Codex version from npm registry: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("close registry response body failed", "error", err)
		}
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("load Codex version from npm registry: upstream returned %s", response.Status)
	}

	var parsed registryResponse
	if err := json.NewDecoder(response.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("parse Codex registry response: %w", err)
	}

	version := normalizeVersion(parsed.Version)
	if version == "" {
		return "", fmt.Errorf("parse Codex registry response: unrecognized version %q", parsed.Version)
	}

	return version, nil
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}

	return false
}

func excludeStrings(values, excluded []string) []string {
	if len(excluded) == 0 {
		return append([]string(nil), values...)
	}

	excludedSet := make(map[string]struct{}, len(excluded))
	for _, value := range excluded {
		excludedSet[value] = struct{}{}
	}

	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := excludedSet[value]; ok {
			continue
		}
		result = append(result, value)
	}

	return result
}
