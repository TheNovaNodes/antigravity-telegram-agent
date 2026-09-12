package harvester

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

type secretRule struct {
	Name    string
	Regex   *regexp.Regexp
	Replace string
}

var secretRules = []secretRule{
	{
		Name:    "GITHUB_PAT_FINE_GRAINED",
		Regex:   regexp.MustCompile(`github_pat_[a-zA-Z0-9_]{70,95}`),
		Replace: "[REDACTED_SECRET:GITHUB_PAT]",
	},
	{
		Name:    "GITHUB_PAT_CLASSIC",
		Regex:   regexp.MustCompile(`ghp_[a-zA-Z0-9]{36}`),
		Replace: "[REDACTED_SECRET:GITHUB_PAT]",
	},
	{
		Name:    "TELEGRAM_BOT_TOKEN",
		Regex:   regexp.MustCompile(`\b\d{8,10}:[A-Za-z0-9_-]{30,40}\b`),
		Replace: "[REDACTED_SECRET:TELEGRAM_BOT_TOKEN]",
	},
	{
		Name:    "PRIVATE_KEY",
		Regex:   regexp.MustCompile(`(?s)-----BEGIN[ A-Z0-9_-]*PRIVATE KEY-----.*?-----END[ A-Z0-9_-]*PRIVATE KEY-----`),
		Replace: "[REDACTED_SECRET:PRIVATE_KEY]",
	},
	{
		Name:    "BEARER_TOKEN",
		Regex:   regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9_\-\.]{20,}`),
		Replace: "Bearer [REDACTED_SECRET:BEARER_TOKEN]",
	},
	{
		Name:    "ANTHROPIC_API_KEY",
		Regex:   regexp.MustCompile(`\bsk-ant-[a-zA-Z0-9_-]{20,}\b`),
		Replace: "[REDACTED_SECRET:ANTHROPIC_KEY]",
	},
	{
		Name:    "OPENAI_API_KEY",
		Regex:   regexp.MustCompile(`\bsk-[a-zA-Z0-9]{32,}\b`),
		Replace: "[REDACTED_SECRET:OPENAI_KEY]",
	},
	{
		Name:    "GENERIC_SK_KEY",
		Regex:   regexp.MustCompile(`\bsk_[a-zA-Z0-9_]{20,}\b`),
		Replace: "[REDACTED_SECRET:API_KEY]",
	},
	{
		Name:    "GOOGLE_API_KEY",
		Regex:   regexp.MustCompile(`\bAIza[0-9A-Za-z-_]{30,40}\b`),
		Replace: "[REDACTED_SECRET:GOOGLE_API_KEY]",
	},
	{
		Name:    "JWT_TOKEN",
		Regex:   regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		Replace: "[REDACTED_SECRET:JWT_TOKEN]",
	},
}

var hostPathRegex = regexp.MustCompile(`(?:/etc/antigravity-bot/accounts/[^/]+/\.gemini/antigravity-cli/brain/[0-9a-fA-F-]+/|(?:/root|/(?:home|Users)/[^/]+|~)/\.gemini/antigravity-cli/brain/[0-9a-fA-F-]+/)`)

// SanitizeContent scrubs sensitive secrets and relinks internal host paths.
// Returns the sanitized string and the count of redactions performed.
func SanitizeContent(content string) (string, int) {
	if content == "" {
		return "", 0
	}

	sanitized := content
	redactions := 0

	for _, rule := range secretRules {
		matches := rule.Regex.FindAllStringIndex(sanitized, -1)
		if len(matches) > 0 {
			redactions += len(matches)
			sanitized = rule.Regex.ReplaceAllString(sanitized, rule.Replace)
		}
	}

	// Relink host paths to clean relative paths
	sanitized = hostPathRegex.ReplaceAllString(sanitized, "./artifacts/")

	return sanitized, redactions
}

// ComputeSHA256 returns hex-encoded SHA-256 of normalized content (trailing whitespace trimmed).
func ComputeSHA256(content string) string {
	normalized := strings.TrimRight(content, " \t\r\n")
	hasher := sha256.New()
	hasher.Write([]byte(normalized))
	return hex.EncodeToString(hasher.Sum(nil))
}
