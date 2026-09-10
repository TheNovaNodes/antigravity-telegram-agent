package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// AccountState represents the operational status of an account profile in the pool.
type AccountState int

const (
	// StateActive indicates the account is healthy, authenticated, and available for turns.
	StateActive AccountState = iota
	// StateInUse indicates the account is currently assigned to an active streaming turn.
	StateInUse
	// StateCooldown indicates the account received 429/quota error and is resting until CooldownUntil.
	StateCooldown
	// StateExpired indicates the token is invalid or missing and requires re-authentication.
	StateExpired
)

func (s AccountState) String() string {
	switch s {
	case StateActive:
		return "Active"
	case StateInUse:
		return "In-Use"
	case StateCooldown:
		return "Cooldown"
	case StateExpired:
		return "Expired"
	default:
		return "Unknown"
	}
}

// ModelQuota holds the usage percentage and reset time for a quota window (#228).
type ModelQuota struct {
	RemainingFraction float64   `json:"remaining_fraction"`
	ResetTime         time.Time `json:"reset_time"`
}

// AccountQuota encapsulates both Gemini and Claude/GPT quota windows (#228).
type AccountQuota struct {
	Gemini5h      ModelQuota `json:"gemini_5h"`
	GeminiWeekly  ModelQuota `json:"gemini_weekly"`
	Claude5h      ModelQuota `json:"claude_5h"`
	ClaudeWeekly  ModelQuota `json:"claude_weekly"`
	LastFetchedAt time.Time  `json:"last_fetched_at"`
}

// Account represents an isolated Google Antigravity account profile.
type Account struct {
	ID            string       `json:"id"`
	Email         string       `json:"email"`
	HomeDir       string       `json:"home_dir"`
	State         AccountState `json:"state"`
	CooldownUntil time.Time    `json:"cooldown_until"`
	ActiveTurns   int          `json:"active_turns"`
	TotalErrors   int          `json:"total_errors"`
	LastUsed      time.Time    `json:"last_used"`
	Quota         AccountQuota `json:"quota"`
}

// AccountNotification carries background events such as cooldown completion.
type AccountNotification struct {
	AccountID string
	Email     string
	Message   string
}

// AccountPool manages multi-account isolation, dynamic LRU rotation, and cooldown timers.
type AccountPool struct {
	mu          sync.RWMutex
	accountsDir string
	accounts    map[string]*Account
	pinnedChat  map[string]string // chatKey -> accountID (Sticky Lock)
	activeChat  map[string]string // chatKey -> accountID (Current active assignment)
	client      *http.Client
}

var (
	// ErrNoAccountsAvailable indicates the pool has no registered accounts.
	ErrNoAccountsAvailable = errors.New("no accounts available in pool")
	// ErrAllAccountsCooldown indicates all accounts are currently resting in cooldown.
	ErrAllAccountsCooldown = errors.New("all accounts in pool are in cooldown")
	// ErrAccountNotFound indicates the requested account ID does not exist.
	ErrAccountNotFound = errors.New("account not found")
	// ErrAccountInCooldown indicates the account cannot be selected because it is in cooldown.
	ErrAccountInCooldown = errors.New("account is currently in cooldown")
)

// GlobalAccountPool is the singleton pool manager accessible across handlers and sessions.
var GlobalAccountPool *AccountPool

// getAccountsDir resolves the base directory where account profiles reside.
func getAccountsDir() string {
	if env := os.Getenv("ACCOUNTS_DIR"); env != "" {
		return env
	}
	etcDir := "/etc/antigravity-bot/accounts"
	if err := os.MkdirAll(etcDir, 0700); err == nil {
		return etcDir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".antigravity-bot", "accounts")
}

// getSharedConversationsDir resolves the shared Antigravity CLI conversations storage directory (#236).
func getSharedConversationsDir() string {
	if env := os.Getenv("CONVERSATIONS_DIR"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "conversations")
}

