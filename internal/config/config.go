package config

const (
	DefaultHost                       = "127.0.0.1"
	DefaultPort                       = 17132
	DefaultCodexBaseURL               = "https://chatgpt.com/backend-api/codex"
	DefaultOAuthClientID              = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultOAuthIssuer                = "https://auth.openai.com"
	DefaultOAuthTokenURL              = "https://auth.openai.com/oauth/token"
	FallbackCodexClientVersion        = "0.111.0"
	OpenAIBetaResponsesHeader         = "responses=experimental"
	CodexModelCacheTTLSeconds         = 300
	CodexVersionCacheTTLSeconds       = 3600
	RefreshExpiryMarginSeconds        = 300
	RefreshIntervalSeconds            = 3300
	MaxRequestBodyBytes         int64 = 64 << 20
)
