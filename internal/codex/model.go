package codex

import "strings"

const (
	FastModelSuffix     = "fast"
	PriorityServiceTier = "priority"
)

type ModelAlias struct {
	Model  string
	Effort string
	Fast   bool
}

func IsAPIReasoningEffort(effort string) bool {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func ParseModelAlias(model string) ModelAlias {
	trimmed := strings.TrimSpace(model)
	alias := ModelAlias{Model: trimmed}
	base := trimmed

	if fastBase, ok := strings.CutSuffix(base, "-"+FastModelSuffix); ok && fastBase != "" {
		alias.Fast = true
		base = fastBase
	}

	index := strings.LastIndex(base, "-")
	if index > 0 {
		effort := strings.ToLower(base[index+1:])
		if IsAPIReasoningEffort(effort) {
			alias.Effort = effort
			base = base[:index]
		}
	}

	if alias.Fast || alias.Effort != "" {
		alias.Model = base
	}

	return alias
}

func ApplyModelAlias(payload map[string]any, model string) bool {
	alias := ParseModelAlias(model)
	modified := false
	if alias.Model != model {
		payload["model"] = alias.Model
		modified = true
	}

	if alias.Effort != "" {
		if reasoning, ok := payload["reasoning"].(map[string]any); ok {
			if _, exists := reasoning["effort"]; !exists {
				reasoning["effort"] = alias.Effort
				modified = true
			}
		} else if _, exists := payload["reasoning"]; !exists {
			payload["reasoning"] = map[string]any{"effort": alias.Effort}
			modified = true
		}
	}

	if alias.Fast {
		if _, exists := payload["service_tier"]; !exists {
			payload["service_tier"] = PriorityServiceTier
			modified = true
		}
	}
	if serviceTier, ok := payload["service_tier"].(string); ok && serviceTier == FastModelSuffix {
		payload["service_tier"] = PriorityServiceTier
		modified = true
	}

	return modified
}
