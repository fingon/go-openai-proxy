package models

import (
	"context"
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
