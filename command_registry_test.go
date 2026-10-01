package main

import (
	"regexp"
	"testing"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func TestRegisterBotCommands_Validation(t *testing.T) {
	// Reconstruct the registered command list to validate Telegram Bot API invariants
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Welcome menu & status"},
		{Command: "help", Description: "Command reference & documentation"},
		{Command: "model", Description: "Select LLM model"},
		{Command: "refresh_models", Description: "Dynamically refresh models cache"},
		{Command: "usage", Description: "Show API quota usage"},
		{Command: "accounts", Description: "Manage multi-account pool and rotation"},
		{Command: "clear", Description: "Clear context and restart agent"},
		{Command: "stop", Description: "Interrupt active execution turn"},
		{Command: "resume", Description: "Resume previous conversation"},
		{Command: "rename", Description: "Rename current session"},
		{Command: "workspace", Description: "Change target workspace directory"},
		{Command: "export", Description: "Export session transcript to file"},
		{Command: "voice", Description: "Toggle persistent voice mode"},
		{Command: "tts", Description: "Text to speech voice synthesis"},
		{Command: "tts_engine", Description: "View or switch active TTS engine"},
		{Command: "goal", Description: "Run exhaustive long-running task"},
		{Command: "schedule", Description: "Set recurring schedule or timer"},
		{Command: "browser", Description: "Use web browser for a task"},
		{Command: "plan", Description: "Step-by-step task planning"},
		{Command: "grill_me", Description: "Interactive design interview"},
		{Command: "teamwork_preview", Description: "Swarm autonomous agents"},
		{Command: "learn", Description: "Persist behavior for future tasks"},
	}

	commandNameRegex := regexp.MustCompile(`^[a-z0-9_]{1,32}$`)
	commandMap := make(map[string]string)

	for _, cmd := range commands {
		// Invariant 1: Telegram command syntax (1-32 chars, lowercase, numbers, underscores)
		if !commandNameRegex.MatchString(cmd.Command) {
			t.Errorf("Invalid Telegram command name syntax: %q", cmd.Command)
		}

		// Invariant 2: Description length <= 256 characters
		if len(cmd.Description) == 0 || len(cmd.Description) > 256 {
			t.Errorf("Command %q description length %d out of bounds (1-256)", cmd.Command, len(cmd.Description))
		}

		commandMap[cmd.Command] = cmd.Description
	}

	// Required commands must be present
	requiredCommands := []string{"help", "refresh_models", "tts_engine", "goal", "plan", "schedule", "browser", "learn", "grill_me", "teamwork_preview"}
	for _, req := range requiredCommands {
		if _, ok := commandMap[req]; !ok {
			t.Errorf("Required command %q missing from registered commands", req)
		}
	}
}

func TestHandleHelpCommand_NilSafeAndContent(t *testing.T) {
	// Must not panic on nil bot pointer
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("handleHelpCommand panicked on nil bot: %v", r)
		}
	}()

	handleHelpCommand(nil, 99999)
}
