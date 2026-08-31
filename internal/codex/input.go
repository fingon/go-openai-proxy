package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	maxFunctionCallIDLengthBytes = 64
	functionCallIDPrefix         = "fc_"
	reasoningEncryptedContent    = "reasoning.encrypted_content"
)

const (
	itemTypeMessage = "message"
	roleDeveloper   = "developer"
	roleSystem      = "system"
)

var legacyCallIDPrefixes = []string{"call_", "ctc_", "tsc_"}

// codexToolCallItemTypes are input items identified by a call_id that upstream
// validates (ids must begin with "fc").
var codexToolCallItemTypes = map[string]struct{}{
	"function_call":           {},
	"custom_tool_call":        {},
	"local_shell_call":        {},
	"mcp_tool_call":           {},
	"tool_search_call":        {},
	"function_call_output":    {},
	"custom_tool_call_output": {},
	"mcp_list_tools":          {},
}

// sanitizeResponsesPayload rewrites a Responses payload in place so it is
// acceptable to the ChatGPT OAuth codex endpoint: system/developer messages are
// promoted into `instructions`, string inputs become arrays, tool result rows
// are converted to function_call_output items, replayed reasoning items lose
// their server-side ids, and call ids are normalized to the required prefix.
func sanitizeResponsesPayload(payload map[string]any) bool {
	modified := coerceStringInput(payload)
	if input, ok := payload["input"].([]any); ok {
		if filtered, itemModified := sanitizeInputItems(input, payload); itemModified {
			payload["input"] = filtered
			modified = true
		}
	}
	if ensureReasoningInclude(payload) {
		modified = true
	}

	return modified
}

func coerceStringInput(payload map[string]any) bool {
	text, ok := payload["input"].(string)
	if !ok {
		return false
	}
	if strings.TrimSpace(text) == "" {
		payload["input"] = []any{}
	} else {
		payload["input"] = []any{map[string]any{
			"content": text,
			"role":    "user",
			"type":    itemTypeMessage,
		}}
	}

	return true
}

func sanitizeInputItems(input []any, payload map[string]any) ([]any, bool) {
	filtered := make([]any, 0, len(input))
	modified := false
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			filtered = append(filtered, rawItem)
			continue
		}

		switch {
		case isPromotableRole(item):
			text := extractTextFromContent(item["content"])
			appendPromotedInstructions(payload, text)
			if hasLosslessTextContent(item["content"]) {
				// The full content now lives in instructions.
				modified = true
				continue
			}
			if item["role"] != roleDeveloper {
				item["role"] = roleDeveloper
				modified = true
			}
		case convertsToToolOutput(item):
			filtered = append(filtered, convertToolMessage(item))
			modified = true
			continue
		}

		if normalizeCallIDs(item) {
			modified = true
		}
		if itemType, _ := item["type"].(string); itemType == "reasoning" && sanitizeReasoningItem(item) {
			modified = true
		}
		filtered = append(filtered, item)
	}

	return filtered, modified
}

func isPromotableRole(item map[string]any) bool {
	itemType, _ := item["type"].(string)
	if itemType != "" && itemType != itemTypeMessage {
		return false
	}
	switch role, _ := item["role"].(string); role {
	case roleSystem, roleDeveloper:
		return true
	default:
		return false
	}
}

func appendPromotedInstructions(payload map[string]any, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	existing, _ := payload["instructions"].(string)
	existing = strings.TrimRight(existing, " \t\r\n")
	switch existing {
	case "":
		payload["instructions"] = text
	default:
		payload["instructions"] = existing + "\n\n" + text
	}
}

func convertsToToolOutput(item map[string]any) bool {
	itemType, _ := item["type"].(string)
	if itemType != "" {
		return false
	}
	role, _ := item["role"].(string)

	return role == "tool"
}

func convertToolMessage(item map[string]any) map[string]any {
	callID, _ := firstNonEmptyString(item["call_id"], item["tool_call_id"], item["id"])
	output, _ := item["output"].(string)
	if output == "" {
		output = extractTextFromContent(item["content"])
	}

	return map[string]any{
		"call_id": callID,
		"output":  output,
		"type":    "function_call_output",
	}
}

