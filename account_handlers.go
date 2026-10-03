package main

import (
	"database/sql"
	"fmt"
	"html"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// resetChatSessionCache resets the conversation identifier in SQLite and terminates/evicts in-memory sessions.
// If preserveDBSession is true, the in-memory session is evicted but the SQLite user session_id is preserved (#236).
func resetChatSessionCache(db *sql.DB, botName string, userID int64, chatID int64, preserveDBSession ...bool) {
	shouldWipeDB := true
	if len(preserveDBSession) > 0 && preserveDBSession[0] {
		shouldWipeDB = false
	}

	if shouldWipeDB && db != nil {
		if userID != 0 {
			if _, err := db.Exec("UPDATE users SET session_id = NULL WHERE user_id = ?", userID); err != nil {
				log.Printf("[AccountPool] Warning: failed to reset session_id for user %d in db: %v", userID, err)
			}
		} else {
			log.Printf("[AccountPool] Notice: userID is 0, skipping database wipe to protect all users; wiping in-memory session cache only for chat %d", chatID)
		}
	}

	sessionMu.Lock()
	exactKey := fmt.Sprintf("%s:%d:%d", botName, chatID, userID)
	legacyKey := fmt.Sprintf("%s:%d", botName, chatID)
	prefix := fmt.Sprintf("%s:%d:", botName, chatID)

	var sessionsToKill []*AgySession

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
				sessionsToKill = append(sessionsToKill, sess)
			}
			delete(globalSessions, k)
		}
	}
	sessionMu.Unlock()

	for _, sess := range sessionsToKill {
		sess.Kill()
	}
	log.Printf("[AccountPool] Cleared session cache and terminated CLI process for bot %s, chat %d, user %d", botName, chatID, userID)
}

// ResetAccountSessions terminates all running CLI processes and evicts in-memory sessions
// associated with the given accountID.
func ResetAccountSessions(accountID string) {
	if accountID == "" {
		return
	}
	sessionMu.Lock()
	var sessionsToKill []*AgySession
	for k, sess := range globalSessions {
		if sess != nil {
			sess.mu.Lock()
			accID := sess.AccountID
			sess.mu.Unlock()
			if accID == accountID {
				sess.mu.Lock()
				sess.Conversation = ""
				sess.UseContinue = false
				sess.AccountID = ""
				sess.AccountHomeDir = ""
				sess.mu.Unlock()
				sessionsToKill = append(sessionsToKill, sess)
				delete(globalSessions, k)
			}
		}
	}
	sessionMu.Unlock()

	for _, sess := range sessionsToKill {
		sess.Kill()
	}
	if len(sessionsToKill) > 0 {
		log.Printf("[AccountPool] Terminated and evicted %d active session(s) for account %s", len(sessionsToKill), accountID)
	}
}