// EnsureSharedAccountDirectories guarantees that conversations in account home dirs
// are symlinked to the central shared conversations directory, preventing context loss on account rotation (#236).
func EnsureSharedAccountDirectories(accHomeDir string) error {
	if accHomeDir == "" {
		return nil
	}
	sharedConvs := getSharedConversationsDir()
	if err := os.MkdirAll(sharedConvs, 0700); err != nil {
		return err
	}

	accCliDir := filepath.Join(accHomeDir, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(accCliDir, 0700); err != nil {
		return err
	}

	accConvs := filepath.Join(accCliDir, "conversations")
	fi, err := os.Lstat(accConvs)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if fi.IsDir() {
			entries, _ := os.ReadDir(accConvs)
			for _, e := range entries {
				src := filepath.Join(accConvs, e.Name())
				dst := filepath.Join(sharedConvs, e.Name())
				if _, statErr := os.Stat(dst); os.IsNotExist(statErr) {
					// #nosec G304 -- gosec:nri (Need Review)
					if data, readErr := os.ReadFile(src); readErr == nil {
						// #nosec G703 G306 -- gosec:nri (Need Review)
						_ = os.WriteFile(dst, data, 0600)
					}
				}
			}
			_ = os.RemoveAll(accConvs)
		}
	}
	return os.Symlink(sharedConvs, accConvs)
}

// NewAccountPool creates and initializes an AccountPool instance.
func NewAccountPool(dir string) (*AccountPool, error) {
	if dir == "" {
		dir = getAccountsDir()
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create accounts directory %s: %w", dir, err)
	}

	pool := &AccountPool{
		accountsDir: dir,
		accounts:    make(map[string]*Account),
		pinnedChat:  make(map[string]string),
		activeChat:  make(map[string]string),
		client:      &http.Client{Timeout: 10 * time.Second},
	}

	if err := pool.LoadState(); err != nil {
		log.Printf("[AccountPool] Warning: failed to load state: %v", err)
	}
	return pool, nil
}

type poolStateJSON struct {
	Accounts   map[string]*Account `json:"accounts"`
	PinnedChat map[string]string   `json:"pinned_chat"`
	ActiveChat map[string]string   `json:"active_chat"`
}

func (p *AccountPool) stateFilePath() string {
	return filepath.Join(p.accountsDir, "accounts.json")
}

// LoadState reads the persisted pool state from accounts.json if present.
func (p *AccountPool) LoadState() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	stateFile := filepath.Clean(p.stateFilePath())
	// #nosec G304 G703 -- gosec:nri (Need Review)
	data, err := os.ReadFile(stateFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var state poolStateJSON
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("failed to parse %s: %w", stateFile, err)
	}

	if state.Accounts != nil {
		p.accounts = state.Accounts
	}
	if state.PinnedChat != nil {
		p.pinnedChat = state.PinnedChat
	}
	if state.ActiveChat != nil {
		p.activeChat = state.ActiveChat
	}

	// Verify profile directories exist, shared storage is linked, and have valid permissions
	now := time.Now()
	migrated := false
	for oldID, acc := range p.accounts {
		if acc == nil {
			continue
		}
		// Migrate legacy acc-X identifiers to email-derived slugs
		if strings.HasPrefix(oldID, "acc-") && acc.Email != "" {
			newID := DeriveAccountIDFromEmail(acc.Email)
			if newID != oldID {
				candidateID := newID
				counter := 2
				for {
					existing, exists := p.accounts[candidateID]
					if !exists || existing == acc {
						break
					}
					candidateID = fmt.Sprintf("%s-%d", newID, counter)
					counter++
				}
				newID = candidateID

				oldHome := acc.HomeDir
				newHome := filepath.Clean(filepath.Join(p.accountsDir, newID))

				if oldHome != "" && oldHome != newHome {
					if _, err := os.Stat(oldHome); err == nil {
						if _, err := os.Stat(newHome); os.IsNotExist(err) {
							if err := os.Rename(oldHome, newHome); err == nil {
								// Maintain backward compatibility via symlink
								_ = os.Symlink(newHome, oldHome)
							}
						}
					}
					acc.HomeDir = newHome
				}

				acc.ID = newID
				delete(p.accounts, oldID)
				p.accounts[newID] = acc

				for k, v := range p.pinnedChat {
					if v == oldID {
						p.pinnedChat[k] = newID
					}
				}
				for k, v := range p.activeChat {
					if v == oldID {
						p.activeChat[k] = newID
					}
				}
				migrated = true
				log.Printf("[AccountPool] Migrated legacy account %s -> %s (%s)", oldID, newID, acc.Email)
			}
		}

		if acc.State == StateCooldown && now.After(acc.CooldownUntil) {
			acc.State = StateActive
			acc.CooldownUntil = time.Time{}
		}
		if acc.HomeDir != "" {
			// #nosec G703 -- gosec:nri (Need Review)
			_ = os.MkdirAll(acc.HomeDir, 0700)
			_ = EnsureSharedAccountDirectories(acc.HomeDir)
		}
	}

	if migrated {
		_ = p.SaveState()
	}

	log.Printf("[AccountPool] Loaded %d accounts from %s", len(p.accounts), stateFile)
	return nil
}

