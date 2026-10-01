package main

import (
	"fmt"
	"os"
)

var (
	// Version is the application version, optionally injected via -ldflags during build.
	Version = "dev"
	// GitCommit is the commit hash, injected via -ldflags during build.
	GitCommit = "none"
	// BuildTime is the RFC3339 build timestamp, injected via -ldflags during build.
	BuildTime = "unknown"
)

// handleVersionFlag checks if a version or help flag was provided in CLI arguments.
// Returns true if a flag was handled and execution should terminate.
func handleVersionFlag() bool {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version", "-version":
			fmt.Printf("antigravity-bot-engine version %s (commit: %s, built: %s)\n", Version, GitCommit, BuildTime)
			return true
		}
	}
	return false
}
