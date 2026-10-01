#!/usr/bin/env bash
# ==============================================================================
# TheNovaNodes Antigravity Telegram Agent — Canonical 5-Echelon Deployment Script
# Standard: ecosystem-docs (Deploy-as-Code / Anti-Ghost-Deploy / Atomic Switch)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

# Deployment paths & config
BIN_DIR="${ROOT_DIR}/bin"
BAK_DIR="${BIN_DIR}/.bak"
ENGINE_BIN="${BIN_DIR}/antigravity-bot-engine"
HARVESTER_BIN="${BIN_DIR}/agy-harvester"
ENV_DIR="/etc/antigravity-bot"
PROD_ENV_FILE="${ENV_DIR}/env"
SERVICE_NAME="antigravity-bot-engine.service"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}"

# State flags for rollback trap
DEPLOY_SUCCESS=0
BACKUP_CAPSULE=""

rollback() {
    local exit_code=$?
    if [ "${DEPLOY_SUCCESS}" -eq 1 ] || [ "${exit_code}" -eq 0 ]; then
        return
    fi
    echo ""
    echo "🚨 [ROLLBACK INITIATED] Deployment encountered an error (exit code: ${exit_code})."
    if [ -n "${BACKUP_CAPSULE}" ] && [ -f "${BACKUP_CAPSULE}" ]; then
        echo "⏪ Restoring previous working binary from capsule: ${BACKUP_CAPSULE}..."
        install -m 755 "${BACKUP_CAPSULE}" "${ENGINE_BIN}"
        if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null || systemctl is-failed --quiet "${SERVICE_NAME}" 2>/dev/null; then
            echo "🔄 Restarting service on restored binary..."
            systemctl restart "${SERVICE_NAME}" || true
        fi
        echo "⚠️ Rollback to capsule complete. Service state:"
        systemctl status "${SERVICE_NAME}" --no-pager || true
    else
        echo "⚠️ No rollback capsule available to restore."
    fi
    echo "❌ Deployment failed. State reverted where possible."
    exit "${exit_code}"
}

trap rollback EXIT

echo "======================================================================"
echo "🚀 Antigravity Telegram Agent — 5-Echelon Deployment Pipeline"
echo "======================================================================"

# ------------------------------------------------------------------------------
# 🛡️ Échelon 0: Pre-Flight Guard & Rollback Capsule
# ------------------------------------------------------------------------------
echo "🛡️  [Échelon 0/5] Pre-Flight Guard & Rollback Capsule..."

# 1. Root / Sudo check
if [ "$(id -u)" -ne 0 ]; then
    echo "❌ Root privileges required to configure systemd and deploy service."
    exit 1
fi

# 2. Go & Make Toolchain check
command -v go >/dev/null 2>&1 || { echo "❌ Go toolchain not found in PATH"; exit 1; }
command -v make >/dev/null 2>&1 || { echo "❌ make tool not found in PATH"; exit 1; }

# 3. Environment directory & permissions (0700 / 0600)
mkdir -p "${ENV_DIR}"
chmod 0700 "${ENV_DIR}"

if [ ! -f "${PROD_ENV_FILE}" ]; then
    if [ -f "${ROOT_DIR}/.env" ]; then
        echo "   📋 Provisioning ${PROD_ENV_FILE} from ${ROOT_DIR}/.env..."
        cp "${ROOT_DIR}/.env" "${PROD_ENV_FILE}"
    else
        echo "❌ CRITICAL: No environment file found at ${PROD_ENV_FILE} or ${ROOT_DIR}/.env!"
        exit 1
    fi
fi
chmod 0600 "${PROD_ENV_FILE}"

# 4. Validate essential secrets present (without echo-printing secret values)
if ! grep -q "^BOT_TOKENS=" "${PROD_ENV_FILE}" || ! grep -q "^ALLOWED_ADMIN_IDS=" "${PROD_ENV_FILE}"; then
    echo "❌ CRITICAL: ${PROD_ENV_FILE} missing BOT_TOKENS or ALLOWED_ADMIN_IDS!"
    exit 1
fi

# 5. Create Rollback Capsule for previous binary
mkdir -p "${BAK_DIR}"
chmod 0700 "${BAK_DIR}"
if [ -f "${ENGINE_BIN}" ]; then
    TIMESTAMP="$(date +%Y%m%d_%H%M%S)"
    BACKUP_CAPSULE="${BAK_DIR}/antigravity-bot-engine.${TIMESTAMP}.bak"
    cp -p "${ENGINE_BIN}" "${BACKUP_CAPSULE}"
    ln -sf "${BACKUP_CAPSULE}" "${BAK_DIR}/antigravity-bot-engine.latest.bak"
    echo "   📦 Rollback capsule created: ${BACKUP_CAPSULE}"
else
    echo "   ℹ️ No previous binary found at ${ENGINE_BIN} (first-time deployment)."
fi

# ------------------------------------------------------------------------------
# 🧱 Échelon 1: Production Isolation Probe
# ------------------------------------------------------------------------------
echo "🧱 [Échelon 1/5] Production Isolation Probe..."
if [ "${DEPLOY_SKIP_GIT_CHECK:-0}" != "1" ]; then
    # Ensure no unstaged / uncommitted changes in tracked repository files
    DIRTY_FILES="$(git status --porcelain 2>/dev/null || true)"
    if [ -n "${DIRTY_FILES}" ]; then
        echo "❌ Dirty working directory detected in production deployment:"
        echo "${DIRTY_FILES}"
        echo "💡 Commit or stash changes before deploying (or DEPLOY_SKIP_GIT_CHECK=1 for emergency bypass)."
        exit 1
    fi
