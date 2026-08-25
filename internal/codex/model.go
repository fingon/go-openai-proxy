package codex

import "strings"

// SplitEffortSuffix separates a trailing effort variant slug from a model id,
// e.g. "gpt-5.3-codex-high". It returns the base model and the effort value
// ("none" maps to "minimal", matching upstream semantics), or the original
// name and an empty effort when no recognized variant suffix is present.
func SplitEffortSuffix(model string) (string, string) {
	trimmed := strings.TrimSpace(model)
	index := strings.LastIndex(trimmed, "-")
	if index <= 0 {
		return trimmed, ""
	}
	base := trimmed[:index]
	switch suffix := strings.ToLower(trimmed[index+1:]); suffix {
	case "none":
		return base, "minimal"
	case "minimal", "low", "medium", "high", "xhigh":
		return base, suffix
	default:
		return trimmed, ""
	}
}

// ApplyEffortAlias sets payload["reasoning"]["effort"] from a suffixed model
// slug when the caller did not request reasoning explicitly.
func ApplyEffortAlias(payload map[string]any, model string) bool {
	if _, ok := payload["reasoning"]; ok {
		return false
	}
	_, effort := SplitEffortSuffix(model)
	if effort == "" {
		return false
	}
	payload["reasoning"] = map[string]any{"effort": effort}

	return true
}
