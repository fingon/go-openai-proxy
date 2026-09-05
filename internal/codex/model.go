package codex

import (
	"fmt"
	"strings"
)

const (
	FastModelSuffix     = "fast"
	PriorityServiceTier = "priority"
)

var apiReasoningEfforts = [...]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

type ModelAlias struct {
	Model  string
	Effort string
	Fast   bool
}

func IsAPIReasoningEffort(effort string) bool {
	for _, supported := range apiReasoningEfforts {
		if effort == supported {
			return true
		}
	}

	return false
}

func NormalizeReasoningEfforts(values []string) ([]string, error) {
	var normalized []string
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		effort := strings.ToLower(strings.TrimSpace(value))
		if effort == "" {
			continue
		}
		if !IsAPIReasoningEffort(effort) {
			return nil, fmt.Errorf("invalid reasoning effort %q; valid choices: %s", effort, strings.Join(apiReasoningEfforts[:], ", "))
		}
		if _, exists := seen[effort]; exists {
			continue
		}
		seen[effort] = struct{}{}
		normalized = append(normalized, effort)
	}

	return normalized, nil
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
