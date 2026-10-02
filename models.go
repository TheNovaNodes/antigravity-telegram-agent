package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// AgyModel describes an LLM model discovered from Antigravity CLI.
type AgyModel struct {
	ID    string
	Name  string
	Emoji string
}

var (
	availableModels []AgyModel
	modelsMu        sync.RWMutex
	ansiRegex       = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)
)

// getEmojiForModel returns an appropriate emoji badge based on the model ID.
func getEmojiForModel(id string) string {
	id = strings.ToLower(id)
	if strings.Contains(id, "flash") {
		return "⚡"
	}
	if strings.Contains(id, "pro") {
		return "🧠"
	}
	if strings.Contains(id, "claude") {
		return "🟣"
	}
	if strings.Contains(id, "oss") || strings.Contains(id, "llama") {
		return "🟢"
	}
	return "🤖"
}

// isValidModelID validates whether the extracted string is a legitimate model identifier.
func isValidModelID(id string) bool {
	if len(id) < 2 || len(id) > 64 {
		return false
	}
	lower := strings.ToLower(id)
	if strings.Contains(lower, "fetching") || strings.Contains(lower, "error") || strings.Contains(lower, "warning") || strings.Contains(lower, "please") {
		return false
	}
	first := id[0]
	if !((first >= 'a' && first <= 'z') || (first >= 'A' && first <= 'Z') || (first >= '0' && first <= '9')) {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// parseModelsOutput cleans terminal escapes, carriage returns, and spinner artifacts,
// then parses model IDs and human-readable names.
func parseModelsOutput(output string) []AgyModel {
	cleaned := ansiRegex.ReplaceAllString(output, "")
	cleaned = strings.ReplaceAll(cleaned, "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")

	var parsed []AgyModel
	seen := make(map[string]bool)

	lines := strings.Split(cleaned, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "fetching available models") || strings.HasPrefix(lower, "error:") || strings.Contains(lower, "please sign in") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) >= 2 {
			id := parts[0]
			if !isValidModelID(id) {
				continue
			}
			if seen[id] {
				continue
			}
			seen[id] = true

			name := strings.TrimSpace(strings.Join(parts[1:], " "))
			if name == "" {
				continue
			}
			parsed = append(parsed, AgyModel{
				ID:    id,
				Name:  name,
				Emoji: getEmojiForModel(id),
			})
		}
	}
	return parsed
}

// getPrimaryAccountHome resolves the home directory of a primary authorized account for CLI operations.
// Resolution order:
// 1. First registered account in GlobalAccountPool (if initialized).
// 2. Scan directories in getAccountsDir() (/etc/antigravity-bot/accounts) for an account profile.
// 3. Fallback to getSystemBaseHome().
func getPrimaryAccountHome() string {
	if GlobalAccountPool != nil {
		accounts := GlobalAccountPool.ListAccounts()
		for _, acc := range accounts {
			if acc != nil && acc.HomeDir != "" {
				if fi, err := os.Stat(acc.HomeDir); err == nil && fi.IsDir() {
					return acc.HomeDir
				}
			}
		}
	}

	accountsDir := getAccountsDir()
	if entries, err := os.ReadDir(accountsDir); err == nil {
		var firstDir string
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			cand := filepath.Join(accountsDir, e.Name())
			fi, err := os.Stat(cand)
			if err != nil || !fi.IsDir() {
				continue
			}
			if firstDir == "" {
				firstDir = cand
			}
			// Prefer directory containing .gemini credentials / configuration
			if geminiFi, err := os.Stat(filepath.Join(cand, ".gemini")); err == nil && geminiFi.IsDir() {
				return cand
			}
		}
		if firstDir != "" {
			return firstDir
		}
	}

	return getSystemBaseHome()
}

// fetchModels dynamically queries the available models from the Antigravity CLI with a strict timeout.
func fetchModels() {
	agyPath := getAgyPath()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agyPath, "models")
	cmd.WaitDelay = 2 * time.Second
	cmd.Env = buildChildEnv(getPrimaryAccountHome())

	out, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to fetch dynamic models: %v", err)
		return
	}

	parsed := parseModelsOutput(string(out))
	if len(parsed) > 0 {
		modelsMu.Lock()
		availableModels = parsed
		modelsMu.Unlock()
		log.Printf("Dynamically loaded %d models", len(parsed))
	}
}
