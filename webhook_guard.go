package main

import (
	"errors"
	"fmt"
	"html"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// DefaultAllowedUpdates defines the update types explicitly requested during Long Polling (Layer 2).
// Explicitly specifying update types prevents Telegram from retaining residual filters
// from previous setWebhook calls that suppress callback_query (inline buttons) (#295).
var DefaultAllowedUpdates = []string{
	"message",
	"edited_message",
	"callback_query",
	"channel_post",
	"edited_channel_post",
}

var (
	alertCooldownMu sync.Mutex
	lastAlertTimes  = make(map[string]time.Time)
	alertCooldown   = 1 * time.Minute
)

// clearWebhookOnStartup unconditionally deletes any stale or pirate webhook before starting polling (Layer 1).
func clearWebhookOnStartup(bot *tgbotapi.BotAPI) error {
	if bot == nil {
		return errors.New("nil bot")
	}
	delCfg := tgbotapi.DeleteWebhookConfig{DropPendingUpdates: false}
	_, err := bot.Request(delCfg)
	if err != nil {
		log.Printf("[Bot %s] ⚠️ Warning: failed to clear webhook on startup: %v", bot.Self.UserName, err)
		return err
	}
	log.Printf("[Bot %s] 🛡️ Layer 1 startup deleteWebhook guard executed successfully", bot.Self.UserName)
	return nil
}

// isWebhookConflictError inspects an error to determine if it signifies a 409 Conflict due to an active webhook.
func isWebhookConflictError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	if strings.Contains(errStr, "webhook") && (strings.Contains(errStr, "conflict") || strings.Contains(errStr, "409")) {
		return true
	}

	var apiErr tgbotapi.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code == 409 && strings.Contains(strings.ToLower(apiErr.Message), "webhook") {
			return true
		}
	}
	return false
}

// recoverFromWebhookConflict performs defensive auto-recovery when a 409 Conflict is detected (Layer 3 & 4).
func recoverFromWebhookConflict(bot *tgbotapi.BotAPI, allowedAdmins map[int64]bool) (string, error) {
	if bot == nil {
		return "", errors.New("nil bot")
	}
	botName := bot.Self.UserName
	parasiteURL := "unknown"

	// 1. Inspect parasite webhook metadata for diagnostics & alerting
	info, infoErr := bot.GetWebhookInfo()
	if infoErr == nil && info.URL != "" {
		parasiteURL = info.URL
	}

	log.Printf("[Bot %s] ⚠️ Webhook conflict detected (source: %s)! Invoking deleteWebhook auto-recovery...", botName, parasiteURL)

	// 2. Unconditionally delete the conflicting webhook
	delCfg := tgbotapi.DeleteWebhookConfig{DropPendingUpdates: false}
	_, delErr := bot.Request(delCfg)
	if delErr != nil {
		log.Printf("[Bot %s] ❌ Auto-recovery deleteWebhook call failed: %v", botName, delErr)
		return parasiteURL, delErr
	}

	log.Printf("[Bot %s] 🛡️ Webhook successfully removed! Long Polling self-healed.", botName)

	// 3. Dispatch Layer 4 Admin Alert with cooldown protection
	alertCooldownMu.Lock()
	lastTime := lastAlertTimes[botName]
	shouldAlert := time.Since(lastTime) > alertCooldown
	if shouldAlert {
		lastAlertTimes[botName] = time.Now()
	}
	alertCooldownMu.Unlock()

	if shouldAlert {
		sendWebhookConflictAlert(bot, botName, parasiteURL, allowedAdmins)
	}

	return parasiteURL, nil
}

// sendWebhookConflictAlert delivers a high-priority alert to authorized administrators.
func sendWebhookConflictAlert(bot *tgbotapi.BotAPI, botName, parasiteURL string, allowedAdmins map[int64]bool) {
	if bot == nil || len(allowedAdmins) == 0 {
		return
	}
	nowStr := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
	text := fmt.Sprintf("⚠️ <b>[Bot %s] External Webhook Hijack Detected & Auto-Cleared!</b>\n\n"+
		"<b>Source:</b> <code>%s</code>\n"+
		"<b>Action:</b> <code>deleteWebhook</code> executed, Long Polling resumed.\n"+
		"<b>Time:</b> <code>%s</code>",
		html.EscapeString(botName),
		html.EscapeString(parasiteURL),
		html.EscapeString(nowStr),
	)

	for adminID := range allowedAdmins {
		msg := tgbotapi.NewMessage(adminID, text)
		msg.ParseMode = "HTML"
		if _, err := bot.Send(msg); err != nil {
			log.Printf("[Bot %s] Failed to send webhook conflict alert to admin %d: %v", botName, adminID, err)
		}
	}
}

// getUpdatesWithRecovery creates an UpdatesChannel that wraps getUpdates with defensive
// 409 Conflict auto-recovery, automatic deleteWebhook self-healing, and admin alerts (#295).
func getUpdatesWithRecovery(bot *tgbotapi.BotAPI, config tgbotapi.UpdateConfig, allowedAdmins map[int64]bool, stopChan <-chan struct{}) tgbotapi.UpdatesChannel {
	ch := make(chan tgbotapi.Update, bot.Buffer)

	go func() {
		defer close(ch)
		for {
			select {
			case <-stopChan:
				return
			default:
			}

			updates, err := bot.GetUpdates(config)
			if err != nil {
				if isWebhookConflictError(err) {
					_, recErr := recoverFromWebhookConflict(bot, allowedAdmins)
					if recErr != nil {
						log.Printf("[Bot %s] Auto-recovery failed: %v, retrying in 2 seconds...", bot.Self.UserName, recErr)
						select {
						case <-stopChan:
							return
						case <-time.After(2 * time.Second):
						}
					} else {
						// Brief backoff after successful recovery before resuming polling
						select {
						case <-stopChan:
							return
						case <-time.After(500 * time.Millisecond):
						}
					}
					continue
				}

				log.Printf("[Bot %s] Failed to get updates: %v, retrying in 3 seconds...", bot.Self.UserName, err)
				select {
				case <-stopChan:
					return
				case <-time.After(3 * time.Second):
				}
				continue
			}

			for _, update := range updates {
				if update.UpdateID >= config.Offset {
					config.Offset = update.UpdateID + 1
					select {
					case <-stopChan:
						return
					case ch <- update:
					}
				}
			}
		}
	}()

	return ch
}
