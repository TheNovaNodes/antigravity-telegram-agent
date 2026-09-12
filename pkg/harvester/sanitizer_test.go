package harvester

import (
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeContent_RedTeamTokens(t *testing.T) {
	// Synthetic red-team tokens suite (20 distinct tokens)
	tokens := []struct {
		Name  string
		Token string
	}{
		{"GHP_1", "ghp_111111111122222222223333333333444444"},
		{"GHP_2", "ghp_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789"},
		{"GITHUB_PAT_1", "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"},
		{"GITHUB_PAT_2", "github_pat_aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStTuUvVwWxXyYzZ0123456789_abcdefghijklmnop"},
		{"TG_1", "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ_0123456"},
		{"TG_2", "9876543210:ZYXwvuTSRqpoNMLkjiHGFedCBA-9876543"},
		{"BEARER_1", "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.doNotLeakThisSignatureHere"},
		{"BEARER_2", "Bearer dGhpc19pc19hX3ZlcnlfbG9uZ19zZWNyZXRfdG9rZW5fZm9yX2F1dGg="},
		{"OPENAI_1", "sk-1111111111222222222233333333334444444444"},
		{"OPENAI_2", "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		{"ANTHROPIC_1", "sk-ant-api03-1234567890abcdefghijklmnopqrstuvwxyz"},
		{"ANTHROPIC_2", "sk-ant-live-abcdefghijklmnopqrstuvwxyz0123456789"},
		{"GENERIC_SK_1", "sk_test_12345678901234567890123456789012"},
		{"GENERIC_SK_2", "sk_live_abcdefghijklmnopqrstuvwxyz012345"},
		{"PRIVATE_KEY_RSA", "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0m4w...\n-----END RSA PRIVATE KEY-----"},
		{"PRIVATE_KEY_EC", "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEI...\n-----END EC PRIVATE KEY-----"},
		{"PRIVATE_KEY_GENERIC", "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEG...\n-----END PRIVATE KEY-----"},
		{"TG_3", "112233445:a-b_c-d_e-f_g-h_i-j_k-l_m-n_o-p_1"},
		{"GHP_3", "ghp_ZZZZZZZZZZYYYYYYYYYYXXXXXXXXXXWWWWWW"},
		{"BEARER_3", "Bearer random_generated_super_secret_jwt_payload_string"},
	}

	if len(tokens) != 20 {
		t.Fatalf("expected exactly 20 test tokens, got %d", len(tokens))
	}

	var sb strings.Builder
	sb.WriteString("# Secret Leakage Benchmark Document\n\n")
	for _, tok := range tokens {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", tok.Name, tok.Token))
	}
	input := sb.String()

	sanitized, count := SanitizeContent(input)

	if count < len(tokens) {
		t.Fatalf("expected at least %d redactions, got %d", len(tokens), count)
	}

	// Assert ZERO leaks of original tokens in sanitized content
	for _, tok := range tokens {
		if strings.Contains(sanitized, tok.Token) {
			t.Errorf("LEAK DETECTED! Token %s was not redacted: %s", tok.Name, tok.Token)
		}
	}
}

func TestSanitizeContent_HostPathRelinker(t *testing.T) {
	input := "See artifact at /etc/antigravity-bot/accounts/acc-1/.gemini/antigravity-cli/brain/123e4567-e89b-12d3-a456-426614174000/report.md and fallback /home/user/.gemini/antigravity-cli/brain/123e4567-e89b-12d3-a456-426614174000/summary.md and macos /Users/alex/.gemini/antigravity-cli/brain/123e4567-e89b-12d3-a456-426614174000/notes.md"
	sanitized, _ := SanitizeContent(input)

	if strings.Contains(sanitized, "/etc/antigravity-bot") || strings.Contains(sanitized, "/home/user/.gemini") || strings.Contains(sanitized, "/Users/alex/.gemini") {
		t.Fatalf("host paths were not relinked: %s", sanitized)
	}

	if !strings.Contains(sanitized, "./artifacts/report.md") || !strings.Contains(sanitized, "./artifacts/summary.md") || !strings.Contains(sanitized, "./artifacts/notes.md") {
		t.Fatalf("expected relative artifacts link, got: %s", sanitized)
	}
}

func TestComputeSHA256(t *testing.T) {
	doc1 := "Hello World\n   \t\n"
	doc2 := "Hello World"

	hash1 := ComputeSHA256(doc1)
	hash2 := ComputeSHA256(doc2)

	if hash1 != hash2 {
		t.Fatalf("expected identical hashes for whitespace-normalized content: %s vs %s", hash1, hash2)
	}

	if len(hash1) != 64 {
		t.Fatalf("expected 64 hex characters for SHA-256, got %d", len(hash1))
	}
}
