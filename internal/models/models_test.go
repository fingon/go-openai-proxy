package models

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fingon/go-openai-proxy/internal/config"
	"gotest.tools/v3/assert"
)

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCodexClientVersionUsesConfiguredVersion(t *testing.T) {
	resolver := NewResolver(nil, Options{CodexVersion: " 1.2.3 "})

	assert.Equal(t, resolver.CodexClientVersion(context.Background()), "1.2.3")
}

func TestCodexClientVersionUsesRegistry(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, request.URL.String(), codexRegistryURL)
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`{"version":"2.3.4"}`)),
			Header:     make(http.Header),
			StatusCode: http.StatusOK,
		}, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})

	assert.Equal(t, resolver.CodexClientVersion(context.Background()), "2.3.4")
}

func TestCodexClientVersionFallsBackWhenRegistryFails(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			Body:       io.NopCloser(strings.NewReader(`not found`)),
			Header:     make(http.Header),
			StatusCode: http.StatusNotFound,
		}, nil
	})}

	resolver := NewResolver(nil, Options{HTTPClient: client})

	assert.Equal(t, resolver.CodexClientVersion(context.Background()), config.FallbackCodexClientVersion)
}
