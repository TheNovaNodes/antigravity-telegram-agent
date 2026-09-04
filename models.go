package main

import (
	"context"
	"log"
	"os/exec"
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

// fetchModels dynamically queries the available models from the Antigravity CLI with a strict timeout.
func fetchModels() {
	agyPath := getAgyPath()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, agyPath, "models")
	out, err := cmd.Output()
	if err != nil {
		log.Printf("Failed to fetch dynamic models: %v", err)
		return
	}

	var parsed []AgyModel
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			id := parts[0]
			name := strings.Join(parts[1:], " ")
			parsed = append(parsed, AgyModel{
				ID:    id,
				Name:  name,
				Emoji: getEmojiForModel(id),
			})
		}
	}

	if len(parsed) > 0 {
		modelsMu.Lock()
		availableModels = parsed
		modelsMu.Unlock()
		log.Printf("Dynamically loaded %d models", len(parsed))
	}
}
