package harvester

import (
	"path/filepath"
	"regexp"
	"strings"
)

var (
	frontmatterTitleRegex = regexp.MustCompile(`(?m)^title:\s*["']?([^"'\r\n]+)["']?`)
	h1TitleRegex          = regexp.MustCompile(`(?m)^#\s+(.+)$`)
	checklistPatternRegex = regexp.MustCompile(`(?m)^[-*]\s+\[[ xX]\]`)
)

// ClassifyDocument categorizes a document based on its path, filename, and content.
func ClassifyDocument(path string, content string) ArtifactKind {
	base := filepath.Base(path)
	lowerBase := strings.ToLower(base)
	upperBase := strings.ToUpper(base)
	lowerContent := strings.ToLower(content)

	// 1. ADR check
	if strings.HasPrefix(upperBase, "ADR_") || strings.HasPrefix(upperBase, "ADR-") || strings.Contains(upperBase, "_ADR_") {
		return KindADR
	}
	if strings.Contains(lowerContent, "# architecture decision record") || strings.Contains(lowerContent, "# adr:") {
		return KindADR
	}
	if strings.Contains(lowerContent, "## context") && (strings.Contains(lowerContent, "## consequences") || strings.Contains(lowerContent, "## status")) {
		return KindADR
	}

	// 2. RFC check
	if strings.HasPrefix(upperBase, "RFC_") || strings.HasPrefix(upperBase, "RFC-") || strings.Contains(upperBase, "_RFC_") {
		return KindRFC
	}
	if strings.Contains(lowerContent, "# request for comments") || strings.Contains(lowerContent, "# rfc:") {
		return KindRFC
	}
	if strings.Contains(lowerContent, "## motivation") && strings.Contains(lowerContent, "## proposal") {
		return KindRFC
	}

	// 3. Research & Audit check
	if strings.Contains(lowerBase, "research") || strings.Contains(lowerBase, "audit") || strings.Contains(lowerBase, "gap_analysis") {
		return KindResearch
	}
	if strings.Contains(lowerContent, "# research report") || strings.Contains(lowerContent, "# audit report") || strings.Contains(lowerContent, "# security audit") {
		return KindResearch
	}

	// 4. Checklist check
	if strings.HasPrefix(lowerBase, "checklist") || strings.HasPrefix(lowerBase, "acceptance") {
		return KindChecklist
	}
	if matches := checklistPatternRegex.FindAllString(content, 4); len(matches) >= 3 {
		return KindChecklist
	}

	// 5. Specification check
	if strings.HasPrefix(lowerBase, "spec_") || strings.HasPrefix(lowerBase, "api_") || strings.HasPrefix(lowerBase, "contract_") {
		return KindSpec
	}
	if strings.Contains(lowerContent, "# specification") || strings.Contains(lowerContent, "# api specification") {
		return KindSpec
	}

	return KindDoc
}

// ExtractDocumentTitle extracts a readable title in priority:
// 1. YAML frontmatter `title:`
// 2. First Markdown `# H1` header
// 3. Cleaned filename
func ExtractDocumentTitle(path string, content string) string {
	// 1. Frontmatter
	if matches := frontmatterTitleRegex.FindStringSubmatch(content); len(matches) > 1 {
		title := strings.TrimSpace(matches[1])
		if title != "" {
			return title
		}
	}

	// 2. Markdown H1
	if matches := h1TitleRegex.FindStringSubmatch(content); len(matches) > 1 {
		title := strings.TrimSpace(matches[1])
		// Remove markdown link syntax if present in H1
		title = strings.Trim(title, "*_`#")
		if title != "" {
			return title
		}
	}

	// 3. Cleaned Base filename
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	return titleCase(name)
}

func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
		}
	}
	return strings.Join(words, " ")
}