// SaveState persists the accounts registry to disk with 0600 permissions.
func (p *AccountPool) SaveState() error {
	state := poolStateJSON{
		Accounts:   p.accounts,
		PinnedChat: p.pinnedChat,
		ActiveChat: p.activeChat,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	stateFile := filepath.Clean(p.stateFilePath())
	tmpFile := filepath.Clean(stateFile + ".tmp")
	// #nosec G304 G703 -- gosec:nri (Need Review)
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	// #nosec G703 -- gosec:nri (Need Review)
	return os.Rename(tmpFile, stateFile)
}

// ListAccounts returns a copy of all registered accounts sorted by ID.
func (p *AccountPool) ListAccounts() []*Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	res := make([]*Account, 0, len(p.accounts))
	for _, acc := range p.accounts {
		cp := *acc
		res = append(res, &cp)
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].ID < res[j].ID
	})
	return res
}

// GetAccount returns a copy of an account by ID, or error if not found.
func (p *AccountPool) GetAccount(id string) (*Account, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	acc, ok := p.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	cp := *acc
	return &cp, nil
}

func poolChatKey(chatID int64, botNames ...string) string {
	if len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		return fmt.Sprintf("%s:%d", strings.TrimSpace(botNames[0]), chatID)
	}
	return fmt.Sprintf("%d", chatID)
}

// GetActiveAccountForChat returns the account assigned to a chat, optionally scoped by bot name.
func (p *AccountPool) GetActiveAccountForChat(chatID int64, botNames ...string) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := poolChatKey(chatID, botNames...)
	id, ok := p.activeChat[key]
	if !ok && len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		id, ok = p.activeChat[fmt.Sprintf("%d", chatID)]
	}
	if !ok {
		return nil
	}
	if acc, exists := p.accounts[id]; exists {
		cp := *acc
		return &cp
	}
	return nil
}

// IsPinned reports whether a chat is locked to a specific account, optionally scoped by bot name.
func (p *AccountPool) IsPinned(chatID int64, botNames ...string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	key := poolChatKey(chatID, botNames...)
	if _, pinned := p.pinnedChat[key]; pinned {
		return true
	}
	if len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		_, pinned := p.pinnedChat[fmt.Sprintf("%d", chatID)]
		return pinned
	}
	return false
}

// GetPinnedAccount reports the pinned account ID if any, optionally scoped by bot name.
func (p *AccountPool) GetPinnedAccount(chatID int64, botNames ...string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	key := poolChatKey(chatID, botNames...)
	if id, pinned := p.pinnedChat[key]; pinned {
		return id, true
	}
	if len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		id, pinned := p.pinnedChat[fmt.Sprintf("%d", chatID)]
		return id, pinned
	}
	return "", false
}

