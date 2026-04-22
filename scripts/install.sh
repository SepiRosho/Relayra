#!/usr/bin/env bash
# Relayra Installer
# Supports both online (download) and offline (bundled binary) installation.
# Target: Ubuntu 24 (linux/amd64)
set -euo pipefail

# ─── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${BLUE}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*"; }
step()  { echo -e "\n${BOLD}── $* ──${NC}"; }

# ─── Root check ───────────────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (sudo ./install.sh)"
    exit 1
fi

INSTALL_DIR="/opt/relayra"
BIN_LINK="/usr/local/bin/relayra"
LOG_DIR="/opt/relayra/logs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYSTEMD_UNIT="/etc/systemd/system/relayra.service"

# ─── Uninstall mode ───────────────────────────────────────────────────────────
if [[ "${1:-}" == "--uninstall" || "${1:-}" == "uninstall" ]]; then
    echo -e "${BOLD}"
    echo "  Relayra Uninstaller"
    echo -e "${NC}"
    echo "  This will remove Relayra and its data from Redis."
    echo "  Redis server itself will be kept intact."
    echo ""
    read -rp "  Continue? [y/N] " confirm
    if [[ "${confirm}" != [yY] ]]; then
        info "Uninstall cancelled."
        exit 0
    fi
    echo ""

    # Stop and disable service
    if systemctl is-active relayra &>/dev/null; then
        info "Stopping Relayra service..."
        systemctl stop relayra 2>/dev/null || true
        ok "Service stopped"
    fi
    if [[ -f "${SYSTEMD_UNIT}" ]]; then
        systemctl disable relayra 2>/dev/null || true
        rm -f "${SYSTEMD_UNIT}"
        systemctl daemon-reload 2>/dev/null || true
        ok "Systemd service removed"
    fi

    # Flush Relayra data from Redis
    if command -v redis-cli &>/dev/null && redis-cli ping &>/dev/null; then
        info "Flushing Relayra data from Redis..."
        RELAYRA_KEYS=$(redis-cli KEYS "relayra:*" 2>/dev/null | wc -l)
        if [[ "${RELAYRA_KEYS}" -gt 0 ]]; then
            redis-cli KEYS "relayra:*" | xargs -r redis-cli DEL >/dev/null 2>&1 || true
            ok "Deleted ${RELAYRA_KEYS} Relayra keys from Redis"
        else
            ok "No Relayra keys found in Redis"
        fi
    else
        warn "Redis is not reachable. Relayra keys were NOT removed."
        warn "Run manually: redis-cli KEYS 'relayra:*' | xargs redis-cli DEL"
    fi

    # Remove symlink
    if [[ -L "${BIN_LINK}" ]]; then
        rm -f "${BIN_LINK}"
        ok "Removed ${BIN_LINK}"
    fi

    # Remove install directory (binary, config, logs, docs)
    if [[ -d "${INSTALL_DIR}" ]]; then
        rm -rf "${INSTALL_DIR}"
        ok "Removed ${INSTALL_DIR}"
    fi

    echo ""
    echo -e "${GREEN}${BOLD}Uninstall complete.${NC}"
    echo ""
    echo "  Kept intact:"
    echo "    - Redis server (only Relayra keys were removed)"
    echo "    - System packages"
    echo ""
    echo "  To also remove Redis (optional):"
    echo "    sudo apt remove --purge redis-server"
    echo ""
    exit 0
fi

echo -e "${BOLD}"
echo "  ╦═╗┌─┐┬  ┌─┐┬ ┬┬─┐┌─┐"
echo "  ╠╦╝├┤ │  ├─┤└┬┘├┬┘├─┤"
echo "  ╩╚═└─┘┴─┘┴ ┴ ┴ ┴└─┴ ┴"
echo -e "${NC}"
echo "  Restricted Server Relay System"
echo ""

# ─── Step 1: Install Redis ───────────────────────────────────────────────────
step "Step 1/4: Redis"

if command -v redis-server &>/dev/null; then
    REDIS_VER=$(redis-server --version | grep -oP 'v=\K[0-9.]+' || echo "unknown")
    ok "Redis already installed (v${REDIS_VER})"
