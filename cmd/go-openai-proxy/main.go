package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/alecthomas/kong"
	"github.com/fingon/go-openai-proxy/internal/auth"
	"github.com/fingon/go-openai-proxy/internal/codex"
	"github.com/fingon/go-openai-proxy/internal/models"
	"github.com/fingon/go-openai-proxy/internal/server"
)

type cli struct {
	BaseURL       string `env:"GO_OPENAI_PROXY_BASE_URL" help:"Override the upstream Codex base URL." name:"base-url"`
	CodexVersion  string `env:"GO_OPENAI_PROXY_CODEX_VERSION" help:"Codex API version to use for model discovery." name:"codex-version"`
	Host          string `env:"GO_OPENAI_PROXY_HOST" help:"Host interface to bind to." name:"host"`
	Models        string `env:"GO_OPENAI_PROXY_MODELS" help:"Comma-separated model ids to expose from /v1/models." name:"models"`
	NoRefresh     bool   `env:"GO_OPENAI_PROXY_NO_REFRESH" help:"Reload auth.json on 401, but do not call the OAuth refresh endpoint." name:"no-refresh"`
	OAuthClientID string `env:"GO_OPENAI_PROXY_OAUTH_CLIENT_ID" help:"Override the OAuth client id used for refresh." name:"oauth-client-id"`
	OAuthFile     string `env:"GO_OPENAI_PROXY_OAUTH_FILE" help:"Path to the local auth.json file." name:"oauth-file"`
	OAuthTokenURL string `env:"GO_OPENAI_PROXY_OAUTH_TOKEN_URL" help:"Override the OAuth token URL used for refresh." name:"oauth-token-url"`
	Port          int    `env:"GO_OPENAI_PROXY_PORT" help:"Port to listen on." name:"port"`
	Verbose       bool   `env:"GO_OPENAI_PROXY_VERBOSE" help:"Enable verbose logging." name:"v" short:"v"`
}

func main() {
	os.Exit(run())
}

func run() int {
	var commandLine cli
	parser := kong.Must(&commandLine,
		kong.Name("go-openai-proxy"),
		kong.Description("OpenAI-compatible local endpoint backed by the local ChatGPT/Codex OAuth cache."),
	)
	_, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	logLevel := slog.LevelInfo
	if commandLine.Verbose {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	authFilePath, err := auth.WritablePath(commandLine.OAuthFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Error("auth file not found", "oauth_file", commandLine.OAuthFile, "candidates", strings.Join(auth.Candidates(commandLine.OAuthFile), ","))
			return 1
		}
		slog.Error("check writable auth file failed", "error", err)
		return 1
	}
	commandLine.OAuthFile = authFilePath

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	availableModels, err := startupModels(ctx, commandLine)
	if err != nil {
		slog.Error("load models failed", "error", err)
		return 1
	}

	running, err := server.Start(ctx, server.Options{
		AuthFilePath: commandLine.OAuthFile,
		BaseURL:      commandLine.BaseURL,
		ClientID:     commandLine.OAuthClientID,
		CodexVersion: commandLine.CodexVersion,
		Host:         commandLine.Host,
		HTTPClient:   http.DefaultClient,
		Models:       parseModels(commandLine.Models),
		NoRefresh:    commandLine.NoRefresh,
		Port:         commandLine.Port,
		TokenURL:     commandLine.OAuthTokenURL,
	})
	if err != nil {
		slog.Error("go-openai-proxy failed", "error", err)
		return 1
	}

	if _, err := fmt.Fprintln(os.Stdout, startupMessage(running.URL, availableModels)); err != nil {
		slog.Error("write startup message failed", "error", err)
		return 1
	}
	<-ctx.Done()
	return 0
}

func startupModels(ctx context.Context, commandLine cli) ([]string, error) {
	codexClient, err := codex.NewClient(codex.Options{
		AuthFilePath: commandLine.OAuthFile,
		BaseURL:      commandLine.BaseURL,
		Client:       http.DefaultClient,
		ClientID:     commandLine.OAuthClientID,
		EnsureFresh:  true,
		NoRefresh:    commandLine.NoRefresh,
		TokenURL:     commandLine.OAuthTokenURL,
	})
	if err != nil {
		return nil, err
	}

	resolver := models.NewResolver(codexClient, models.Options{
		CodexVersion: commandLine.CodexVersion,
		HTTPClient:   http.DefaultClient,
		Models:       parseModels(commandLine.Models),
	})

	return resolver.Resolve(ctx)
}

func parseModels(value string) []string {
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		model := strings.TrimSpace(part)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}

	return models
}

func startupMessage(baseURL string, availableModels []string) string {
	return strings.Join([]string{
		"OpenAI-compatible endpoint ready at " + baseURL,
		"Use this as your OpenAI base URL. No API key is required.",
		"",
		"Available Models: " + strings.Join(availableModels, ", "),
	}, "\n")
}