// PinAccount locks a chat to a specific account, optionally scoped by bot name.
func (p *AccountPool) PinAccount(chatID int64, accountID string, botNames ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.accounts[accountID]; !exists {
		return ErrAccountNotFound
	}
	key := poolChatKey(chatID, botNames...)
	p.pinnedChat[key] = accountID
	p.activeChat[key] = accountID
	return p.SaveState()
}

// UnpinAccount unlocks a chat, restoring automatic pool selection.
func (p *AccountPool) UnpinAccount(chatID int64, botNames ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := poolChatKey(chatID, botNames...)
	delete(p.pinnedChat, key)
	if len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		delete(p.pinnedChat, fmt.Sprintf("%d", chatID))
	}
	return p.SaveState()
}

// SwitchAccount manually sets the active account for a chat, optionally scoped by bot name.
func (p *AccountPool) SwitchAccount(chatID int64, accountID string, botNames ...string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, exists := p.accounts[accountID]
	if !exists {
		return ErrAccountNotFound
	}
	if acc.State == StateCooldown && time.Now().Before(acc.CooldownUntil) {
		return ErrAccountInCooldown
	}
	key := poolChatKey(chatID, botNames...)
	p.activeChat[key] = accountID
	return p.SaveState()
}

// AcquireAccount selects an account using Sticky Lock or LRU rotation, optionally scoped by bot name.
func (p *AccountPool) AcquireAccount(chatID int64, botNames ...string) (*Account, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.accounts) == 0 {
		return nil, ErrNoAccountsAvailable
	}

	now := time.Now()
	// Re-check cooldown expirations
	for _, acc := range p.accounts {
		if acc.State == StateCooldown && now.After(acc.CooldownUntil) {
			acc.State = StateActive
			acc.CooldownUntil = time.Time{}
		}
	}

	key := poolChatKey(chatID, botNames...)
	legacyKey := fmt.Sprintf("%d", chatID)

	// 1. Check if pinned for this chat
	pinnedID, ok := p.pinnedChat[key]
	if !ok && len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		pinnedID, ok = p.pinnedChat[legacyKey]
	}
	if ok {
		if acc, exists := p.accounts[pinnedID]; exists {
			if acc.State == StateCooldown && now.Before(acc.CooldownUntil) {
				return nil, fmt.Errorf("%w: pinned account %s is resting until %s",
					ErrAccountInCooldown, pinnedID, acc.CooldownUntil.Format(time.Kitchen))
			}
			acc.ActiveTurns++
			acc.LastUsed = now
			acc.State = StateInUse
			p.activeChat[key] = acc.ID
			_ = p.SaveState()
			cp := *acc
			return &cp, nil
		}
	}

	// 2. If chat has an active assignment and it's healthy, try to retain it
	currentID, ok := p.activeChat[key]
	if !ok && len(botNames) > 0 && strings.TrimSpace(botNames[0]) != "" {
		currentID, ok = p.activeChat[legacyKey]
	}
	if ok {
		if acc, exists := p.accounts[currentID]; exists && acc.State == StateActive {
			acc.ActiveTurns++
			acc.LastUsed = now
			acc.State = StateInUse
			p.activeChat[key] = acc.ID
			_ = p.SaveState()
			cp := *acc
			return &cp, nil
		}
	}

	// 3. LRU Selection: Pick healthy account with fewest active turns and oldest LastUsed
	var candidates []*Account
	hasCooldown := false

	for _, acc := range p.accounts {
		if acc.State == StateActive || acc.State == StateInUse {
			candidates = append(candidates, acc)
		} else if acc.State == StateCooldown {
			hasCooldown = true
		}
	}

	if len(candidates) == 0 {
		if hasCooldown {
			return nil, ErrAllAccountsCooldown
		}
		return nil, ErrNoAccountsAvailable
	}

	sort.Slice(candidates, func(i, j int) bool {
		// Least active turns first
		if candidates[i].ActiveTurns != candidates[j].ActiveTurns {
			return candidates[i].ActiveTurns < candidates[j].ActiveTurns
		}
		// Prioritize account with higher Gemini 5h remaining quota if info available (#228)
		qI := candidates[i].Quota.Gemini5h.RemainingFraction
		qJ := candidates[j].Quota.Gemini5h.RemainingFraction
		if qI != qJ && (!candidates[i].Quota.LastFetchedAt.IsZero() || !candidates[j].Quota.LastFetchedAt.IsZero()) {
			return qI > qJ
		}
		// Oldest LastUsed first (LRU)
		return candidates[i].LastUsed.Before(candidates[j].LastUsed)
	})

	selected := candidates[0]
	selected.ActiveTurns++
	selected.LastUsed = now
	selected.State = StateInUse
	p.activeChat[key] = selected.ID
	_ = p.SaveState()

	cp := *selected
	return &cp, nil
}