else
    info "Installing Redis..."
    if apt-get update -qq && apt-get install -y -qq redis-server; then
        ok "Redis installed via apt"
    else
        warn "apt install failed. Trying alternative method..."
        if apt-get install -y -qq redis-tools redis-server 2>/dev/null; then
            ok "Redis installed (alternative)"
        else
            err "Could not install Redis automatically."
            echo ""
            echo "  Manual install instructions:"
            echo "    apt-get update && apt-get install -y redis-server"
            echo "    -- or --"
            echo "    Download from https://redis.io/download"
            echo ""
            echo "  After installing Redis, re-run this script."
            exit 1
        fi
    fi

    # Enable and start Redis
    systemctl enable redis-server 2>/dev/null || true
    systemctl start redis-server 2>/dev/null || true
fi

# Verify Redis is running
if redis-cli ping 2>/dev/null | grep -q PONG; then
    ok "Redis is running"
else
    warn "Redis is installed but not responding."
    warn "Start it with: systemctl start redis-server"
fi

# ─── Step 2: Create directories ──────────────────────────────────────────────
step "Step 2/4: Directories"

mkdir -p "${INSTALL_DIR}"
mkdir -p "${LOG_DIR}"
chmod 755 "${INSTALL_DIR}"
chmod 755 "${LOG_DIR}"
ok "Created ${INSTALL_DIR}"
ok "Created ${LOG_DIR}"

# ─── Step 3: Install binary ──────────────────────────────────────────────────
step "Step 3/4: Binary"

# Check if bundled binary exists (offline mode)
if [[ -f "${SCRIPT_DIR}/relayra" ]]; then
    info "Offline mode: using bundled binary"
    cp "${SCRIPT_DIR}/relayra" "${INSTALL_DIR}/relayra"
else
    err "Binary not found at ${SCRIPT_DIR}/relayra"
    echo ""
    echo "  This installer expects the relayra binary in the same directory."
    echo "  Make sure you extracted the full .tar.gz archive:"
    echo "    tar xzf relayra-*-linux-amd64.tar.gz"
    echo "    cd relayra-*/"
    echo "    sudo ./install.sh"
    exit 1
fi

chmod +x "${INSTALL_DIR}/relayra"

# Create symlink
ln -sf "${INSTALL_DIR}/relayra" "${BIN_LINK}"
ok "Installed binary to ${INSTALL_DIR}/relayra"
ok "Symlinked to ${BIN_LINK}"

# Verify
if relayra version &>/dev/null; then
    VER=$(relayra version 2>/dev/null | head -1)
    ok "Verified: ${VER}"
else
    warn "Binary installed but 'relayra version' did not succeed."
    warn "You can still run it directly: ${INSTALL_DIR}/relayra"
fi

# ─── Step 4: Copy docs ───────────────────────────────────────────────────────
step "Step 4/4: Documentation"

if [[ -f "${SCRIPT_DIR}/GUIDE.md" ]]; then
    cp "${SCRIPT_DIR}/GUIDE.md" "${INSTALL_DIR}/GUIDE.md"
    ok "Copied GUIDE.md to ${INSTALL_DIR}/"
else
    warn "GUIDE.md not found in archive"
fi

# ─── Done ─────────────────────────────────────────────────────────────────────
echo ""
echo -e "${GREEN}${BOLD}Installation complete!${NC}"
echo ""
echo "  Next steps:"
echo ""
echo "  1. Run the setup wizard:"
echo "     ${BOLD}relayra${NC}"
echo ""
echo "  2. Or start the service directly:"
echo "     ${BOLD}relayra run${NC}"
echo ""
echo "  3. Install as systemd service:"
echo "     ${BOLD}relayra service install${NC}"
echo "     ${BOLD}relayra service start${NC}"
echo ""
echo "  4. For Listener — generate pairing token:"
echo "     ${BOLD}relayra pair generate${NC}"
echo ""
echo "  5. For Sender — connect to Listener:"
echo "     ${BOLD}relayra pair connect <token>${NC}"
echo ""
echo "  Documentation: ${INSTALL_DIR}/GUIDE.md"
echo "  Logs:          ${LOG_DIR}/"
echo "  Config:        ${INSTALL_DIR}/.env (after setup)"
echo ""
