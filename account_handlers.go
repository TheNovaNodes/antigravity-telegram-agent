package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// maskEmail returns the full email address without obfuscation (#228).
func maskEmail(email string) string {
	return email
}

// resetChatSessionCache resets the conversation identifier in SQLite and terminates/evicts in-memory sessions.
// This prevents Google backend session ownership collisions when switching between accounts.
func resetChatSessionCache(db *sql.DB, botName string, userID int64, chatID int64) {
	if db != nil {
		if userID != 0 {
			if _, err := db.Exec("UPDATE users SET session_id = NULL WHERE user_id = ?", userID); err != nil {
				log.Printf("[AccountPool] Warning: failed to reset session_id for user %d in db: %v", userID, err)
			}
		} else {
			if _, err := db.Exec("UPDATE users SET session_id = NULL"); err != nil {
				log.Printf("[AccountPool] Warning: failed to reset session_id in db: %v", err)
			}
		}
	}

	sessionMu.Lock()
	defer sessionMu.Unlock()

	exactKey := fmt.Sprintf("%s:%d:%d", botName, chatID, userID)
	legacyKey := fmt.Sprintf("%s:%d", botName, chatID)
	prefix := fmt.Sprintf("%s:%d:", botName, chatID)

	for k, sess := range globalSessions {
		matches := false
		if k == exactKey || k == legacyKey {
			matches = true
		} else if strings.HasPrefix(k, prefix) {
			matches = true
		} else if sess != nil && sess.ChatID == chatID && (botName == "" || sess.BotName == botName) {
			if userID == 0 || sess.UserID == userID {
				matches = true
			}
		}

		if matches {
			if sess != nil {
				sess.mu.Lock()
				sess.Conversation = ""
				sess.UseContinue = false
				sess.AccountID = ""
				sess.AccountHomeDir = ""
				sess.mu.Unlock()
				sess.Kill()
			}
			delete(globalSessions, k)
		}
	}
	log.Printf("[AccountPool] Cleared session cache and terminated CLI process for bot %s, chat %d, user %d", botName, chatID, userID)
}

