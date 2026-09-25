# 🛸 Operational Runbook: Session Artifact Harvester & Anti-Orphan Pipeline

## 1. Overview & Architecture

The **Harvester Pipeline** prevents architectural degradation, knowledge amnesia, and uncommitted documentation loss across the NovaNodes multi-account ecosystem.

During agent coding turns, markdown artifacts (ADRs, RFCs, research documents, specs, deployment checklists) are frequently generated in ephemeral session directories (`<ACCOUNTS_DIR>/*/brain/<sessionID>/`). The Harvester guarantees:
1. **Zero-Leakage Sanitization**: Intercepts and redacts GitHub PATs, Telegram Bot tokens, JWTs, SSH private keys, and API tokens before exposure.
2. **Deterministic Taxonomy**: Automatic categorization into `ADR`, `RFC`, `RESEARCH`, `CHECKLIST`, `SPEC`, and `DOC`.
3. **Structured Packaging**: Bundling with `manifest.json` into taxonomy-organized ZIP archives.
4. **Anti-Orphan Safeguards**: Automated extraction on `/export`, Safe Parking during 429 quota cooldowns, and audit telemetry via Prometheus.

```
Session Lifecycle
       │
       ├─► In-Flight Creation / Edits  ──► Streaming JSONL Parser
       │
       ├─► User triggers /export       ──► HarvestSession ──► Companion ZIP + Enriched Telegram Caption
       │
       ├─► Quota 429 Cooldown Strike   ──► Safe Parking   ──► Auto-packaged to scratch/exports/
       │
       └─► Routine Operations (Doctor) ──► agy-harvester  ──► Orphan detection & Prometheus metrics
```

---

## 2. Automated Harvesting Workflows

### 2.1 Telegram `/export` Command
When an authorized user sends `/export` (or clicks the `📄 Export` inline button):
1. The engine compiles the conversation history into a sanitized Markdown transcript (`session_<title>_<id>.md`).
2. Harvester discovers all engineering documents written during the session across accounts.
3. If artifacts are detected, they are bundled into a companion `artifacts_<title>_<id>.zip` and sent directly to the Telegram chat.
4. Caption is automatically enriched:
   ```markdown
   📄 *Session Transcript Export*
   🏷 *Title:* Pure Go Engine Architecture
   👣 *Steps:* 42
   📦 *Extracted Artifacts:* 3 (1 ADR, 2 RESEARCH)
   ```

### 2.2 Tier 2 Quota Safe Parking (429 Exhaustion)
When upstream model quotas are exhausted across all rotation accounts:
1. Safe Parking initiates immediately.
2. Active state, transcript, and all session artifacts are extracted and saved into `scratch/exports/`.
3. If Telegram API is reachable, both documents are delivered with quota alert notice.
4. Subprocesses are gracefully parked without data loss.

---

## 3. Standalone CLI: `agy-harvester`

The standalone utility provides batch audit, manual rescue, and scheduled orphan detection capabilities without running the Telegram bot daemon.

### 3.1 Compilation & Installation
```bash
go build -o /usr/local/bin/agy-harvester ./cmd/agy-harvester
agy-harvester --help
```

### 3.2 Auditing All Accounts (`scan`)
Audit all active accounts and host brain storage to list artifact inventories:
```bash
# Tabular overview
agy-harvester scan --all

# Machine-readable JSON output for automated CI/CD checks
agy-harvester scan --all --json
```

**Sample Output:**
```
SESSION ID            ACCOUNT      ARTIFACTS   BREAKDOWN              SECRETS SCRUBBED   LAST MODIFIED
----------            -------      ---------   ---------              ----------------   -------------
sess_986702_alpha     thedoctormes 3           1 ADR, 2 RESEARCH      0                  2026-09-11 06:30:15
sess_441203_beta      caduceus     2           1 SPEC, 1 CHECKLIST    1                  2026-09-11 05:14:22

✨ Discovered 2 session(s) with 5 total artifacts (1 secrets redacted).
```

### 3.3 Manual Session Extraction (`extract`)
Extract artifacts from any session into a structured folder or ZIP archive:
```bash
# Extract to structured ZIP archive
agy-harvester extract --session sess_986702_alpha --out /tmp/session_export.zip

# Extract to structured directory
agy-harvester extract --session sess_986702_alpha --out ./salvaged_docs/
```

**Resulting Directory Structure:**
```
salvaged_docs/
├── adr/
│   └── ADR_001_Pure_Go_Runtime.md
├── research/
│   ├── memory_profile_audit.md
│   └── streaming_benchmarks.md
└── manifest.json
```

### 3.4 Orphan Detection (`doctor`)
Detect orphaned engineering documents older than 48 hours that remain in temporary session folders and are not committed to Git:
```bash
# Standard 48-hour orphan audit
agy-harvester doctor

# Custom threshold (e.g. 24 hours)
agy-harvester doctor --max-age 24h

# JSON output for alerting scripts
agy-harvester doctor --max-age 48h --json
```

---

## 4. Prometheus Telemetry & Metrics

The bot daemon and harvester export Prometheus metrics when configured via `METRICS_ADDR` or `METRICS_PORT`.

### 4.1 Configuration
Add to `.env` or container environment:
```ini
# Enable metrics server on port 9090
METRICS_ADDR=:9090
```

Verify metrics endpoint:
```bash
curl http://localhost:9090/metrics
curl http://localhost:9090/healthz
```

### 4.2 Metrics Catalog

| Metric | Type | Labels | Description |
| :--- | :---: | :---: | :--- |
| `agy_harvester_extracted_total` | Counter | `kind="ADR\|RFC\|RESEARCH\|CHECKLIST\|SPEC\|DOC"` | Total artifacts extracted and preserved. |
| `agy_harvester_sanitized_secrets_total` | Counter | _none_ | Total credentials/secrets intercepted and masked. |
| `agy_harvester_orphan_uncommitted_count` | Gauge | _none_ | Current number of uncommitted artifacts older than 48h. |
| `session_context_resets_total` | Counter | `bot="<name>"` | Total runtime context desyncs where requested conversation ID was rejected by CLI (#303). |

### 4.3 Recommended Alerting Rules
```yaml
groups:
  - name: harvester.alerts
    rules:
      - alert: OrphanArtifactsDetected
        expr: agy_harvester_orphan_uncommitted_count > 0
        for: 2h
        labels:
          severity: warning
        annotations:
          summary: "Orphaned engineering artifacts stranded in session storage"
          description: "{{ $value }} artifact(s) older than 48 hours are uncommitted. Run 'agy-harvester doctor' to rescue."

      - alert: CredentialRedactionSpike
        expr: rate(agy_harvester_sanitized_secrets_total[15m]) > 5
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "High volume of credentials intercepted in agent outputs"
          description: "Agent is outputting sensitive credentials. Check session logs and sanitize upstream prompts."
```

---

## 5. Troubleshooting & Maintenance

1. **Artifacts Missing from Transcript Export**:
   - Verify that the artifact file was saved with `.md` extension.
   - Check if the artifact was stored in excluded directories (`.system_generated`, `.tempmediaStorage`, `scratch`). Files in session root are always harvested.

2. **Secret False Positives**:
   - If a test dummy string was redacted as `[REDACTED_SECRET:GITHUB_PAT]`, review `pkg/harvester/sanitizer.go`. Synthetic test credentials should use clearly marked mock prefixes.

3. **Disk Maintenance**:
   - `CleanMediaAndExports` automatically reaps `scratch/exports/` files older than 24h.
   - Rescued archives can be safely committed to the project's `docs/` repository directory.
