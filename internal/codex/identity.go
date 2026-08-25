package codex

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/fingon/go-openai-proxy/internal/config"
)

var identityVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

type identity struct {
	sessionID string
	userAgent string
	version   string
}

// versionResolver returns the Codex CLI version to advertise upstream; it may
// return an empty string when resolution is not possible.
type versionResolver func(ctx context.Context) string

func normalizeIdentityVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 64 || !identityVersionPattern.MatchString(version) {
		return ""
	}

	return version
}

func compareIdentityVersions(left, right string) int {
	leftParts := splitVersionParts(left)
	rightParts := splitVersionParts(right)
	for index := range max(len(leftParts), len(rightParts)) {
		var leftPart, rightPart int
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		switch {
		case leftPart < rightPart:
			return -1
		case leftPart > rightPart:
			return 1
		}
	}

	return 0
}

func splitVersionParts(version string) []int {
	core, _, _ := strings.Cut(version, "-")
	fields := strings.Split(core, ".")
	parts := make([]int, 0, len(fields))
	for _, field := range fields {
		value, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		parts = append(parts, value)
	}

	return parts
}

func clampIdentityVersion(version string) string {
	if normalizeIdentityVersion(version) == "" {
		return config.FallbackCodexIdentityVersion
	}
	if compareIdentityVersions(version, config.MinCodexIdentityVersion) < 0 {
		return config.MinCodexIdentityVersion
	}

	return version
}

func buildCodexUserAgent(version string) string {
	return fmt.Sprintf("%s/%s (%s %s) unknown", config.CodexOriginator, version, runtime.GOOS, runtime.GOARCH)
}

func newSessionID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80

	return formatUUID(bytes), nil
}

func formatUUID(bytes [16]byte) string {
	hexed := hex.EncodeToString(bytes[:])
	return strings.Join([]string{hexed[0:8], hexed[8:12], hexed[12:16], hexed[16:20], hexed[20:32]}, "-")
}

type identityCache struct {
	mu       sync.Mutex
	resolved bool
	value    identity
}

func (cache *identityCache) get(ctx context.Context, resolver versionResolver) identity {
	cache.mu.Lock()
	defer cache.mu.Unlock()

	if cache.resolved {
		return cache.value
	}

	sessionID, err := newSessionID()
	if err != nil {
		// A missing session header is cosmetic; fall back to a constant id so
		// requests still carry the expected header shape.
		sessionID = "00000000-0000-4000-8000-000000000000"
	}
	version := clampIdentityVersion(resolver(ctx))
	cache.value = identity{
		sessionID: sessionID,
		userAgent: buildCodexUserAgent(version),
		version:   version,
	}
	cache.resolved = true

	return cache.value
}
