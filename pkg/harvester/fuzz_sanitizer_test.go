package harvester

import (
	"strings"
	"testing"
)

// FuzzSanitizeContent fuzzes the Secret Shield red-team regex scrubbing engine.
// It ensures that arbitrary byte slices, null bytes, and malicious regex patterns
// never cause a panic, denial of service, or regular expression catastrophic backtracking.
func FuzzSanitizeContent(f *testing.F) {
	f.Add("github_pat_11ABCD_dummy_token_value_here")
	f.Add("AIzaSyDummySecretGoogleAPIKey123456789")
	f.Add("Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e30.t-IDcSemACt8x4iTMC6Y5uZ3iAjBgL_zsFFnn27Ec-M")
	f.Add("sk-ant-api03-abcdefghijklmnop-1234567890-XYZ")
	f.Add("-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0\n-----END RSA PRIVATE KEY-----")
	f.Add("Ordinary markdown document with # Title and code blocks.")
	f.Add(strings.Repeat("A", 1000))
	f.Add("\x00\x01\x02\x03\xff\xfe\xfd")

	f.Fuzz(func(t *testing.T, data string) {
		sanitized, redactedCount := SanitizeContent(data)
		_ = redactedCount
		if len(data) > 0 && len(sanitized) == 0 {
			t.Errorf("SanitizeContent produced empty output for non-empty input")
		}
	})
}

// FuzzClassifyDocument fuzzes the document taxonomy classifier.
func FuzzClassifyDocument(f *testing.F) {
	f.Add("docs/adr/001_decision.md", "Context and Decision")
	f.Add("docs/rfc/protocol.md", "Proposal and Specification")
	f.Add("research/benchmark.md", "Metrics and latency analysis")
	f.Add("checklists/deploy.md", "- [ ] Run migrations\n- [ ] Restart bot")
	f.Add("specs/api.md", "Endpoint definitions")
	f.Add("notes.md", "General notes")

	f.Fuzz(func(t *testing.T, path, content string) {
		kind := ClassifyDocument(path, content)
		if kind == "" {
			t.Errorf("ClassifyDocument returned empty kind")
		}
	})
}

// FuzzExtractDocumentTitle fuzzes the document title extraction from markdown headers.
func FuzzExtractDocumentTitle(f *testing.F) {
	f.Add("docs/adr/001_decision.md", "# Canonical System Title\n\nContent paragraph.")
	f.Add("docs/rfc/protocol.md", "=== Title Header ===\nSome text")
	f.Add("specs/system.md", "title: Frontmatter Title\n---\nBody")
	f.Add("notes.md", "No headers here at all, just plain text.")
	f.Add("deep.md", strings.Repeat("#", 50)+" Deep header")
	f.Add("", "")
	f.Add("   ", "   ")

	f.Fuzz(func(t *testing.T, path, content string) {
		title := ExtractDocumentTitle(path, content)
		if title == "" {
			t.Errorf("ExtractDocumentTitle returned empty title")
		}
	})
}