// ReleaseAccount decrements active turns and sets StateActive when free.
func (p *AccountPool) ReleaseAccount(accountID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, ok := p.accounts[accountID]
	if !ok {
		return
	}
	if acc.ActiveTurns > 0 {
		acc.ActiveTurns--
	}
	if acc.ActiveTurns == 0 && acc.State == StateInUse {
		acc.State = StateActive
	}
	_ = p.SaveState()
}

// MarkCooldown places an account into StateCooldown for a specified duration.
func (p *AccountPool) MarkCooldown(accountID string, duration time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, ok := p.accounts[accountID]
	if !ok {
		return
	}
	acc.State = StateCooldown
	acc.CooldownUntil = time.Now().Add(duration)
	acc.TotalErrors++
	acc.ActiveTurns = 0
	_ = p.SaveState()
	log.Printf("[AccountPool] Account %s placed in cooldown until %s (duration %v)", accountID, acc.CooldownUntil.Format(time.RFC3339), duration)
}

// ClearCooldown removes cooldown status and makes the account active immediately.
func (p *AccountPool) ClearCooldown(accountID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, ok := p.accounts[accountID]
	if !ok {
		return ErrAccountNotFound
	}
	acc.State = StateActive
	acc.CooldownUntil = time.Time{}
	_ = p.SaveState()
	log.Printf("[AccountPool] Cooldown manually cleared for %s", accountID)
	return nil
}

// FetchEmailForToken calls Google UserInfo endpoint using the provided access token.
func (p *AccountPool) FetchEmailForToken(accessToken string) (string, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return "", errors.New("access token is empty")
	}

	req, err := http.NewRequest(http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", "antigravity-bot-agent/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("userinfo returned status %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", fmt.Errorf("decode userinfo response: %w", err)
	}
	if strings.TrimSpace(data.Email) == "" {
		return "", errors.New("userinfo response missing email")
	}
	return strings.TrimSpace(data.Email), nil
}

