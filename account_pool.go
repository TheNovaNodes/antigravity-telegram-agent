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
	pinnedChat  map[int64]string // chatID -> accountID (Sticky Lock)
	activeChat  map[int64]string // chatID -> accountID (Current active assignment)
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
		pinnedChat:  make(map[int64]string),
		activeChat:  make(map[int64]string),
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

	stateFile := p.stateFilePath()
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
		for kStr, v := range state.PinnedChat {
			var chatID int64
			if _, err := fmt.Sscanf(kStr, "%d", &chatID); err == nil {
				p.pinnedChat[chatID] = v
			}
		}
	}
	if state.ActiveChat != nil {
		for kStr, v := range state.ActiveChat {
			var chatID int64
			if _, err := fmt.Sscanf(kStr, "%d", &chatID); err == nil {
				p.activeChat[chatID] = v
			}
		}
	}

	// Verify profile directories exist and have valid permissions
	now := time.Now()
	for _, acc := range p.accounts {
		if acc.State == StateCooldown && now.After(acc.CooldownUntil) {
			acc.State = StateActive
			acc.CooldownUntil = time.Time{}
		}
		if acc.HomeDir != "" {
			_ = os.MkdirAll(acc.HomeDir, 0700)
		}
	}

	log.Printf("[AccountPool] Loaded %d accounts from %s", len(p.accounts), stateFile)
	return nil
}

// SaveState persists the accounts registry to disk with 0600 permissions.
func (p *AccountPool) SaveState() error {
	state := poolStateJSON{
		Accounts:   p.accounts,
		PinnedChat: make(map[string]string),
		ActiveChat: make(map[string]string),
	}
	for k, v := range p.pinnedChat {
		state.PinnedChat[fmt.Sprintf("%d", k)] = v
	}
	for k, v := range p.activeChat {
		state.ActiveChat[fmt.Sprintf("%d", k)] = v
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}

	stateFile := p.stateFilePath()
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
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

// GetActiveAccountForChat returns the account assigned to a chat.
func (p *AccountPool) GetActiveAccountForChat(chatID int64) *Account {
	p.mu.RLock()
	defer p.mu.RUnlock()

	id, ok := p.activeChat[chatID]
	if !ok {
		return nil
	}
	if acc, exists := p.accounts[id]; exists {
		cp := *acc
		return &cp
	}
	return nil
}

// IsPinned reports whether a chat is locked to a specific account.
func (p *AccountPool) IsPinned(chatID int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, pinned := p.pinnedChat[chatID]
	return pinned
}

// GetPinnedAccount reports the pinned account ID if any.
func (p *AccountPool) GetPinnedAccount(chatID int64) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	id, pinned := p.pinnedChat[chatID]
	return id, pinned
}

// PinAccount locks a chat to a specific account.
func (p *AccountPool) PinAccount(chatID int64, accountID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.accounts[accountID]; !exists {
		return ErrAccountNotFound
	}
	p.pinnedChat[chatID] = accountID
	p.activeChat[chatID] = accountID
	return p.SaveState()
}

// UnpinAccount unlocks a chat, restoring automatic pool selection.
func (p *AccountPool) UnpinAccount(chatID int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.pinnedChat, chatID)
	return p.SaveState()
}

// SwitchAccount manually sets the active account for a chat.
func (p *AccountPool) SwitchAccount(chatID int64, accountID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	acc, exists := p.accounts[accountID]
	if !exists {
		return ErrAccountNotFound
	}
	if acc.State == StateCooldown && time.Now().Before(acc.CooldownUntil) {
		return ErrAccountInCooldown
	}
	p.activeChat[chatID] = accountID
	return p.SaveState()
}

// AcquireAccount selects an account using Sticky Lock or LRU rotation.
func (p *AccountPool) AcquireAccount(chatID int64) (*Account, error) {
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

	// 1. Check if pinned for this chat
	if pinnedID, ok := p.pinnedChat[chatID]; ok {
		if acc, exists := p.accounts[pinnedID]; exists {
			if acc.State == StateCooldown && now.Before(acc.CooldownUntil) {
				return nil, fmt.Errorf("%w: pinned account %s is resting until %s",
					ErrAccountInCooldown, pinnedID, acc.CooldownUntil.Format(time.Kitchen))
			}
			acc.ActiveTurns++
			acc.LastUsed = now
			acc.State = StateInUse
			p.activeChat[chatID] = acc.ID
			_ = p.SaveState()
			cp := *acc
			return &cp, nil
		}
	}

	// 2. If chat has an active assignment and it's healthy, try to retain it
	if currentID, ok := p.activeChat[chatID]; ok {
		if acc, exists := p.accounts[currentID]; exists && acc.State == StateActive {
			acc.ActiveTurns++
			acc.LastUsed = now
			acc.State = StateInUse
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
		// Oldest LastUsed first (LRU)
		return candidates[i].LastUsed.Before(candidates[j].LastUsed)
	})

	selected := candidates[0]
	selected.ActiveTurns++
	selected.LastUsed = now
	selected.State = StateInUse
	p.activeChat[chatID] = selected.ID
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

// IngestCurrentAccount parses the server's current ~/.gemini/antigravity-cli/antigravity-oauth-token,
// fetches the user email, creates an isolated profile folder, and incorporates it into the pool.
func (p *AccountPool) IngestCurrentAccount() (*Account, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	sourceTokenPath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
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
		// Allocate next ID e.g. acc-1, acc-2
		nextIdx := len(p.accounts) + 1
		targetID := fmt.Sprintf("acc-%d", nextIdx)
		for {
			if _, exists := p.accounts[targetID]; !exists {
				break
			}
			nextIdx++
			targetID = fmt.Sprintf("acc-%d", nextIdx)
		}

		profileDir := filepath.Join(p.accountsDir, targetID)
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
	profileGeminiDir := filepath.Join(targetAccount.HomeDir, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(profileGeminiDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create profile dir %s: %w", profileGeminiDir, err)
	}

	destTokenPath := filepath.Join(profileGeminiDir, "antigravity-oauth-token")
	if err := os.WriteFile(destTokenPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to write isolated token %s: %w", destTokenPath, err)
	}

	// Symlink shared global resources into the profile so skills, MCP, and brain storage remain intact
	sharedGeminiDir := filepath.Join(home, ".gemini", "antigravity-cli")
	sharedItems := []string{"builtin", "mcp_config.json", "settings.json", "brain", "agents", "knowledge"}
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
