package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fingon/go-openai-proxy/internal/config"
)

const (
	authFilename        = "auth.json"
	oauthRefreshGrant   = "refresh_token"
	accountIDClaim      = "chatgpt_account_id"
	openAIAuthClaimName = "https://api.openai.com/auth"
)

type HTTPClient interface {
	Do(request *http.Request) (*http.Response, error)
}

type StoredTokens struct {
	AccountID    string `json:"account_id,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type File struct {
	OpenAIAPIKey string       `json:"OPENAI_API_KEY,omitempty"`
	LastRefresh  string       `json:"last_refresh,omitempty"`
	Tokens       StoredTokens `json:"tokens,omitempty"`
}

type Effective struct {
	AccountID    string
	AccessToken  string
	IDToken      string
	LastRefresh  string
	RefreshToken string
	SourcePath   string
}

type Loader struct {
	AuthFilePath string
	Client       HTTPClient
	ClientID     string
	EnsureFresh  bool
	Issuer       string
	NoRefresh    bool
	Now          func() time.Time
	TokenURL     string
}

type refreshResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

func Candidates(authFilePath string) []string {
	if authFilePath != "" {
		return []string{authFilePath}
	}

	var candidates []string
	for _, home := range []string{os.Getenv("CHATGPT_LOCAL_HOME"), os.Getenv("CODEX_HOME")} {
		if home != "" {
			candidates = append(candidates, filepath.Join(home, authFilename))
		}
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(
			candidates,
			filepath.Join(home, ".chatgpt-local", authFilename),
			filepath.Join(home, ".codex", authFilename),
		)
	}

	return uniqueStrings(candidates)
}

func ExistingPath(authFilePath string) (string, error) {
	for _, candidate := range Candidates(authFilePath) {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("stat auth file %q: %w", candidate, err)
		}
	}

	return "", os.ErrNotExist
}

func WritablePath(authFilePath string) (string, error) {
	path, err := ExistingPath(authFilePath)
	if err != nil {
		return "", err
	}

	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return "", fmt.Errorf("open auth file for writing %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close writable auth file %q: %w", path, err)
	}

	return path, nil
}

func ParseJWTClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}

	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, false
	}

	return claims, true
}

func DeriveAccountID(idToken string) string {
	claims, ok := ParseJWTClaims(idToken)
	if !ok {
		return ""
	}

	authClaim, ok := claims[openAIAuthClaimName].(map[string]any)
	if !ok {
		return ""
	}

	accountID, ok := authClaim[accountIDClaim].(string)
	if !ok {
		return ""
	}

	return accountID
}

func (effective Effective) ShouldRefresh(now time.Time) bool {
	return effective.RefreshToken != "" && shouldRefreshAccessToken(effective.AccessToken, effective.LastRefresh, now)
}

func (loader Loader) Load(ctx context.Context) (Effective, error) {
	return loader.load(ctx, true)
}

func (loader Loader) LoadStored(ctx context.Context) (Effective, error) {
	return loader.load(ctx, false)
}

func (loader Loader) Refresh(ctx context.Context) (Effective, error) {
	loader = loader.withDefaults()
	if loader.NoRefresh {
		return Effective{}, errors.New("ChatGPT token refresh is disabled")
	}

	authPath, authFile, err := loader.readAuthFile()
	if err != nil {
		return Effective{}, err
	}

	tokens := normalizeTokens(authFile.Tokens)
	accountID := tokens.AccountID
	if accountID == "" {
		accountID = DeriveAccountID(tokens.IDToken)
	}

	updatedTokens, updatedAccountID, updatedLastRefresh, err := loader.refreshedAuthFile(ctx, tokens, accountID)
	if err != nil {
		return Effective{}, err
	}
	tokens = updatedTokens
	accountID = updatedAccountID
	authFile.Tokens = updatedTokens
	authFile.LastRefresh = updatedLastRefresh
	if err := writeAuthFile(authPath, authFile); err != nil {
		return Effective{}, err
	}

	return effectiveFromTokens(authPath, authFile.LastRefresh, tokens, accountID)
}

func (loader Loader) load(ctx context.Context, allowRefresh bool) (Effective, error) {
	loader = loader.withDefaults()

	authPath, authFile, err := loader.readAuthFile()
	if err != nil {
		return Effective{}, err
	}

	tokens := normalizeTokens(authFile.Tokens)
	accountID := tokens.AccountID
	if accountID == "" {
		accountID = DeriveAccountID(tokens.IDToken)
	}

	needsRefresh := allowRefresh && loader.EnsureFresh && !loader.NoRefresh && tokens.RefreshToken != "" &&
		shouldRefreshAccessToken(tokens.AccessToken, authFile.LastRefresh, loader.Now())
	if needsRefresh {
		updatedTokens, updatedAccountID, updatedLastRefresh, err := loader.refreshedAuthFile(ctx, tokens, accountID)
		if err != nil {
			return Effective{}, err
		}
		tokens = updatedTokens
		accountID = updatedAccountID
		authFile.Tokens = updatedTokens
		authFile.LastRefresh = updatedLastRefresh
		if err := writeAuthFile(authPath, authFile); err != nil {
			return Effective{}, err
		}
	}

	return effectiveFromTokens(authPath, authFile.LastRefresh, tokens, accountID)
}

func (loader Loader) withDefaults() Loader {
	if loader.Client == nil {
		loader.Client = http.DefaultClient
	}
	if loader.ClientID == "" {
		loader.ClientID = config.DefaultOAuthClientID
	}
	if loader.Issuer == "" {
		loader.Issuer = config.DefaultOAuthIssuer
	}
	if loader.Now == nil {
		loader.Now = time.Now
	}

	return loader
}

func effectiveFromTokens(authPath, lastRefresh string, tokens StoredTokens, accountID string) (Effective, error) {
	if tokens.AccessToken == "" {
		return Effective{}, errors.New("ChatGPT access token not found. Run `codex login` to create auth.json")
	}
	if accountID == "" {
		return Effective{}, errors.New("ChatGPT account id not found in auth.json. Run `codex login` to create auth.json")
	}

	return Effective{
		AccountID:    accountID,
		AccessToken:  tokens.AccessToken,
		IDToken:      tokens.IDToken,
		LastRefresh:  lastRefresh,
		RefreshToken: tokens.RefreshToken,
		SourcePath:   authPath,
	}, nil
}

func (loader Loader) refreshedAuthFile(ctx context.Context, tokens StoredTokens, accountID string) (StoredTokens, string, string, error) {
	refreshed, err := loader.refreshTokens(ctx, tokens.RefreshToken)
	if err != nil {
		return StoredTokens{}, "", "", err
	}

	if refreshed.AccessToken != "" {
		tokens.AccessToken = refreshed.AccessToken
	}
	if refreshed.IDToken != "" {
		tokens.IDToken = refreshed.IDToken
	}
	if refreshed.RefreshToken != "" {
		tokens.RefreshToken = refreshed.RefreshToken
	}
	if refreshedAccountID := DeriveAccountID(tokens.IDToken); refreshedAccountID != "" {
		accountID = refreshedAccountID
	}
	if accountID == "" {
		accountID = tokens.AccountID
	}
	tokens.AccountID = accountID

	return tokens, accountID, loader.Now().UTC().Format(time.RFC3339Nano), nil
}

func (loader Loader) readAuthFile() (string, File, error) {
	path, err := ExistingPath(loader.AuthFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", File{}, missingAuthError(loader.AuthFilePath)
		}

		return "", File{}, err
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return "", File{}, fmt.Errorf("read auth file %q: %w", path, err)
	}

	var authFile File
	if err := json.Unmarshal(content, &authFile); err != nil {
		return "", File{}, fmt.Errorf("parse auth file %q: %w", path, err)
	}

	return path, authFile, nil
}

func missingAuthError(authFilePath string) error {
	if authFilePath != "" {
		return fmt.Errorf("no auth file was found at %s. Run `codex login` and try again", authFilePath)
	}

	return fmt.Errorf("no auth file was found in the default search paths: %s. Run `codex login` and try again", strings.Join(Candidates(""), ", "))
}

func (loader Loader) refreshTokens(ctx context.Context, refreshToken string) (refreshResponse, error) {
	tokenURL := loader.TokenURL
	if tokenURL == "" {
		tokenURL = strings.TrimRight(loader.Issuer, "/") + "/oauth/token"
	}

	body, err := json.Marshal(map[string]string{
		"client_id":     loader.ClientID,
		"grant_type":    oauthRefreshGrant,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return refreshResponse{}, fmt.Errorf("marshal token refresh body: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return refreshResponse{}, fmt.Errorf("create token refresh request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := loader.Client.Do(request)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("refresh ChatGPT tokens: %w", err)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			slog.Warn("close token refresh response body failed", "error", err)
		}
	}()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("read token refresh response: %w", err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return refreshResponse{}, refreshError(response.StatusCode, response.Status, responseBody)
	}

	var parsed refreshResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return refreshResponse{}, fmt.Errorf("parse token refresh response: %w", err)
	}
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = refreshToken
	}

	return parsed, nil
}

func shouldRefreshAccessToken(accessToken, lastRefresh string, now time.Time) bool {
	if accessToken == "" {
		return true
	}

	claims, ok := ParseJWTClaims(accessToken)
	if ok {
		if expiry, ok := claims["exp"].(float64); ok {
			expiryTime := time.Unix(int64(expiry), 0)
			if !expiryTime.After(now) {
				return true
			}
		}
	}

	if lastRefresh == "" {
		return false
	}

	refreshedAt, err := time.Parse(time.RFC3339Nano, lastRefresh)
	if err != nil {
		refreshedAt, err = time.Parse(time.RFC3339, lastRefresh)
	}
	if err != nil {
		return false
	}

	return refreshedAt.Before(now.AddDate(0, 0, -config.TokenRefreshIntervalDays))
}

func refreshError(statusCode int, status string, body []byte) error {
	if statusCode != http.StatusUnauthorized {
		return fmt.Errorf("refresh ChatGPT tokens: upstream returned %s: %s", status, tryParseErrorMessage(body))
	}

	code := refreshErrorCode(body)
	switch strings.ToLower(code) {
	case "refresh_token_expired":
		return errors.New("your access token could not be refreshed because your refresh token has expired; please log out and sign in again")
	case "refresh_token_reused":
		return errors.New("your access token could not be refreshed because your refresh token was already used; please log out and sign in again")
	case "refresh_token_invalidated":
		return errors.New("your access token could not be refreshed because your refresh token was revoked; please log out and sign in again")
	default:
		if code != "" {
			slog.Warn("unknown token refresh 401 code", "code", code)
		}
		return errors.New("your access token could not be refreshed; please log out and sign in again")
	}
}

func refreshErrorCode(body []byte) string {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}

	if errorValue, ok := parsed["error"]; ok {
		switch value := errorValue.(type) {
		case string:
			return value
		case map[string]any:
			if code, ok := value["code"].(string); ok {
				return code
			}
		}
	}
	if code, ok := parsed["code"].(string); ok {
		return code
	}

	return ""
}

func tryParseErrorMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			return parsed.Error.Message
		}
		if parsed.Message != "" {
			return parsed.Message
		}
	}
	return string(body)
}

func normalizeTokens(tokens StoredTokens) StoredTokens {
	return StoredTokens{
		AccountID:    strings.TrimSpace(tokens.AccountID),
		AccessToken:  strings.TrimSpace(tokens.AccessToken),
		IDToken:      strings.TrimSpace(tokens.IDToken),
		RefreshToken: strings.TrimSpace(tokens.RefreshToken),
	}
}

func writeAuthFile(path string, authFile File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir auth directory %q: %w", filepath.Dir(path), err)
	}

	content, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth file: %w", err)
	}
	content = append(content, '\n')

	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write auth file %q: %w", path, err)
	}

	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
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
