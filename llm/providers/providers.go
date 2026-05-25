package providers

import "strings"

// ParseModel splits "provider/model" into (provider, model).
func ParseModel(full string) (provider, model string) {
	parts := strings.SplitN(full, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	if strings.HasPrefix(full, "claude-") {
		return "claude-oauth", full
	}
	if strings.HasPrefix(full, "gpt-") || strings.HasPrefix(full, "o1-") || strings.HasPrefix(full, "o3-") || strings.HasPrefix(full, "o4-") {
		return "openai", full
	}
	return "claude-oauth", full
}