// formatAccountsDashboard builds the English dashboard message and inline keyboard.
func formatAccountsDashboard(pool *AccountPool, chatID int64) (string, tgbotapi.InlineKeyboardMarkup) {
	accounts := pool.ListAccounts()
	pinnedID, isPinned := pool.GetPinnedAccount(chatID)
	activeAcc := pool.GetActiveAccountForChat(chatID)

	var activeCount int
	for _, acc := range accounts {
		if acc.State == StateActive || acc.State == StateInUse {
			activeCount++
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 <b>Account Pool Manager</b> (Active: %d/%d)\n\n", activeCount, len(accounts)))

	if len(accounts) == 0 {
		sb.WriteString("<i>No accounts registered in pool yet.</i>\n")
		sb.WriteString("Use <b>[📥 Ingest Current Login]</b> or <code>/accounts ingest</code> to adopt your active CLI login.")
	}

	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	for _, acc := range accounts {
		isCurrent := (activeAcc != nil && activeAcc.ID == acc.ID) || (pinnedID == acc.ID)
		var statusBadge, statusText string

		switch acc.State {
		case StateActive:
			statusBadge = "🟢"
			statusText = "Active (Ready)"
		case StateInUse:
			statusBadge = "🟡"
			statusText = "In-Use (Processing turn)"
		case StateCooldown:
			statusBadge = "⏳"
			timeLeft := time.Until(acc.CooldownUntil)
			if timeLeft < 0 {
				timeLeft = 0
			}
			hours := int(timeLeft.Hours())
			mins := int(timeLeft.Minutes()) % 60
			statusText = fmt.Sprintf("Cooldown (%02dh %02dm remaining, resets %s UTC)",
				hours, mins, acc.CooldownUntil.UTC().Format("15:04"))
		case StateExpired:
			statusBadge = "🔴"
			statusText = "Expired (Re-authentication required)"
		}

		currentTag := ""
		if isCurrent {
			if isPinned {
				currentTag = " <b>[CURRENT • PINNED 🔒]</b>"
			} else {
				currentTag = " <b>[CURRENT]</b>"
			}
		}

		sb.WriteString(fmt.Sprintf("%s <b>[%s]</b> <code>%s</code>%s\n", statusBadge, acc.ID, acc.Email, currentTag))
		sb.WriteString(fmt.Sprintf("   ├─ Status: %s\n", statusText))

		if !acc.Quota.LastFetchedAt.IsZero() {
			g5h := fmt.Sprintf("%.0f%%", acc.Quota.Gemini5h.RemainingFraction*100)
			gWeekly := fmt.Sprintf("%.0f%%", acc.Quota.GeminiWeekly.RemainingFraction*100)
			c5h := fmt.Sprintf("%.0f%%", acc.Quota.Claude5h.RemainingFraction*100)
			cWeekly := fmt.Sprintf("%.0f%%", acc.Quota.ClaudeWeekly.RemainingFraction*100)

			reset5h := ""
			if !acc.Quota.Gemini5h.ResetTime.IsZero() && time.Now().Before(acc.Quota.Gemini5h.ResetTime) {
				reset5h = fmt.Sprintf(" (resets %s UTC)", acc.Quota.Gemini5h.ResetTime.UTC().Format("15:04"))
			}

			sb.WriteString(fmt.Sprintf("   ├─ Gemini: 5h %s%s • 7d %s\n", g5h, reset5h, gWeekly))
			sb.WriteString(fmt.Sprintf("   ├─ Claude/GPT: 5h %s • 7d %s\n", c5h, cWeekly))
		} else {
			sb.WriteString("   ├─ Quotas: <i>Not fetched (tap [🔄 Refresh Quotas])</i>\n")
		}

		sb.WriteString(fmt.Sprintf("   ├─ Active Turns: %d | Total Errors: %d\n", acc.ActiveTurns, acc.TotalErrors))
		if !acc.LastUsed.IsZero() {
			sb.WriteString(fmt.Sprintf("   └─ Last Used: %s\n\n", acc.LastUsed.Format("15:04:05 UTC")))
		} else {
			sb.WriteString("   └─ Last Used: Never\n\n")
		}

		// Row buttons for this account if not current
		var row []tgbotapi.InlineKeyboardButton
		if !isCurrent && acc.State != StateCooldown {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("👉 Switch to %s", acc.ID), fmt.Sprintf("acc:switch:%s", acc.ID)))
		}
		if acc.State == StateCooldown {
			row = append(row, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🔄 Reset Cooldown: %s", acc.ID), fmt.Sprintf("acc:cooldown:%s", acc.ID)))
		}
		if len(row) > 0 {
			keyboardRows = append(keyboardRows, row)
		}
	}

	// Pin / Unpin controls
	var pinRow []tgbotapi.InlineKeyboardButton
	if isPinned {
		pinRow = append(pinRow, tgbotapi.NewInlineKeyboardButtonData("🔓 Unpin (Enable Auto-Pool)", "acc:unpin"))
	} else if activeAcc != nil {
		pinRow = append(pinRow, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("🔒 Pin to %s (Sticky)", activeAcc.ID), fmt.Sprintf("acc:pin:%s", activeAcc.ID)))
	}
	if len(pinRow) > 0 {
		keyboardRows = append(keyboardRows, pinRow)
	}

	// Actions row
	actionRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh Quotas", "acc:refresh"),
		tgbotapi.NewInlineKeyboardButtonData("📥 Ingest Current Login", "acc:ingest"),
	}
	keyboardRows = append(keyboardRows, actionRow)

	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)
}

