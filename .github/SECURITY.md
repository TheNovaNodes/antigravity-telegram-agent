# Security Policy

## Supported Versions

The following versions of `antigravity-telegram-agent` currently receive security updates:

| Version | Supported          |
| ------- | ------------------ |
| latest master | :white_check_mark: |
| v1.3.x  | :white_check_mark: |
| v1.2.x  | :white_check_mark: |
| < 1.2.0 | :x:                |

## Reporting a Vulnerability

The NovaNodes Collective takes the security of agent systems and credential isolation seriously.

If you discover a security vulnerability (such as credential exposure, path traversal, command injection, or privilege escalation):

1. **Do NOT open a public GitHub issue.**
2. Report the vulnerability privately to the project maintainers via encrypted communication or direct administrative channel:
   - Contact: **ЗавЛаб** (@TheNovaNodes)
   - Email / PGP: As specified in NovaNodes Collective registry
3. Include:
   - Detailed description of the vulnerability.
   - Steps to reproduce or proof-of-concept (PoC).
   - Potential impact on host systems, Telegram bot sessions, or Google Antigravity accounts.

### Response Timelines
- **Initial Response:** Within 12 hours.
- **Triage & Status Assessment:** Within 24 hours.
- **Patch & Remediation PR:** Within 48 hours.

## Security Directives & Invariants
- **No Hardcoded Secrets:** All credentials must be provisioned via `/etc/antigravity-bot/env` (mode `0600`) or environment variables.
- **Fail-Closed Permissions:** The engine verifies file permissions at startup and terminates if sensitive configurations are publicly readable.
- **Process Group Isolation:** Subprocesses are sandboxed within distinct process groups (`Setpgid`) and reaped upon timeout or interruption.