func normalizeCallIDs(item map[string]any) bool {
	itemTypeRaw, _ := item["type"].(string)
	itemType := strings.TrimSpace(itemTypeRaw)

	if _, ok := codexToolCallItemTypes[itemType]; !ok {
		// Message-like items must not carry call ids.
		if _, ok := item["call_id"]; ok && itemType != "" && itemType != itemTypeMessage {
			delete(item, "call_id")
			return true
		}
		return false
	}

	modified := false
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		if fallback, found := firstNonEmptyString(item["id"]); found {
			callID = fallback
			item["call_id"] = fallback
			delete(item, "id")
			modified = true
		}
	}
	if normalized := normalizeCallID(callID); callID != "" && normalized != callID {
		item["call_id"] = normalized
		modified = true
	}

	return modified
}

func sanitizeReasoningItem(item map[string]any) bool {
	modified := false
	if _, ok := item["id"]; ok {
		delete(item, "id")
		modified = true
	}
	if summary, ok := item["summary"]; !ok || summary == nil {
		item["summary"] = []any{}
		modified = true
	}

	return modified
}

func ensureReasoningInclude(payload map[string]any) bool {
	const encrypted = reasoningEncryptedContent
	switch existing := payload["include"].(type) {
	case nil:
		payload["include"] = []any{encrypted}
		return true
	case []any:
		for _, value := range existing {
			if value, ok := value.(string); ok && value == encrypted {
				return false
			}
		}
		payload["include"] = append(existing, encrypted)
		return true
	case []string:
		for _, value := range existing {
			if value == encrypted {
				return false
			}
		}
		values := make([]any, 0, len(existing)+1)
		for _, value := range existing {
			values = append(values, value)
		}
		values = append(values, encrypted)
		payload["include"] = values
		return true
	default:
		return false
	}
}

func normalizeCallID(id string) string {
	trimmed := strings.TrimSpace(id)
	switch {
	case trimmed == "":
		return trimmed
	case strings.HasPrefix(trimmed, functionCallIDPrefix):
	default:
		prefixStripped := trimmed
		for _, legacyPrefix := range legacyCallIDPrefixes {
			if stripped, ok := strings.CutPrefix(prefixStripped, legacyPrefix); ok {
				prefixStripped = stripped
				break
			}
		}
		trimmed = functionCallIDPrefix + prefixStripped
	}
	if len(trimmed) <= maxFunctionCallIDLengthBytes {
		return trimmed
	}

	return compactCallID(trimmed)
}

func compactCallID(id string) string {
	digest := sha256.Sum256([]byte("go-openai-proxy:call-id:v1:" + id))
	encoded := hex.EncodeToString(digest[:])

	return functionCallIDPrefix + encoded[:maxFunctionCallIDLengthBytes-len(functionCallIDPrefix)]
}

// extractTextFromContent flattens Responses-style content (a plain string or an
// array of typed content parts) into a single text string.
func extractTextFromContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var builder strings.Builder
		for _, part := range typed {
			partMap, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch partType, _ := partMap["type"].(string); partType {
			case "", "text", "input_text", "output_text":
				if text, ok := partMap["text"].(string); ok {
					builder.WriteString(text)
				}
			}
		}
		return builder.String()
	default:
		return ""
	}
}

// hasLosslessTextContent reports whether the content consists exclusively of
// text parts, so promoting it into instructions loses nothing.
func hasLosslessTextContent(content any) bool {
	switch typed := content.(type) {
	case string:
		return true
	case nil:
		return true
	case []any:
		if len(typed) == 0 {
			return true
		}
		for _, part := range typed {
			partMap, ok := part.(map[string]any)
			if !ok {
				return false
			}
			partType, _ := partMap["type"].(string)
			switch partType {
			case "", "text", "input_text", "output_text":
				if _, ok := partMap["text"].(string); !ok {
					return false
				}
			default:
				return false
			}
		}
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...any) (string, bool) {
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return text, true
		}
	}

	return "", false
}