// handleAccountsCommand routes the /accounts slash command and its subcommands.
func handleAccountsCommand(bot *tgbotapi.BotAPI, chatID int64, userID int64, text string, botName string, db *sql.DB) {
	if GlobalAccountPool == nil {
		msg := tgbotapi.NewMessage(chatID, "❌ Account Pool Manager is not initialized on this instance.")
		bot.Send(msg)
		return
	}

	fields := strings.Fields(text)
	subcmd := ""
	if len(fields) > 1 {
		subcmd = strings.ToLower(fields[1])
	}

	switch subcmd {
	case "ingest":
		acc, err := GlobalAccountPool.IngestCurrentAccount()
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ <b>Failed to ingest account:</b> %v", err)))
			return
		}
		resp := fmt.Sprintf("✅ <b>Account Ingested Successfully!</b>\n\n"+
			"• <b>ID:</b> <code>%s</code>\n"+
			"• <b>Email:</b> <code>%s</code>\n"+
			"• <b>Status:</b> Active\n"+
			"• <b>Profile:</b> <code>%s</code>\n\n"+
			"Account is now registered in the multi-account rotation pool.",
			acc.ID, maskEmail(acc.Email), acc.HomeDir)
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "switch":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts switch &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.SwitchAccount(chatID, targetID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to switch account: %v", err)))
			return
		}
		resetChatSessionCache(db, botName, userID, chatID)
		acc, _ := GlobalAccountPool.GetAccount(targetID)
		emailStr := targetID
		if acc != nil {
			emailStr = maskEmail(acc.Email)
		}
		resp := fmt.Sprintf("✅ <b>Switched to Account:</b> <code>%s</code> (%s)\n\n"+
			"🧹 <i>Session cache cleared automatically. Ready for clean prompt.</i>", targetID, emailStr)
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "pin":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts pin &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.PinAccount(chatID, targetID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to pin account: %v", err)))
			return
		}
		resetChatSessionCache(db, botName, userID, chatID)
		resp := fmt.Sprintf("🔒 <b>Sticky Mode Enabled:</b> Pinned to <code>%s</code>.\n\n"+
			"<i>Automatic failover to other accounts is disabled for this chat.</i>", targetID)
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "unpin":
		if err := GlobalAccountPool.UnpinAccount(chatID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to unpin: %v", err)))
			return
		}
		resp := "🔓 <b>Sticky Mode Disabled:</b> Dynamic pool rotation re-enabled for this chat."
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "clear_cooldown":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts clear_cooldown &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.ClearCooldown(targetID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to clear cooldown: %v", err)))
			return
		}
		resp := fmt.Sprintf("🔄 <b>Cooldown Cleared:</b> Account <code>%s</code> is back in the active rotation pool.", targetID)
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "quotas", "refresh":
		GlobalAccountPool.FetchAllQuotas()
		dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID)
		msg := tgbotapi.NewMessage(chatID, dashboardText)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = keyboard
		bot.Send(msg)
		return

	case "help":
		helpText := "📖 <b>Account Pool Management Commands</b>\n\n" +
			"• <code>/accounts</code> — Show pool dashboard and quick actions\n" +
			"• <code>/accounts quotas</code> — Refresh and display real-time quotas\n" +
			"• <code>/accounts ingest</code> — Capture server's current CLI login into pool\n" +
			"• <code>/accounts switch &lt;id&gt;</code> — Switch active account and reset session cache\n" +
			"• <code>/accounts pin &lt;id&gt;</code> — Lock chat to an account (Sticky Mode)\n" +
			"• <code>/accounts unpin</code> — Unlock chat and restore dynamic rotation\n" +
			"• <code>/accounts clear_cooldown &lt;id&gt;</code> — Force account out of cooldown\n" +
			"• <code>/accounts help</code> — Display this guide"
		msg := tgbotapi.NewMessage(chatID, helpText)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	default:
		dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID)
		msg := tgbotapi.NewMessage(chatID, dashboardText)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = keyboard
		bot.Send(msg)
	}
}

// handleAccountCallbackQuery processes inline keyboard events originating from the accounts dashboard.
func handleAccountCallbackQuery(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, botName string, db *sql.DB) bool {
	if !strings.HasPrefix(cb.Data, "acc:") {
		return false
	}

	chatID := cb.Message.Chat.ID
	userID := cb.From.ID
	parts := strings.Split(cb.Data, ":")

	if GlobalAccountPool == nil {
		bot.Request(tgbotapi.NewCallback(cb.ID, "Account pool not initialized"))
		return true
	}

	action := parts[1]
	switch action {
	case "switch":
		if len(parts) >= 3 {
			targetID := parts[2]
			if err := GlobalAccountPool.SwitchAccount(chatID, targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			resetChatSessionCache(db, botName, userID, chatID)
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Switched to %s", targetID)))
		}

	case "pin":
		if len(parts) >= 3 {
			targetID := parts[2]
			if err := GlobalAccountPool.PinAccount(chatID, targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			resetChatSessionCache(db, botName, userID, chatID)
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Pinned to %s", targetID)))
		}

	case "unpin":
		if err := GlobalAccountPool.UnpinAccount(chatID); err != nil {
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
			return true
		}
		bot.Request(tgbotapi.NewCallback(cb.ID, "Unpinned. Auto-pool enabled."))

	case "cooldown":
		if len(parts) >= 3 {
			targetID := parts[2]
			if err := GlobalAccountPool.ClearCooldown(targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Cooldown cleared for %s", targetID)))
		}

	case "ingest":
		bot.Request(tgbotapi.NewCallback(cb.ID, "Ingesting current login..."))
		if acc, err := GlobalAccountPool.IngestCurrentAccount(); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Ingest failed: %v", err)))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Successfully ingested account %s (%s)!", acc.ID, acc.Email)))
		}

	case "refresh":
		bot.Request(tgbotapi.NewCallback(cb.ID, "Refreshing quotas from Google..."))
		GlobalAccountPool.FetchAllQuotas()
	}

	// Update dashboard message in place
	dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID)
	editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, dashboardText)
	editMsg.ParseMode = "HTML"
	editMsg.ReplyMarkup = &keyboard
	bot.Send(editMsg)
	return true
}
