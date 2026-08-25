package config

const (
	DefaultHost               = "127.0.0.1"
	DefaultPort               = 17132
	DefaultCodexBaseURL       = "https://chatgpt.com/backend-api/codex"
	DefaultOAuthClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultOAuthIssuer        = "https://auth.openai.com"
	DefaultOAuthTokenURL      = "https://auth.openai.com/oauth/token"
	DefaultExcludedModel      = "codex-auto-review"
	OpenAIBetaResponsesHeader = "responses=experimental"
	CodexOriginator           = "codex_cli_rs"
	// MinCodexIdentityVersion is the lowest client version the upstream Codex
	// endpoint still accepts; lower versions get 404s.
	MinCodexIdentityVersion            = "0.144.0"
	FallbackCodexIdentityVersion       = MinCodexIdentityVersion
	CodexModelCacheTTLSeconds          = 300
	CodexVersionCacheTTLSeconds        = 3600
	TokenRefreshIntervalDays           = 8
	MaxRequestBodyBytes          int64 = 64 << 20
)
