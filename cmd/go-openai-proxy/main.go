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

func main() {
	os.Exit(run())
}

func run() int {
	var options server.Options
	parser := kong.Must(&options,
		kong.Name("go-openai-proxy"),
		kong.Description("OpenAI-compatible local endpoint backed by the local ChatGPT/Codex OAuth cache."),
	)
	_, err := parser.Parse(os.Args[1:])
	parser.FatalIfErrorf(err)

	logLevel := slog.LevelInfo
	if options.Verbose {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	authFilePath, err := auth.WritablePath(options.AuthFilePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			slog.Error("auth file not found", "oauth_file", options.AuthFilePath, "candidates", auth.Candidates(options.AuthFilePath))
			return 1
		}
		slog.Error("check writable auth file failed", "error", err)
		return 1
	}
	options.AuthFilePath = authFilePath
	options.HTTPClient = http.DefaultClient

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	availableModels, err := startupModels(ctx, options)
	if err != nil {
		slog.Error("load models failed", "error", err)
		return 1
	}

	running, err := server.Start(ctx, options)
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

func startupModels(ctx context.Context, options server.Options) ([]string, error) {
	codexClient, err := codex.NewClient(codex.Options{
		AuthFilePath: options.AuthFilePath,
		BaseURL:      options.BaseURL,
		Client:       options.HTTPClient,
		ClientID:     options.ClientID,
		EnsureFresh:  true,
		NoRefresh:    options.NoRefresh,
		TokenURL:     options.TokenURL,
	})
	if err != nil {
		return nil, err
	}

	resolver := models.NewResolver(codexClient, models.Options{
		CodexVersion:   options.CodexVersion,
		ExcludedModels: options.ExcludedModels,
		HTTPClient:     options.HTTPClient,
		Models:         options.Models,
	})

	return resolver.Resolve(ctx)
}

func startupMessage(baseURL string, availableModels []string) string {
	return strings.Join([]string{
		"OpenAI-compatible endpoint ready at " + baseURL,
		"Use this as your OpenAI base URL. No API key is required.",
		"",
		"Available Models: " + strings.Join(availableModels, ", "),
	}, "\n")
}
