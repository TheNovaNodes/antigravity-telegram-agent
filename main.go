package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

var (
	activeBotsMu sync.Mutex
	activeBots   []*tgbotapi.BotAPI
)

// startBotPolling initializes a Telegram Bot instance and starts its dedicated long-polling loop.
func startBotPolling(botToken string, allowedAdmins map[int64]bool, wg *sync.WaitGroup) {
	defer wg.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[PANIC RECOVERED in startBotPolling] %v", r)
		}
	}()
	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Printf("Failed to init bot: %v", err)
		return
	}
	bot.Client = &http.Client{Timeout: 65 * time.Second}

	activeBotsMu.Lock()
	activeBots = append(activeBots, bot)
	activeBotsMu.Unlock()

	registerBotCommands(bot)
	db := initDB(bot.Self.UserName)
	defer db.Close()
	log.Printf("[Bot %s] Started in PURE GO mode", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		var userID int64
		if update.Message != nil && update.Message.From != nil {
			userID = update.Message.From.ID
		} else if update.CallbackQuery != nil && update.CallbackQuery.From != nil {
			userID = update.CallbackQuery.From.ID
		}

		if !allowedAdmins[userID] {
			log.Printf("[Bot %s] 🛑 ACL BLOCK: Unauthorized access attempt from %d", bot.Self.UserName, userID)
			continue
		}

		dispatchUpdate(bot, update, db)
	}
}

// registerBotCommands sets the default slash commands menu for the Telegram bot interface.
func registerBotCommands(bot *tgbotapi.BotAPI) {
	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Welcome menu & status"},
		{Command: "model", Description: "Select LLM model"},
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
		{Command: "goal", Description: "Run exhaustive long-running task"},
		{Command: "schedule", Description: "Set recurring schedule or timer"},
		{Command: "browser", Description: "Use web browser for a task"},
		{Command: "plan", Description: "Step-by-step task planning"},
		{Command: "grill_me", Description: "Interactive design interview"},
		{Command: "teamwork_preview", Description: "Swarm autonomous agents"},
		{Command: "learn", Description: "Persist behavior for future tasks"},
	}
	cfg := tgbotapi.NewSetMyCommands(commands...)
	if _, err := bot.Request(cfg); err != nil {
		log.Printf("Failed to set bot commands: %v", err)
	} else {
		log.Printf("Successfully registered slash commands for %s", bot.Self.UserName)
	}
}

// main is the entry point that spins up multiple bot instances concurrently based on the BOT_TOKENS environment variable.
func main() {
	loadEnvFile()
	fetchModels()
	tokensEnv := os.Getenv("BOT_TOKENS")
	if tokensEnv == "" {
		log.Fatal("BOT_TOKENS env var required")
	}

	allowedAdmins := loadAllowedAdmins()
	if len(allowedAdmins) == 0 {
		log.Fatal("FATAL: ALLOWED_ADMIN_IDS is required and must contain at least one valid Telegram User ID. Refusing to start in open-access mode.")
	}

	// Initialize Multi-Account Rotation Pool (#216)
	var poolErr error
	var reaperCancel context.CancelFunc
	GlobalAccountPool, poolErr = NewAccountPool("")
	if poolErr != nil {
		log.Printf("[AccountPool] Warning: failed to initialize account pool: %v", poolErr)
	} else {
		log.Printf("[AccountPool] Initialized successfully with %d accounts", len(GlobalAccountPool.ListAccounts()))
		var reaperCtx context.Context
		reaperCtx, reaperCancel = context.WithCancel(context.Background())
		go GlobalAccountPool.StartBackgroundReaper(reaperCtx, func(acc *Account) {
			broadcastNotice := fmt.Sprintf("🔔 <b>[Account Cooldown Ended]</b> Account <code>%s</code> (<code>%s</code>) has completed cooldown and returned to the active pool.", acc.ID, acc.Email)
			activeBotsMu.Lock()
			bots := make([]*tgbotapi.BotAPI, len(activeBots))
			copy(bots, activeBots)
			activeBotsMu.Unlock()
			if len(bots) > 0 {
				for adminID := range allowedAdmins {
					msg := tgbotapi.NewMessage(adminID, broadcastNotice)
					msg.ParseMode = "HTML"
					bots[0].Send(msg)
				}
			}
		})
	}
	var wg sync.WaitGroup

	for _, t := range strings.Split(tokensEnv, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			wg.Add(1)
			go startBotPolling(t, allowedAdmins, &wg)
		}
	}

	// Start background housekeeping workers (session GC, disk cleanup, and subprocess watchdog)
	stopHousekeeping := make(chan struct{})
	maxIdleSessionDuration := 4 * time.Hour
	if idleEnv := os.Getenv("SESSION_MAX_IDLE"); idleEnv != "" {
		if d, err := time.ParseDuration(idleEnv); err == nil && d > 0 {
			maxIdleSessionDuration = d
		}
	}
	StartSessionGCWorker(30*time.Minute, maxIdleSessionDuration, stopHousekeeping)
	StartDiskCleanupWorker(2*time.Hour, 24*time.Hour, stopHousekeeping)
	StartSubprocessWatchdogWorker(2*time.Second, 3*time.Second, stopHousekeeping)

	// Optional Prometheus metrics server (#248)
	metricsServer, _ := StartMetricsServer("")

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs

	log.Println("Shutting down gracefully...")
	if metricsServer != nil {
		_ = StopMetricsServer(metricsServer)
	}
	if reaperCancel != nil {
		reaperCancel()
	}
	close(stopHousekeeping)

	// 1. Stop Telegram polling on all active bots
	activeBotsMu.Lock()
	for _, b := range activeBots {
		b.StopReceivingUpdates()
	}
	activeBotsMu.Unlock()

	// 2. Kill and clean up all active agent processes
	sessionMu.Lock()
	sessionsToKill := make([]*AgySession, 0, len(globalSessions))
	for _, s := range globalSessions {
		sessionsToKill = append(sessionsToKill, s)
	}
	sessionMu.Unlock()

	for _, s := range sessionsToKill {
		s.Kill()
	}

	// 3. Await clean exit of all bot polling loops
	doneChan := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneChan)
	}()

	select {
	case <-doneChan:
		log.Println("All bot polling loops terminated cleanly.")
	case <-time.After(3 * time.Second):
		log.Println("Shutdown timed out waiting for polling loops, proceeding with exit.")
	}

	log.Println("Goodbye.")
}
