# 🔐 Secrets Management & Production Hardening Guide

This document outlines standard operational practices for provisioning, storing, and rotating credentials for `antigravity-telegram-agent`.

---

## 1. Threat Model & Principles

1. **No Secrets in Source Control or Working Directory**:
   - Plaintext `.env` files adjacent to the binary in the working directory represent a high security risk (accidental bundle exports, directory listing disclosure, or subagent file reads).
   - In production mode (`ALLOW_DOTENV != "1"`), the engine fails closed if an environment file is missing from its isolated path (`ENV_FILE`, default `/etc/antigravity-bot/env`).
2. **Fail-Closed Permission Enforcement**:
   - All environment files MUST have mode `0600` (`-rw-------`). If permissions are looser, the runtime attempts an immediate `chmod 0600`. If this fails, the process exits fatally (`log.Fatalf`).
3. **Operational Capabilities for Dev/Admin Swarm**:
   - Bot engine processes run with root environment access to ensure coding agents can manage host system configurations, systemd units, SSH credentials, and developer tooling without artificial filesystem barriers.

---

## 2. Production Deployment Patterns

### Pattern A: Standard Systemd Environment File (Default)

Provision the credentials into `/etc/antigravity-bot/env` with restricted ownership:

```bash
sudo mkdir -p /etc/antigravity-bot
sudo chmod 0700 /etc/antigravity-bot

sudo tee /etc/antigravity-bot/env << 'EOF'
BOT_TOKENS=123456789:AAExampleTokenOne,987654321:AAExampleTokenTwo
ALLOWED_ADMIN_IDS=12345678,87654321
ELEVENLABS_API_KEY=sk_example_key_1,sk_example_key_2
EOF

sudo chmod 0600 /etc/antigravity-bot/env
```

In the systemd service definition (`/etc/systemd/system/antigravity-bot-engine.service`):

```ini
[Service]
EnvironmentFile=/etc/antigravity-bot/env
Environment="ENV_FILE=/etc/antigravity-bot/env"
```

---

### Pattern B: HashiCorp Vault Agent Integration

When operating in enterprise or cloud clusters, deploy a Vault Agent sidecar or template renderer:

```hcl
auto_auth {
  method "approle" {
    config = {
      role_id_file_path   = "/etc/vault-agent/role-id"
      secret_id_file_path = "/etc/vault-agent/secret-id"
    }
  }
}

template {
  contents = <<EOH
{{ with secret "secret/data/antigravity/production" }}
BOT_TOKENS={{ .Data.data.BOT_TOKENS }}
ALLOWED_ADMIN_IDS={{ .Data.data.ALLOWED_ADMIN_IDS }}
ELEVENLABS_API_KEY={{ .Data.data.ELEVENLABS_API_KEY }}
{{ end }}
EOH
  destination = "/etc/antigravity-bot/env"
  perms       = "0600"
  command     = "systemctl restart antigravity-bot-engine.service"
}
```

---

### Pattern C: AWS Secrets Manager / Parameter Store

For AWS EC2 or ECS deployments, inject secrets via an IAM-authenticated fetch script in systemd `ExecStartPre`:

```bash
#!/usr/bin/env bash
set -euo pipefail

SECRET_JSON=$(aws secretsmanager get-secret-value \
  --secret-id "prod/antigravity/telegram-gateway" \
  --query 'SecretString' \
  --output text)

mkdir -p /etc/antigravity-bot
chmod 0700 /etc/antigravity-bot

echo "$SECRET_JSON" | jq -r 'to_entries | .[] | "\(.key)=\(.value)"' > /etc/antigravity-bot/env
chmod 0600 /etc/antigravity-bot/env
```

In the systemd unit:

```ini
[Service]
ExecStartPre=/usr/local/bin/fetch-antigravity-secrets.sh
EnvironmentFile=/etc/antigravity-bot/env
Environment="ENV_FILE=/etc/antigravity-bot/env"
```

---

## 3. Local Development Mode

For local development where a `.env` in the working directory is desired:

```bash
# Explicitly enable dev mode fallback
export ALLOW_DOTENV=1
go run .

# Or using Makefile
make run
```

Run `make env-check` at any time to verify that no stray `.env` files are left exposed in the repository tree during production staging.