// ParseUsageJSON parses the raw JSON output of `agy -p "/usage" --output-format json` into an AccountQuota (#228).
func ParseUsageJSON(raw []byte) (*AccountQuota, error) {
	var resp struct {
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID                string  `json:"id"`
						Window            string  `json:"window"`
						RemainingFraction float64 `json:"remaining_fraction"`
						ResetTime         string  `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}

	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal usage json: %w", err)
	}

	quota := &AccountQuota{
		LastFetchedAt: time.Now(),
	}

	for _, g := range resp.Command.Data.Groups {
		nameLower := strings.ToLower(g.Name)
		isGemini := strings.Contains(nameLower, "gemini")
		isClaude := strings.Contains(nameLower, "claude") || strings.Contains(nameLower, "gpt")

		for _, b := range g.Buckets {
			var resetT time.Time
			if b.ResetTime != "" {
				resetT, _ = time.Parse(time.RFC3339, b.ResetTime)
			}
			mq := ModelQuota{
				RemainingFraction: b.RemainingFraction,
				ResetTime:         resetT,
			}

			if isGemini {
				if b.Window == "5h" || b.ID == "gemini-5h" {
					quota.Gemini5h = mq
				} else if b.Window == "weekly" || b.ID == "gemini-weekly" {
					quota.GeminiWeekly = mq
				}
			} else if isClaude {
				if b.Window == "5h" || b.ID == "3p-5h" {
					quota.Claude5h = mq
				} else if b.Window == "weekly" || b.ID == "3p-weekly" {
					quota.ClaudeWeekly = mq
				}
			}
		}
	}

	return quota, nil
}

// FetchAccountQuotas queries the agy CLI for quota metrics using the account's home directory (#228).
func (p *AccountPool) FetchAccountQuotas(accountID string) (*AccountQuota, error) {
	p.mu.RLock()
	acc, ok := p.accounts[accountID]
	p.mu.RUnlock()
	if !ok {
		return nil, ErrAccountNotFound
	}

	agyPath := getAgyPath()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// #nosec G204 -- gosec:nri (Need Review)
	cmd := exec.CommandContext(ctx, agyPath, "-p", "/usage", "--output-format", "json")
	cmd.Env = append(os.Environ(), "HOME="+acc.HomeDir)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to execute usage command: %w", err)
	}

	quota, err := ParseUsageJSON(out)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if currentAcc, exists := p.accounts[accountID]; exists {
		currentAcc.Quota = *quota

		now := time.Now()
		if quota.Gemini5h.RemainingFraction == 0 && !quota.Gemini5h.ResetTime.IsZero() && now.Before(quota.Gemini5h.ResetTime) {
			currentAcc.State = StateCooldown
			currentAcc.CooldownUntil = quota.Gemini5h.ResetTime
			log.Printf("[AccountPool] Account %s 5-hour quota exhausted, in cooldown until %s", accountID, quota.Gemini5h.ResetTime.Format(time.RFC3339))
		} else if quota.GeminiWeekly.RemainingFraction == 0 && !quota.GeminiWeekly.ResetTime.IsZero() && now.Before(quota.GeminiWeekly.ResetTime) {
			currentAcc.State = StateCooldown
			currentAcc.CooldownUntil = quota.GeminiWeekly.ResetTime
			log.Printf("[AccountPool] Account %s weekly quota exhausted, in cooldown until %s", accountID, quota.GeminiWeekly.ResetTime.Format(time.RFC3339))
		}

		_ = p.SaveState()
	}
	p.mu.Unlock()

	return quota, nil
}

// FetchAllQuotas updates quota metrics for all accounts in the pool concurrently (#228).
func (p *AccountPool) FetchAllQuotas() {
	p.mu.RLock()
	ids := make([]string, 0, len(p.accounts))
	for id := range p.accounts {
		ids = append(ids, id)
	}
	p.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(accID string) {
			defer wg.Done()
			if _, err := p.FetchAccountQuotas(accID); err != nil {
				log.Printf("[AccountPool] Warning: failed to fetch quotas for %s: %v", accID, err)
			}
		}(id)
	}
	wg.Wait()
}

// DeriveAccountIDFromEmail extracts the username prefix before '@' from an email address,
// sanitizing it to an alphanumeric, dash, dot, and underscore slug safe for filesystem paths and Telegram callbacks.
func DeriveAccountIDFromEmail(email string) string {
	prefix := email
	if idx := strings.Index(email, "@"); idx != -1 {
		prefix = email[:idx]
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	var sb strings.Builder
	for _, r := range prefix {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			sb.WriteRune(r)
		}
	}
	slug := strings.Trim(sb.String(), ".-_")
	if len(slug) > 32 {
		slug = slug[:32]
	}
	if slug == "" {
		slug = "account"
	}
	return slug
}

// IngestCurrentAccount parses the server's current ~/.gemini/antigravity-cli/antigravity-oauth-token,
// fetches the user email, creates an isolated profile folder, and incorporates it into the pool.
func (p *AccountPool) IngestCurrentAccount() (*Account, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	sourceTokenPath := filepath.Clean(filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"))
	// #nosec G304 G703 -- gosec:nri (Need Review)
	data, err := os.ReadFile(sourceTokenPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read token from %s: %w", sourceTokenPath, err)
	}

	var tokenPayload struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"token"`
	}
	if err := json.Unmarshal(data, &tokenPayload); err != nil {
		return nil, fmt.Errorf("failed to parse token JSON: %w", err)
	}
	if tokenPayload.Token.AccessToken == "" {
		return nil, errors.New("token file does not contain a valid access_token")
	}

	// Retrieve email via Google UserInfo API
	email, err := p.FetchEmailForToken(tokenPayload.Token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user email: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Check if an account with this email already exists
	var targetAccount *Account
	for _, acc := range p.accounts {
		if strings.EqualFold(acc.Email, email) {
			targetAccount = acc
			break
		}
	}

	if targetAccount == nil {
		baseID := DeriveAccountIDFromEmail(email)
		targetID := baseID
		counter := 2
		for {
			if _, exists := p.accounts[targetID]; !exists {
				break
			}
			targetID = fmt.Sprintf("%s-%d", baseID, counter)
			counter++
		}

		profileDir := filepath.Clean(filepath.Join(p.accountsDir, targetID))
		targetAccount = &Account{
			ID:       targetID,
			Email:    email,
			HomeDir:  profileDir,
			State:    StateActive,
			LastUsed: time.Now(),
		}
		p.accounts[targetID] = targetAccount
	}

	// Prepare isolated directory structure with strict permissions
	profileGeminiDir := filepath.Clean(filepath.Join(targetAccount.HomeDir, ".gemini", "antigravity-cli"))
	// #nosec G703 -- gosec:nri (Need Review)
	if err := os.MkdirAll(profileGeminiDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create profile dir %s: %w", profileGeminiDir, err)
	}

	destTokenPath := filepath.Clean(filepath.Join(profileGeminiDir, "antigravity-oauth-token"))
	// #nosec G304 G703 -- gosec:nri (Need Review)
	if err := os.WriteFile(destTokenPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write isolated token %s: %w", destTokenPath, err)
	}

	// Symlink shared global resources into the profile so skills, MCP, and brain storage remain intact
	sharedGeminiDir := filepath.Join(home, ".gemini", "antigravity-cli")
	sharedItems := []string{"builtin", "mcp_config.json", "settings.json", "brain", "agents", "knowledge", "conversations"}
	for _, item := range sharedItems {
		src := filepath.Join(sharedGeminiDir, item)
		dst := filepath.Join(profileGeminiDir, item)
		if _, err := os.Stat(src); err == nil {
			_ = os.Remove(dst) // remove any stale symlink or file
			_ = os.Symlink(src, dst)
		}
	}

	targetAccount.State = StateActive
	targetAccount.CooldownUntil = time.Time{}
	_ = p.SaveState()

	log.Printf("[AccountPool] Successfully ingested account %s (%s) into %s", targetAccount.ID, email, targetAccount.HomeDir)
	go func(id string) {
		_, _ = p.FetchAccountQuotas(id)
	}(targetAccount.ID)

	cp := *targetAccount
	return &cp, nil
}

// StartBackgroundReaper periodically checks accounts in cooldown and recovers them.
func (p *AccountPool) StartBackgroundReaper(ctx context.Context, notifyCallback func(acc *Account)) {
	p.StartBackgroundReaperWithInterval(ctx, 30*time.Second, notifyCallback)
}

// StartBackgroundReaperWithInterval periodically checks accounts in cooldown using a specified interval.
func (p *AccountPool) StartBackgroundReaperWithInterval(ctx context.Context, interval time.Duration, notifyCallback func(acc *Account)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.mu.Lock()
			now := time.Now()
			var recovered []*Account

			for _, acc := range p.accounts {
				if acc.State == StateCooldown && now.After(acc.CooldownUntil) {
					acc.State = StateActive
					acc.CooldownUntil = time.Time{}
					recovered = append(recovered, acc)
				}
			}

			if len(recovered) > 0 {
				_ = p.SaveState()
			}
			p.mu.Unlock()

			for _, acc := range recovered {
				log.Printf("[AccountPool] Account %s (%s) cooldown expired. Restored to Active.", acc.ID, acc.Email)
				if notifyCallback != nil {
					notifyCallback(acc)
				}
			}
		}
	}
}
