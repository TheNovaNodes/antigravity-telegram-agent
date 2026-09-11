package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"engine/pkg/harvester"
)

const version = "1.3.0"

func printHelp() {
	helpText := `agy-harvester - Autonomous Session Artifact Harvester & Anti-Orphan CLI

USAGE:
  agy-harvester <subcommand> [flags]

SUBCOMMANDS:
  scan      Audit accounts and list unharvested artifact inventory
  extract   Extract artifacts from a session to a directory or ZIP archive
  doctor    Detect orphaned artifacts (>48h) stranded in session storage
  version   Show version information

FLAGS (scan):
  --all           Scan all accounts and host brain storage (default: true)
  --json          Output inventory in JSON format
  --accounts-dir  Custom accounts directory path
  --brain-dir     Custom brain storage directory path

FLAGS (extract):
  --session       Session ID to extract (required)
  --out           Target destination directory or .zip archive (required)

FLAGS (doctor):
  --max-age       Threshold age for orphan detection (default: 48h)
  --json          Output orphaned list in JSON format
  --accounts-dir  Custom accounts directory path
  --brain-dir     Custom brain storage directory path

EXAMPLES:
  agy-harvester scan --all
  agy-harvester extract --session sess_12345 --out ./artifacts.zip
  agy-harvester extract --session sess_12345 --out ./my_docs/
  agy-harvester doctor --max-age 48h
`
	fmt.Print(helpText)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(0)
	}

	subcommand := os.Args[1]
	switch subcommand {
	case "scan":
		handleScan(os.Args[2:])
	case "extract":
		handleExtract(os.Args[2:])
	case "doctor":
		handleDoctor(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("agy-harvester v%s (TheNovaNodes / Pure Go Engine)\n", version)
	case "help", "-h", "--help":
		printHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n", subcommand)
		printHelp()
		os.Exit(1)
	}
}

func handleScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	allFlag := fs.Bool("all", true, "Scan all accounts and host brain")
	jsonFlag := fs.Bool("json", false, "Output in JSON format")
	accountsDir := fs.String("accounts-dir", "", "Custom accounts root")
	brainDir := fs.String("brain-dir", "", "Custom brain root")
	_ = fs.Parse(args)

	_ = allFlag
	invList, err := harvester.AuditAllSessions(*accountsDir, *brainDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error auditing sessions: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		b, _ := json.MarshalIndent(invList, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(invList) == 0 {
		fmt.Println("📭 No active or historical session artifacts discovered.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tACCOUNT\tARTIFACTS\tBREAKDOWN\tSECRETS SCRUBBED\tLAST MODIFIED")
	fmt.Fprintln(w, "----------\t-------\t---------\t---------\t----------------\t-------------")

	totalArtifacts := 0
	totalRedacted := 0
	for _, inv := range invList {
		totalArtifacts += inv.ArtifactCount
		totalRedacted += inv.RedactedSecretsCount
		timeStr := inv.LastModified.Format("2006-01-02 15:04:05")
		if inv.LastModified.IsZero() {
			timeStr = "unknown"
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\n",
			inv.SessionID, inv.AccountSlug, inv.ArtifactCount, inv.Breakdown, inv.RedactedSecretsCount, timeStr)
	}
	_ = w.Flush()
	fmt.Printf("\n✨ Discovered %d session(s) with %d total artifacts (%d secrets redacted).\n",
		len(invList), totalArtifacts, totalRedacted)
}

func handleExtract(args []string) {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	sessionID := fs.String("session", "", "Target session ID")
	outPath := fs.String("out", "", "Target destination (.zip or directory)")
	_ = fs.Parse(args)

	if *sessionID == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "❌ Both --session and --out flags are required for extraction.")
		fmt.Fprintln(os.Stderr, "Usage: agy-harvester extract --session <session-id> --out <path>")
		os.Exit(1)
	}

	start := time.Now()
	report, err := harvester.ExtractSession(*sessionID, *outPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Extraction failed: %v\n", err)
		os.Exit(1)
	}

	duration := time.Since(start)
	fmt.Printf("📦 Successfully harvested session %s in %s\n", *sessionID, duration)
	fmt.Printf("📁 Extracted: %d artifact(s) (%s)\n", len(report.Artifacts), harvester.FormatBreakdown(report.Artifacts))
	if report.RedactedSecretsCount > 0 {
		fmt.Printf("🛡️ Intercepted & redacted: %d credentials/secrets\n", report.RedactedSecretsCount)
	}
	fmt.Printf("🚀 Destination: %s\n", *outPath)
}

func handleDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	maxAgeStr := fs.String("max-age", "48h", "Threshold duration for orphan detection")
	jsonFlag := fs.Bool("json", false, "Output in JSON format")
	accountsDir := fs.String("accounts-dir", "", "Custom accounts root")
	brainDir := fs.String("brain-dir", "", "Custom brain root")
	_ = fs.Parse(args)

	maxAge, err := time.ParseDuration(*maxAgeStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Invalid duration --max-age: %v\n", err)
		os.Exit(1)
	}

	orphans, err := harvester.RunDoctor(*accountsDir, *brainDir, maxAge)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running doctor audit: %v\n", err)
		os.Exit(1)
	}

	if *jsonFlag {
		b, _ := json.MarshalIndent(orphans, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(orphans) == 0 {
		fmt.Printf("✅ All session artifacts healthy! No orphaned artifacts older than %s detected.\n", *maxAgeStr)
		return
	}

	fmt.Printf("⚠️ Detected %d orphaned artifact(s) older than %s not committed to git:\n\n", len(orphans), *maxAgeStr)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tKIND\tTITLE\tAGE\tPATH")
	fmt.Fprintln(w, "----------\t----\t-----\t---\t----")
	for _, orph := range orphans {
		fmt.Fprintf(w, "%s\t%s\t%s\t%.1fh\t%s\n",
			orph.SessionID, orph.Kind, truncate(orph.Title, 30), orph.AgeHours, orph.Path)
	}
	_ = w.Flush()

	fmt.Println("\n💡 Actionable Recovery:")
	fmt.Println("To extract and salvage these artifacts, run:")
	for _, orph := range orphans {
		fmt.Printf("  agy-harvester extract --session %s --out ./salvaged_%s/\n", orph.SessionID, orph.SessionID)
	}
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