// formatAccountsDashboard builds the English dashboard message and inline keyboard.
func formatAccountsDashboard(pool *AccountPool, chatID int64, botNames ...string) (string, tgbotapi.InlineKeyboardMarkup) {
	accounts := pool.ListAccounts()
	pinnedID, isPinned := pool.GetPinnedAccount(chatID, botNames...)
	activeAcc := pool.GetActiveAccountForChat(chatID, botNames...)

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

	var accountButtons []tgbotapi.InlineKeyboardButton

	for _, acc := range accounts {
		isCurrent := (activeAcc != nil && activeAcc.ID == acc.ID) || (pinnedID == acc.ID)
		var statusBadge, statusText string

		switch acc.State {
		case StateActive:
			statusBadge = "🟢"
			statusText = "Active (Ready)"
		case StateInUse:
			statusBadge = "⚡"
			statusText = "In-Use (Processing turn)"
		case StateCooldown:
			statusBadge = "⏳"
			timeLeft := time.Until(acc.CooldownUntil)
			if timeLeft < 0 {
				timeLeft = 0
			}
			hours := int(timeLeft.Hours())
			mins := int(timeLeft.Minutes()) % 60
			secs := int(timeLeft.Seconds()) % 60
			if hours > 0 {
				statusText = fmt.Sprintf("Cooldown (%02dh %02dm remaining, resets %s UTC)",
					hours, mins, acc.CooldownUntil.UTC().Format("15:04"))
			} else if mins > 0 {
				statusText = fmt.Sprintf("Cooldown (Backoff: %02dm %02ds remaining, resets %s UTC)",
					mins, secs, acc.CooldownUntil.UTC().Format("15:04"))
			} else {
				statusText = fmt.Sprintf("Cooldown (Backoff: %02ds remaining)", secs)
			}
		case StateExpired:
			statusBadge = "🔴"
			statusText = "Expired (Re-authentication required)"
		case StateFrozen:
			statusBadge = "🧊"
			statusText = "Frozen (Administrative Hold)"
		default:
			statusBadge = "⚪"
			statusText = acc.State.String()
		}

		currentTag := ""
		indicator := ""
		if isCurrent {
			if isPinned {
				currentTag = " <b>[CURRENT • PINNED 🔒]</b>"
				indicator = " 🔒"
			} else {
				currentTag = " <b>[CURRENT]</b>"
				indicator = " •"
			}
		}

		sb.WriteString(fmt.Sprintf("%s <b>[%s]</b> <code>%s</code>%s\n", statusBadge, html.EscapeString(acc.ID), html.EscapeString(acc.Email), currentTag))
		sb.WriteString(fmt.Sprintf("   ├─ Status: %s\n", statusText))

		if !acc.Quota.LastFetchedAt.IsZero() {
			g5h := fmt.Sprintf("%.0f%%", acc.Quota.Gemini5h.RemainingFraction*100)
			if acc.Quota.Gemini5h.Disabled {
				g5h = "disabled"
			}
			gWeekly := fmt.Sprintf("%.0f%%", acc.Quota.GeminiWeekly.RemainingFraction*100)
			if acc.Quota.GeminiWeekly.Disabled {
				gWeekly = "disabled"
			}
			c5h := fmt.Sprintf("%.0f%%", acc.Quota.Claude5h.RemainingFraction*100)
			if acc.Quota.Claude5h.Disabled {
				c5h = "disabled"
			}
			cWeekly := fmt.Sprintf("%.0f%%", acc.Quota.ClaudeWeekly.RemainingFraction*100)
			if acc.Quota.ClaudeWeekly.Disabled {
				cWeekly = "disabled"
			}

			reset5h := ""
			if !acc.Quota.Gemini5h.Disabled && !acc.Quota.Gemini5h.ResetTime.IsZero() && time.Now().Before(acc.Quota.Gemini5h.ResetTime) {
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

		// Master View button for this account: 2 per row
		btnLabel := fmt.Sprintf("%s %s%s", statusBadge, acc.ID, indicator)
		accountButtons = append(accountButtons, tgbotapi.NewInlineKeyboardButtonData(btnLabel, fmt.Sprintf("acc:manage:%s", acc.ID)))
	}

	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	// Layout account buttons 2 per row
	var currentRow []tgbotapi.InlineKeyboardButton
	for _, btn := range accountButtons {
		currentRow = append(currentRow, btn)
		if len(currentRow) == 2 {
			keyboardRows = append(keyboardRows, currentRow)
			currentRow = nil
		}
	}
	if len(currentRow) > 0 {
		keyboardRows = append(keyboardRows, currentRow)
	}

	// Pin / Unpin controls for master view: only unpin if currently pinned
	if isPinned {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("🔓 Unpin (Enable Auto-Pool)", "acc:unpin"),
		})
	}

	// Actions row
	actionRow := []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh Quotas", "acc:refresh"),
		tgbotapi.NewInlineKeyboardButtonData("📥 Ingest Current Login", "acc:ingest"),
	}
	keyboardRows = append(keyboardRows, actionRow)

	return sb.String(), tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)
}

