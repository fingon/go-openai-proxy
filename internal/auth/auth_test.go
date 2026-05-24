package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

type roundTripFunc func(request *http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestParseJWTClaimsAndDeriveAccountID(t *testing.T) {
	token := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-1"},
	})

	claims, ok := ParseJWTClaims(token)
	assert.Assert(t, ok)
	assert.Equal(t, claims["https://api.openai.com/auth"].(map[string]any)["chatgpt_account_id"], "acct-1")
	assert.Equal(t, DeriveAccountID(token), "acct-1")
}

func TestLoadReturnsStoredTokens(t *testing.T) {
	authPath := writeTestAuthFile(t, File{Tokens: StoredTokens{
		AccountID:    "acct-1",
		AccessToken:  "access",
		RefreshToken: "refresh",
	}})

	loader := Loader{
		AuthFilePath: authPath,
		Client:       http.DefaultClient,
		EnsureFresh:  false,
	}
	effective, err := loader.Load(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, effective.AccessToken, "access")
	assert.Equal(t, effective.AccountID, "acct-1")
	assert.Equal(t, effective.SourcePath, authPath)
}

func TestWritablePathReturnsExistingWritablePath(t *testing.T) {
	authPath := writeTestAuthFile(t, File{Tokens: StoredTokens{
		AccountID:   "acct-1",
		AccessToken: "access",
	}})

	path, err := WritablePath(authPath)
	assert.NilError(t, err)
	assert.Equal(t, path, authPath)
}

func TestWritablePathRejectsMissingFile(t *testing.T) {
	_, err := WritablePath(filepath.Join(t.TempDir(), "missing.json"))

	assert.Assert(t, errors.Is(err, os.ErrNotExist))
}

func TestLoadRefreshesExpiredToken(t *testing.T) {
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	expiredAccessToken := testJWT(map[string]any{"exp": now.Add(-time.Minute).Unix()})
	refreshedIDToken := testJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"},
	})
	authPath := writeTestAuthFile(t, File{
		LastRefresh: "2020-01-01T00:00:00Z",
		Tokens: StoredTokens{
			AccessToken:  expiredAccessToken,
			RefreshToken: "refresh",
		},
	})

	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		assert.Equal(t, request.URL.String(), "https://auth.example.test/token")
		return &http.Response{
			Body:       ioNopCloser(`{"access_token":"new-access","id_token":` + quote(refreshedIDToken) + `,"refresh_token":"new-refresh"}`),
			Header:     make(http.Header),
			StatusCode: http.StatusOK,
		}, nil
	})}

	loader := Loader{
		AuthFilePath: authPath,
		Client:       client,
		EnsureFresh:  true,
		Now:          func() time.Time { return now },
		TokenURL:     "https://auth.example.test/token",
	}
	effective, err := loader.Load(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, effective.AccessToken, "new-access")
	assert.Equal(t, effective.AccountID, "acct-2")

	content, err := os.ReadFile(authPath)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(content), `"access_token": "new-access"`))
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

func writeTestAuthFile(t *testing.T, authFile File) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	content, err := json.MarshalIndent(authFile, "", "  ")
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(path, content, 0o600))
	return path
}

func quote(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func ioNopCloser(value string) *readCloser {
	return &readCloser{Reader: strings.NewReader(value)}
}

type readCloser struct {
	*strings.Reader
}

func (closer *readCloser) Close() error {
	return nil
}