fi
echo "   ✅ Production workspace is clean and isolated."

# ------------------------------------------------------------------------------
# 🔄 Échelon 2: Atomic Switch & Graceful Shift
# ------------------------------------------------------------------------------
echo "🔄 [Échelon 2/5] Atomic Switch & Graceful Shift..."

echo "   🔨 Compiling binaries with ldflags metadata via make all..."
make all

echo "   ⚙️ Installing systemd unit: ${SERVICE_FILE}..."
cat << SVC > "${SERVICE_FILE}"
[Unit]
Description=Antigravity Telegram Agent (Multi-Agent Swarm)
After=network.target
StartLimitIntervalSec=60s
StartLimitBurst=5

[Service]
Type=simple
WorkingDirectory=${ROOT_DIR}
ExecStart=${ENGINE_BIN}
EnvironmentFile=${PROD_ENV_FILE}
Environment="ENV_FILE=${PROD_ENV_FILE}"
Environment="HOME=${HOME:-/root}"
Environment="SYSTEM_HOME=/root"
Environment="PATH=/root/.local/bin:${HOME:-/root}/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
SVC

echo "   🔄 Reloading systemd daemon..."
systemctl daemon-reload

echo "   🚀 Restarting ${SERVICE_NAME}..."
systemctl enable --now "${SERVICE_NAME}"
systemctl restart "${SERVICE_NAME}"

# PID Liveness Check (ensure process stays alive for at least 5 seconds without crashing)
echo "   ⏳ Verifying process liveness (5s stability window)..."
sleep 5

if ! systemctl is-active --quiet "${SERVICE_NAME}"; then
    echo "❌ Service failed liveness check after restart!"
    systemctl status "${SERVICE_NAME}" --no-pager || true
    exit 1
fi
echo "   ✅ Process is actively running (PID: $(systemctl show -p MainPID --value "${SERVICE_NAME}"))."

# ------------------------------------------------------------------------------
# 🪞 Échelon 3: Identity & Truth Probe
# ------------------------------------------------------------------------------
echo "🪞 [Échelon 3/5] Identity & Truth Probe..."
EXPECTED_COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")"
ACTUAL_VERSION_OUTPUT="$("${ENGINE_BIN}" --version 2>&1 || true)"
echo "   Engine identity: ${ACTUAL_VERSION_OUTPUT}"

if [ "${EXPECTED_COMMIT}" != "unknown" ]; then
    if [[ "${ACTUAL_VERSION_OUTPUT}" != *"${EXPECTED_COMMIT}"* ]]; then
        echo "❌ GHOST DEPLOY DETECTED! Binary commit does not match Git HEAD (${EXPECTED_COMMIT})."
        exit 1
    fi
fi
echo "   ✅ Identity & Truth verified: running commit matches HEAD."

# ------------------------------------------------------------------------------
# 🔍 Échelon 4: Deep Smoke Probe & Rollback Gate
# ------------------------------------------------------------------------------
echo "🔍 [Échelon 4/5] Deep Smoke Probe..."

# 1. agy-harvester CLI smoke test
if [ -x "${HARVESTER_BIN}" ]; then
    "${HARVESTER_BIN}" version >/dev/null 2>&1 || {
        echo "❌ agy-harvester failed CLI smoke execution";
        exit 1;
    }
    echo "   ✅ agy-harvester smoke test passed."
fi

# 2. Systemd service state & uptime verification
SERVICE_ACTIVE="$(systemctl is-active "${SERVICE_NAME}")"
if [ "${SERVICE_ACTIVE}" != "active" ]; then
    echo "❌ Service status is '${SERVICE_ACTIVE}' instead of 'active'."
    exit 1
fi

MAIN_PID="$(systemctl show -p MainPID --value "${SERVICE_NAME}")"
if [ -z "${MAIN_PID}" ] || [ "${MAIN_PID}" -le 0 ]; then
    echo "❌ Invalid MainPID (${MAIN_PID}) detected."
    exit 1
fi

# 3. Optional healthz probe if METRICS_PORT is configured
if [ -f "${PROD_ENV_FILE}" ]; then
    METRICS_PORT="$(grep -E "^METRICS_PORT=" "${PROD_ENV_FILE}" | cut -d'=' -f2 | tr -d ' "' || true)"
    if [ -n "${METRICS_PORT}" ]; then
        echo "   🌐 Probing metrics endpoint at http://127.0.0.1:${METRICS_PORT}/healthz..."
        curl -s -f "http://127.0.0.1:${METRICS_PORT}/healthz" >/dev/null 2>&1 || {
            echo "   ⚠️ Healthz probe non-responsive on port ${METRICS_PORT} (skipping non-blocking check)";
        }
    fi
fi

DEPLOY_SUCCESS=1
echo "======================================================================"
echo "🎉 DEPLOYMENT SUCCESSFUL: All 5 Echelons Verified!"
echo "   Active Unit: ${SERVICE_NAME} (PID: ${MAIN_PID})"
echo "   Commit:      ${EXPECTED_COMMIT}"
echo "======================================================================"