// formatAccountCard builds the detailed single-account card and full-width management keyboard.
func formatAccountCard(pool *AccountPool, acc *Account, chatID int64, botNames ...string) (string, tgbotapi.InlineKeyboardMarkup) {
	pinnedID, isPinned := pool.GetPinnedAccount(chatID, botNames...)
	activeAcc := pool.GetActiveAccountForChat(chatID, botNames...)

	isCurrent := (activeAcc != nil && activeAcc.ID == acc.ID) || (pinnedID == acc.ID)
	isThisPinned := isPinned && pinnedID == acc.ID

	var statusBadge, statusText string
	switch acc.State {
	case StateActive:
		statusBadge = "🟢"
		statusText = "Active (Ready)"
	case StateInUse:
		statusBadge = "⚡"
		statusText = "In-Use (Processing turn)"
	case StateCooldown:
		statusBadge = "⏳"
		timeLeft := time.Until(acc.CooldownUntil)
		if timeLeft < 0 {
			timeLeft = 0
		}
		hours := int(timeLeft.Hours())
		mins := int(timeLeft.Minutes()) % 60
		secs := int(timeLeft.Seconds()) % 60
		if hours > 0 {
			statusText = fmt.Sprintf("Cooldown (%02dh %02dm remaining, resets %s UTC)",
				hours, mins, acc.CooldownUntil.UTC().Format("15:04"))
		} else if mins > 0 {
			statusText = fmt.Sprintf("Cooldown (Backoff: %02dm %02ds remaining, resets %s UTC)",
				mins, secs, acc.CooldownUntil.UTC().Format("15:04"))
		} else {
			statusText = fmt.Sprintf("Cooldown (Backoff: %02ds remaining)", secs)
		}
	case StateExpired:
		statusBadge = "🔴"
		statusText = "Expired (Re-authentication required)"
	case StateFrozen:
		statusBadge = "🧊"
		statusText = "Frozen (Administrative Hold)"
	default:
		statusBadge = "⚪"
		statusText = acc.State.String()
	}

	currentTag := ""
	if isCurrent {
		if isThisPinned {
			currentTag = " <b>[CURRENT • PINNED 🔒]</b>"
		} else {
			currentTag = " <b>[CURRENT]</b>"
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚙️ <b>Manage Account:</b> <code>%s</code>%s\n", html.EscapeString(acc.ID), currentTag))
	sb.WriteString(fmt.Sprintf("📧 <b>Email:</b> <code>%s</code>\n", html.EscapeString(acc.Email)))
	if acc.HomeDir != "" {
		sb.WriteString(fmt.Sprintf("📁 <b>Profile:</b> <code>%s</code>\n", html.EscapeString(acc.HomeDir)))
	}
	sb.WriteString(fmt.Sprintf("🚥 <b>Status:</b> %s %s\n", statusBadge, statusText))

	if !acc.Quota.LastFetchedAt.IsZero() {
		g5h := fmt.Sprintf("%.0f%%", acc.Quota.Gemini5h.RemainingFraction*100)
		if acc.Quota.Gemini5h.Disabled {
			g5h = "disabled"
		}
		gWeekly := fmt.Sprintf("%.0f%%", acc.Quota.GeminiWeekly.RemainingFraction*100)
		if acc.Quota.GeminiWeekly.Disabled {
			gWeekly = "disabled"
		}
		c5h := fmt.Sprintf("%.0f%%", acc.Quota.Claude5h.RemainingFraction*100)
		if acc.Quota.Claude5h.Disabled {
			c5h = "disabled"
		}
		cWeekly := fmt.Sprintf("%.0f%%", acc.Quota.ClaudeWeekly.RemainingFraction*100)
		if acc.Quota.ClaudeWeekly.Disabled {
			cWeekly = "disabled"
		}

		reset5h := ""
		if !acc.Quota.Gemini5h.Disabled && !acc.Quota.Gemini5h.ResetTime.IsZero() && time.Now().Before(acc.Quota.Gemini5h.ResetTime) {
			reset5h = fmt.Sprintf(" (resets %s UTC)", acc.Quota.Gemini5h.ResetTime.UTC().Format("15:04"))
		}

		sb.WriteString("📊 <b>Quotas:</b>\n")
		sb.WriteString(fmt.Sprintf("   ├─ Gemini: 5h %s%s • 7d %s\n", g5h, reset5h, gWeekly))
		sb.WriteString(fmt.Sprintf("   └─ Claude/GPT: 5h %s • 7d %s\n", c5h, cWeekly))
	} else {
		sb.WriteString("📊 <b>Quotas:</b> <i>Not fetched (use [🔄 Refresh Quotas])</i>\n")
	}

	lastUsedStr := "Never"
	if !acc.LastUsed.IsZero() {
		lastUsedStr = acc.LastUsed.Format("15:04:05 UTC")
	}
	sb.WriteString(fmt.Sprintf("⏱️ <b>Statistics:</b> Active Turns: %d | Total Errors: %d | Last Used: %s\n\n",
		acc.ActiveTurns, acc.TotalErrors, lastUsedStr))
	sb.WriteString("Choose an action for this profile:")

	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	// 1. Switch
	if !isCurrent && acc.State != StateFrozen && acc.State != StateCooldown {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("👉 Switch to this Account", fmt.Sprintf("acc:switch:%s", acc.ID)),
		})
	}

	// 2. Cooldown reset
	if acc.State == StateCooldown {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("🔄 Reset Cooldown", fmt.Sprintf("acc:cooldown:%s", acc.ID)),
		})
	}

	// 3. Pin / Unpin
	if isThisPinned {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("🔓 Unpin from this Chat", fmt.Sprintf("acc:unpin:%s", acc.ID)),
		})
	} else if acc.State != StateFrozen {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("🔒 Pin to this Chat (Sticky Mode)", fmt.Sprintf("acc:pin:%s", acc.ID)),
		})
	}

	// 4. Freeze / Unfreeze
	if acc.State == StateFrozen {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("🧊 Unfreeze Account", fmt.Sprintf("acc:unfreeze:%s", acc.ID)),
		})
	} else {
		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData("❄️ Freeze Account", fmt.Sprintf("acc:freeze:%s", acc.ID)),
		})
	}

	// 5. Delete
	keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("🗑️ Delete Account from Pool", fmt.Sprintf("acc:del_confirm:%s", acc.ID)),
	})

	// 6. Back button
	keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("🔙 « Back to Account List", "acc:back"),
	})

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
			html.EscapeString(acc.ID), html.EscapeString(acc.Email), html.EscapeString(acc.HomeDir))
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "switch":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts switch &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.SwitchAccount(chatID, targetID, botName); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to switch account: %v", err)))
			return
		}
		user := getUser(db, userID, botName)
		if isValidSessionID(user.SessionID) {
			handleExportCommand(bot, chatID, userID, botName, user)
		}
		resetChatSessionCache(db, botName, userID, chatID, true)
		acc, _ := GlobalAccountPool.GetAccount(targetID)
		emailStr := targetID
		if acc != nil {
			emailStr = acc.Email
		}
		resp := fmt.Sprintf("✅ <b>Switched to Account:</b> <code>%s</code> (%s)\n\n"+
			"🧹 <i>In-memory session evicted and safely exported. Session context preserved for next prompt.</i>", html.EscapeString(targetID), html.EscapeString(emailStr))
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "pin":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts pin &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.PinAccount(chatID, targetID, botName); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to pin account: %v", err)))
			return
		}
		user := getUser(db, userID, botName)
		if isValidSessionID(user.SessionID) {
			handleExportCommand(bot, chatID, userID, botName, user)
		}
		resetChatSessionCache(db, botName, userID, chatID, true)
		resp := fmt.Sprintf("🔒 <b>Sticky Mode Enabled:</b> Pinned to <code>%s</code>.\n\n"+
			"<i>Automatic failover to other accounts is disabled for this chat. Session context preserved.</i>", html.EscapeString(targetID))
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "unpin":
		if err := GlobalAccountPool.UnpinAccount(chatID, botName); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to unpin: %v", err)))
			return
		}
		resp := "🔓 <b>Sticky Mode Disabled:</b> Dynamic pool rotation re-enabled for this chat."
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "freeze":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts freeze &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.FreezeAccount(targetID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to freeze account: %v", err)))
			return
		}
		resp := fmt.Sprintf("🧊 <b>Account Frozen:</b> <code>%s</code> has been administratively suspended and removed from rotation.", html.EscapeString(targetID))
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "unfreeze":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts unfreeze &lt;account-id&gt;</code>"))
			return
		}
		targetID := fields[2]
		if err := GlobalAccountPool.UnfreezeAccount(targetID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to unfreeze account: %v", err)))
			return
		}
		acc, _ := GlobalAccountPool.GetAccount(targetID)
		st := "Active"
		if acc != nil {
			st = acc.State.String()
		}
		resp := fmt.Sprintf("✅ <b>Account Unfrozen:</b> <code>%s</code> restored to <b>%s</b>.", html.EscapeString(targetID), html.EscapeString(st))
		msg := tgbotapi.NewMessage(chatID, resp)
		msg.ParseMode = "HTML"
		bot.Send(msg)

	case "delete":
		if len(fields) < 3 {
			bot.Send(tgbotapi.NewMessage(chatID, "⚠️ Usage: <code>/accounts delete &lt;account-id&gt; [purge]</code>"))
			return
		}
		targetID := fields[2]
		purge := len(fields) >= 4 && strings.ToLower(fields[3]) == "purge"
		if err := GlobalAccountPool.DeleteAccount(targetID, purge); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Failed to delete account: %v", err)))
			return
		}
		purgeNote := "profile storage preserved on disk"
		if purge {
			purgeNote = "profile storage permanently purged from disk"
		}
		resp := fmt.Sprintf("🗑️ <b>Account Deleted:</b> <code>%s</code> removed from pool (%s).", html.EscapeString(targetID), purgeNote)
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
		resp := fmt.Sprintf("🔄 <b>Cooldown Cleared:</b> Account <code>%s</code> is back in the active rotation pool.", html.EscapeString(targetID))
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
			"• <code>/accounts freeze &lt;id&gt;</code> — Administratively suspend account\n" +
			"• <code>/accounts unfreeze &lt;id&gt;</code> — Restore frozen account to rotation\n" +
			"• <code>/accounts delete &lt;id&gt; [purge]</code> — Delete account from pool (optional storage purge)\n" +
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
	targetID := ""
	if len(parts) >= 3 {
		targetID = parts[2]
	}

	var stayOnCard bool

	switch action {
	case "manage":
		if targetID == "" {
			bot.Request(tgbotapi.NewCallback(cb.ID, "Invalid account ID"))
			return true
		}
		acc, err := GlobalAccountPool.GetAccount(targetID)
		if err != nil || acc == nil {
			bot.Request(tgbotapi.NewCallback(cb.ID, "❌ Account not found or deleted"))
			dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID, botName)
			editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, dashboardText)
			editMsg.ParseMode = "HTML"
			editMsg.ReplyMarkup = &keyboard
			if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
				log.Printf("[AccountPool] Failed to refresh dashboard on missing account: %v", err)
			}
			return true
		}

		bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		cardText, cardKeyboard := formatAccountCard(GlobalAccountPool, acc, chatID, botName)
		editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, cardText)
		editMsg.ParseMode = "HTML"
		editMsg.ReplyMarkup = &cardKeyboard
		if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("[AccountPool] Failed to render account card: %v", err)
		}
		return true

	case "back":
		bot.Request(tgbotapi.NewCallback(cb.ID, ""))
		dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID, botName)
		editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, dashboardText)
		editMsg.ParseMode = "HTML"
		editMsg.ReplyMarkup = &keyboard
		if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("[AccountPool] Failed to return to dashboard: %v", err)
		}
		return true

	case "switch":
		if targetID != "" {
			if err := GlobalAccountPool.SwitchAccount(chatID, targetID, botName); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			user := getUser(db, userID, botName)
			if isValidSessionID(user.SessionID) {
				handleExportCommand(bot, chatID, userID, botName, user)
			}
			resetChatSessionCache(db, botName, userID, chatID, true)
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Switched to %s", targetID)))
			stayOnCard = true
		}

	case "pin":
		if targetID != "" {
			if err := GlobalAccountPool.PinAccount(chatID, targetID, botName); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			user := getUser(db, userID, botName)
			if isValidSessionID(user.SessionID) {
				handleExportCommand(bot, chatID, userID, botName, user)
			}
			resetChatSessionCache(db, botName, userID, chatID, true)
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Pinned to %s", targetID)))
			stayOnCard = true
		}

	case "unpin":
		if err := GlobalAccountPool.UnpinAccount(chatID, botName); err != nil {
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
			return true
		}
		bot.Request(tgbotapi.NewCallback(cb.ID, "Unpinned. Auto-pool enabled."))
		if targetID != "" {
			stayOnCard = true
		}

	case "freeze":
		if targetID != "" {
			if err := GlobalAccountPool.FreezeAccount(targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Account %s frozen", targetID)))
			stayOnCard = true
		}

	case "unfreeze":
		if targetID != "" {
			if err := GlobalAccountPool.UnfreezeAccount(targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Account %s unfrozen", targetID)))
			stayOnCard = true
		}

	case "del_confirm":
		if targetID != "" {
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Confirm deletion of %s", targetID)))
			confirmText := fmt.Sprintf("⚠️ <b>Confirm Deletion of Account:</b> <code>%s</code>\n\n"+
				"Are you sure you want to remove this account from the pool?\n"+
				"• Pinned bindings will be removed.\n"+
				"• Any active sessions bound to this account will be terminated.\n\n"+
				"Choose deletion mode below:", html.EscapeString(targetID))
			confirmKeyboard := tgbotapi.NewInlineKeyboardMarkup(
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("❌ Confirm Delete (Keep Storage)", fmt.Sprintf("acc:del_exec:%s:keep", targetID)),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🗑️ Purge Storage & Delete", fmt.Sprintf("acc:del_exec:%s:purge", targetID)),
				),
				tgbotapi.NewInlineKeyboardRow(
					tgbotapi.NewInlineKeyboardButtonData("🔙 Cancel", fmt.Sprintf("acc:del_cancel:%s", targetID)),
				),
			)
			editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, confirmText)
			editMsg.ParseMode = "HTML"
			editMsg.ReplyMarkup = &confirmKeyboard
			if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
				log.Printf("[AccountPool] Failed to send delete confirmation: %v", err)
			}
			return true
		}

	case "del_cancel":
		bot.Request(tgbotapi.NewCallback(cb.ID, "Deletion cancelled"))
		if targetID != "" {
			if acc, err := GlobalAccountPool.GetAccount(targetID); err == nil && acc != nil {
				cardText, cardKeyboard := formatAccountCard(GlobalAccountPool, acc, chatID, botName)
				editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, cardText)
				editMsg.ParseMode = "HTML"
				editMsg.ReplyMarkup = &cardKeyboard
				if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
					log.Printf("[AccountPool] Failed to return to card on cancel: %v", err)
				}
				return true
			}
		}
		// Fallback to dashboard if account not found or no targetID
		dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID, botName)
		editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, dashboardText)
		editMsg.ParseMode = "HTML"
		editMsg.ReplyMarkup = &keyboard
		if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
			log.Printf("[AccountPool] Failed to return to dashboard on cancel: %v", err)
		}
		return true

	case "del_exec":
		if len(parts) >= 4 {
			delTargetID := parts[2]
			purge := parts[3] == "purge"
			if err := GlobalAccountPool.DeleteAccount(delTargetID, purge); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Account %s deleted", delTargetID)))
			stayOnCard = false // Account deleted, return to dashboard
		}

	case "cooldown":
		if targetID != "" {
			if err := GlobalAccountPool.ClearCooldown(targetID); err != nil {
				bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Error: %v", err)))
				return true
			}
			bot.Request(tgbotapi.NewCallback(cb.ID, fmt.Sprintf("Cooldown cleared for %s", targetID)))
			stayOnCard = true
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

	// Update message in place: either card (if stayOnCard) or master dashboard
	if stayOnCard && targetID != "" {
		if acc, _ := GlobalAccountPool.GetAccount(targetID); acc != nil {
			cardText, cardKeyboard := formatAccountCard(GlobalAccountPool, acc, chatID, botName)
			editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, cardText)
			editMsg.ParseMode = "HTML"
			editMsg.ReplyMarkup = &cardKeyboard
			if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
				log.Printf("[AccountPool] Failed to update account card: %v", err)
			}
			return true
		}
	}

	dashboardText, keyboard := formatAccountsDashboard(GlobalAccountPool, chatID, botName)
	editMsg := tgbotapi.NewEditMessageText(chatID, cb.Message.MessageID, dashboardText)
	editMsg.ParseMode = "HTML"
	editMsg.ReplyMarkup = &keyboard
	if _, err := bot.Send(editMsg); err != nil && !strings.Contains(err.Error(), "message is not modified") {
		log.Printf("[AccountPool] Failed to update dashboard: %v", err)
	}
	return true
}
