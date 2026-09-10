# ISSUES TRIAGE MATRIX: Antigravity Bot Agent

## 1. Executive Overview
- **Repository:** `TheNovaNodes/antigravity-go-tg-bot-agent`
- **Total Open Issues Analysed:** 8 (#243, #244, #245, #246, #247, #248, #249, #251)
- **Distribution:**
  - 1 FinOps/CI Optimization RFC (#251)
  - 1 Subsystem Master Epic (#249)
  - 5 Harvester/Artifact Component Tasks (#244, #245, #246, #247, #248)
  - 1 Core Architecture RFC (#243)
- **Quality Audit Verdict:** The backlog is highly focused on the new Harvester subsystem, but lacks clear epic containment. A dangerous ambiguity was flagged during Red Team audit regarding Issue #245 vs existing legacy artifact transport in `session.go`.

---

## 2. Red Team Audit: Jules Hallucination Deconstruction
> [!WARNING]
> **Swarm Agent Blindspot Detected & Corrected:**
> Jules initially recommended closing **Issue #245** believing it was implemented in `session.go` (`ExtractAllowedArtifacts` / `sendArtifacts`).
> **Red Team Inspection:** `session.go` only contains the legacy Telegram document sender. Issue #245 specifically demands the new streaming JSONL transcript parser and document classifier (`pkg/harvester/transcript.go` and `pkg/harvester/classifier.go`), which does NOT exist yet. Closing #245 would have abandoned Phase 2 of the Harvester subsystem!

---

## 3. Section 1 — 🔗 EPIC CONSOLIDATION (Subsystem Harvester)
- **Parent Master Epic:** **#249: `epic(artifacts): Session Artifact Engine & Harvester`**
- **Clustered Sub-Tasks:**
  - **#244: `feat(harvester): Multi-Account Session Discovery`** — Harvester scanner for multi-account `brain/` dirs.
  - **#245: `feat(harvester): Streaming Transcript Parser & Document Taxonomy Classifier`** — Streaming JSONL parser (`pkg/harvester/transcript.go`) and taxonomy categorizer (`pkg/harvester/classifier.go`).
  - **#246: `feat(harvester): Secret Redaction & Hygiene Gate`** — **HIGH SEVERITY SECURITY FIX**. Inspect file buffers for tokens/keys prior to transport.
  - **#247: `feat(core): Automated Artifact Metadata Sync`** — Metadata extractor and database/index sync.
  - **#248: `feat(cli/telemetry): State Dashboard & Diagnostic CLI`** — Harvester telemetry and status dashboard.

---

## 4. Section 2 — 🚨 CRITICAL & IMMEDIATE ACTION ITEMS
1. **Issue #246 (`Secret Redaction & Hygiene Gate`):**
   - **Vulnerability:** Current `session.go:sendArtifacts` only validates path boundary (`isPathUnderRoot`). It transmits raw files without secret scanning. Must be promoted to top priority before widespread harvester deployment.
2. **Issue #251 (`FinOps CI/CD Speedup Manifest`):**
   - **FinOps:** Optimize GitHub Actions runner minutes (Go build cache, mod cache, concurrency cancellation).

---

## 5. Section 3 — 📋 VALID ARCHITECTURAL BACKLOG
- **Issue #243 (`rfc: Swarm Arena — Multi-Agent Consensus Architecture`):**
  - Retain in long-term strategic backlog. Defines future multi-agent debate and consensus protocols.

---

## 6. Summary Action Table

| Issue # | Title | Recommended Action | Concrete Rationale | Suggested Closing/Status Comment |
| :--- | :--- | :--- | :--- | :--- |
| **#246** | `feat(harvester): Secret Redaction & Hygiene Gate` | **PROMOTE (CRITICAL)** & **CLUSTER** | Security flaw: unredacted artifact transmission. Sub-task of Epic #249. | `"Promoting to critical security priority within Epic #249."` |
| **#251** | `[RFC] Оптимизация расхода CI/CD минут в GitHub Actions` | **PROMOTE** | FinOps priority. Apply concurrency cancellation and Go caching to ci.yml. | `"Promoting to active sprint for immediate CI optimization."` |
| **#249** | `epic(artifacts): Session Artifact Engine & Harvester` | **RETAIN (EPIC)** | Master architectural epic for the entire artifact harvester pipeline. | `"Retaining as umbrella epic for issues #244-#248."` |
| **#244** | `feat(harvester): Multi-Account Session Discovery` | **CLUSTER** | Phase 1 of Harvester. Belongs under Epic #249. | `"Clustering as sub-task under Epic #249."` |
| **#245** | `feat(harvester): Streaming Transcript Parser & Document Taxonomy Classifier` | **CLUSTER / RETAIN** | Phase 2 of Harvester (`pkg/harvester/transcript.go`). NOT implemented yet. | `"Clustering as sub-task under Epic #249."` |
| **#247** | `feat(core): Automated Artifact Metadata Sync` | **CLUSTER** | Phase 3 of Harvester. Belongs under Epic #249. | `"Clustering as sub-task under Epic #249."` |
| **#248** | `feat(cli/telemetry): State Dashboard & Diagnostic CLI` | **CLUSTER** | Phase 4 of Harvester. Belongs under Epic #249. | `"Clustering as sub-task under Epic #249."` |
| **#243** | `rfc: Swarm Arena — Multi-Agent Consensus Architecture` | **RETAIN** | Valid long-term multi-agent consensus architecture. | `"Retained in strategic architectural backlog."` |
