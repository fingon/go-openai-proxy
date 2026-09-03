package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCodexClientVersionUsesConfiguredVersion(t *testing.T) {
	resolver := NewResolver(nil, Options{CodexVersion: " 1.2.3 "})

	version, err := resolver.CodexClientVersion(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, version, "1.2.3")
}

func TestResolveExcludesConfiguredModels(t *testing.T) {
	const (
		excludedAutoReviewModel = "codex-auto-review"
		excludedGPTModel        = "gpt-5.2"
		includedModel           = "gpt-5.3-codex"
	)
	resolver := NewResolver(nil, Options{
		ExcludedModels: []string{excludedAutoReviewModel, excludedGPTModel},
		Models:         []string{excludedGPTModel, excludedAutoReviewModel, includedModel, includedModel},
	})

	resolved, err := resolver.Resolve(context.Background())
	assert.NilError(t, err)
	assert.DeepEqual(t, resolved, []string{includedModel})
}

func TestCatalogModelsExpandsSupportedAliases(t *testing.T) {
	var response catalogResponse
	assert.NilError(t, json.Unmarshal([]byte(`{
		"models":[
			{
				"slug":"gpt-priority",
				"supported_reasoning_levels":[
					{"effort":"low"},
					{"effort":"high"},
					{"effort":"ultra"},
					{"effort":"high"}
				],
				"service_tiers":[{"id":"priority"}]
			},
			{
				"slug":"gpt-standard",
				"supported_reasoning_levels":[{"effort":"medium"},{"effort":"persistent"}]
			},
			{
				"slug":"gpt-legacy",
				"supported_reasoning_levels":[{"effort":"max"}],
				"additional_speed_tiers":["fast"]
			},
			{"slug":"gpt-excluded","supported_reasoning_levels":[{"effort":"low"}]},
			{"slug":"gpt-priority","supported_reasoning_levels":[{"effort":"low"}]}
		]
	}`), &response))

	resolved, found := catalogModels(response.Models, []string{"gpt-standard-medium", "gpt-excluded"})
	assert.Assert(t, found)
	assert.DeepEqual(t, resolved, []string{
		"gpt-priority",
		"gpt-priority-low",
		"gpt-priority-high",
		"gpt-priority-fast",
		"gpt-priority-low-fast",
		"gpt-priority-high-fast",
		"gpt-standard",
		"gpt-legacy",
		"gpt-legacy-max",
		"gpt-legacy-fast",
		"gpt-legacy-max-fast",
	})
}

func TestCatalogModelsReportsWhetherCatalogContainsModels(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		input []catalogModel
		want  bool
	}{
		{name: "empty catalog"},
		{name: "blank slug", input: []catalogModel{{Slug: " "}}},
		{name: "excluded model", input: []catalogModel{{Slug: "gpt-5.4"}}, want: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, found := catalogModels(testCase.input, []string{"gpt-5.4"})
			assert.Equal(t, found, testCase.want)
		})
	}
}

func TestCodexClientVersionUsesInstalledCLI(t *testing.T) {
	dir := t.TempDir()
	writeCodexScript(t, dir, "codex-cli 3.4.5\n", 0)
	t.Setenv("PATH", dir)

	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatalf("registry should not be called when codex CLI is installed")
		return nil, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})
	version, err := resolver.CodexClientVersion(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, version, "3.4.5")
}

func TestCodexClientVersionUsesRegistryWhenCLIIsAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, request.URL.String(), codexRegistryURL)
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"version":"2.3.4"}`)),
			Header:     make(http.Header),
			StatusCode: http.StatusOK,
		}, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})

	version, err := resolver.CodexClientVersion(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, version, "2.3.4")
}

func TestCodexClientVersionErrorsWhenInstalledCLIOutputIsInvalid(t *testing.T) {
	dir := t.TempDir()
	writeCodexScript(t, dir, "codex-cli dev\n", 0)
	t.Setenv("PATH", dir)

	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatalf("registry should not be called when codex CLI is installed")
		return nil, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})
	_, err := resolver.CodexClientVersion(context.Background())
	assert.ErrorContains(t, err, "unrecognized version output")
}

func TestCodexClientVersionErrorsWhenRegistryFails(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`not found`)),
			Header:     make(http.Header),
			Status:     "404 Not Found",
			StatusCode: http.StatusNotFound,
		}, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})

	_, err := resolver.CodexClientVersion(context.Background())
	assert.ErrorContains(t, err, "upstream returned 404 Not Found")
}

func writeCodexScript(t *testing.T, dir, output string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, "codex")
	content := "#!/bin/sh\nprintf '%s' " + shellQuote(output) + "\nexit " + string(rune('0'+exitCode)) + "\n"
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o755))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
