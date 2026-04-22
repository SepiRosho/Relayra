#!/usr/bin/env bash
# Relayra Installer
# Supports both online (download) and offline (bundled binary) installation.
# Target: Ubuntu 24 (linux/amd64)
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

info()  { printf "%b\n" "${BLUE}[INFO]${NC}  $*"; }
ok()    { printf "%b\n" "${GREEN}[OK]${NC}    $*"; }
warn()  { printf "%b\n" "${YELLOW}[WARN]${NC}  $*"; }
err()   { printf "%b\n" "${RED}[ERROR]${NC} $*"; }
step()  { printf "\n%b\n" "${BOLD}-- $* --${NC}"; }

if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (sudo ./install.sh)"
    exit 1
fi

INSTALL_DIR="/opt/relayra"
BIN_LINK="/usr/local/bin/relayra"
LOG_DIR="/opt/relayra/logs"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SYSTEMD_UNIT="/etc/systemd/system/relayra.service"
REDIS_READY=0
REDIS_SKIPPED=0

prompt_continue_without_redis() {
    printf "\n"
    warn "Redis installation is unavailable."
    printf "%b\n" "  Relayra can still be installed and configured later with ${BOLD}SQLite${NC}."
    printf "%b" "  Continue installation without Redis? [y/N] "
    read -r continue_without_redis
    case "${continue_without_redis:-}" in
        y|Y|yes|YES)
            REDIS_SKIPPED=1
            warn "Continuing without Redis. Choose SQLite during setup, or install Redis manually later."
            return 0
            ;;
        *)
            printf "\n"
            printf "%s\n" "  Manual Redis install instructions:"
            printf "%s\n" "    apt-get update && apt-get install -y redis-server"
            printf "%s\n" "    -- or --"
            printf "%s\n" "    Download from https://redis.io/download"
            printf "\n"
            printf "%s\n" "  After installing Redis, re-run this script."
            exit 1
            ;;
    esac
}

if [[ "${1:-}" == "--uninstall" || "${1:-}" == "uninstall" ]]; then
    printf "%b\n" "${BOLD}"
    printf "%s\n" "  Relayra Uninstaller"
    printf "%b\n" "${NC}"
    printf "%s\n" "  This will remove Relayra and its data from Redis."
    printf "%s\n" "  Redis server itself will be kept intact."
    printf "\n"
    read -rp "  Continue? [y/N] " confirm
    if [[ "${confirm}" != [yY] ]]; then
        info "Uninstall cancelled."
        exit 0
    fi
    printf "\n"

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

    if [[ -L "${BIN_LINK}" ]]; then
        rm -f "${BIN_LINK}"
        ok "Removed ${BIN_LINK}"
    fi

    if [[ -d "${INSTALL_DIR}" ]]; then
        rm -rf "${INSTALL_DIR}"
        ok "Removed ${INSTALL_DIR}"
    fi

    printf "\n%b\n\n" "${GREEN}${BOLD}Uninstall complete.${NC}"
    printf "%s\n" "  Kept intact:"
    printf "%s\n" "    - Redis server (only Relayra keys were removed)"
    printf "%s\n" "    - System packages"
    printf "\n"
    printf "%s\n" "  To also remove Redis (optional):"
    printf "%s\n" "    sudo apt remove --purge redis-server"
    printf "\n"
    exit 0
fi

printf "%b\n" "${BOLD}"
printf "%s\n" "  Relayra"
printf "%b\n" "${NC}"
printf "%s\n" "  Restricted Server Relay System"
printf "\n"

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
            prompt_continue_without_redis
        fi
    fi

    if [[ ${REDIS_SKIPPED} -eq 0 ]]; then
        systemctl enable redis-server 2>/dev/null || true
        systemctl start redis-server 2>/dev/null || true
    fi
fi

if [[ ${REDIS_SKIPPED} -eq 0 ]]; then
    if command -v redis-cli &>/dev/null && redis-cli ping 2>/dev/null | grep -q PONG; then
        REDIS_READY=1
        ok "Redis is running"
    else
        warn "Redis is installed but not responding."
        warn "You can still continue and choose SQLite during setup."
        warn "To use Redis later, start it with: systemctl start redis-server"
    fi
fi

step "Step 2/4: Directories"

mkdir -p "${INSTALL_DIR}"
mkdir -p "${LOG_DIR}"
chmod 755 "${INSTALL_DIR}"
chmod 755 "${LOG_DIR}"
ok "Created ${INSTALL_DIR}"
ok "Created ${LOG_DIR}"

step "Step 3/4: Binary"

if [[ -f "${SCRIPT_DIR}/relayra" ]]; then
    info "Offline mode: using bundled binary"
    cp "${SCRIPT_DIR}/relayra" "${INSTALL_DIR}/relayra"
else
    err "Binary not found at ${SCRIPT_DIR}/relayra"
    printf "\n"
    printf "%s\n" "  This installer expects the relayra binary in the same directory."
    printf "%s\n" "  Make sure you extracted the full .tar.gz archive:"
    printf "%s\n" "    tar xzf relayra-*-linux-amd64.tar.gz"
    printf "%s\n" "    cd relayra-*/"
    printf "%s\n" "    sudo ./install.sh"
    exit 1
fi

chmod +x "${INSTALL_DIR}/relayra"
ln -sf "${INSTALL_DIR}/relayra" "${BIN_LINK}"
ok "Installed binary to ${INSTALL_DIR}/relayra"
ok "Symlinked to ${BIN_LINK}"

if relayra version &>/dev/null; then
    VER=$(relayra version 2>/dev/null | head -1)
    ok "Verified: ${VER}"
else
    warn "Binary installed but 'relayra version' did not succeed."
    warn "You can still run it directly: ${INSTALL_DIR}/relayra"
fi

step "Step 4/4: Documentation"

if [[ -f "${SCRIPT_DIR}/README.md" ]]; then
    cp "${SCRIPT_DIR}/README.md" "${INSTALL_DIR}/README.md"
    ok "Copied README.md to ${INSTALL_DIR}/"
fi

if [[ -f "${SCRIPT_DIR}/GUIDE.md" ]]; then
    cp "${SCRIPT_DIR}/GUIDE.md" "${INSTALL_DIR}/GUIDE.md"
    ok "Copied GUIDE.md to ${INSTALL_DIR}/"
else
    warn "GUIDE.md not found in archive"
fi

printf "\n%b\n\n" "${GREEN}${BOLD}Installation complete!${NC}"
printf "%s\n" "  Next steps:"
printf "\n"
printf "%s\n" "  1. Run the setup wizard:"
printf "%b\n" "     ${BOLD}relayra${NC}"
printf "\n"
printf "%s\n" "  2. Or start the service directly:"
printf "%b\n" "     ${BOLD}relayra run${NC}"
printf "\n"
printf "%s\n" "  3. Install as systemd service:"
printf "%b\n" "     ${BOLD}relayra service install${NC}"
printf "%b\n" "     ${BOLD}relayra service start${NC}"
printf "\n"
printf "%s\n" "  4. For Listener - generate pairing token:"
printf "%b\n" "     ${BOLD}relayra pair generate${NC}"
printf "\n"
printf "%s\n" "  5. For Sender - connect to Listener:"
printf "%b\n" "     ${BOLD}relayra pair connect <token>${NC}"
printf "\n"
if [[ ${REDIS_SKIPPED} -eq 1 ]]; then
    printf "%b\n" "  ${YELLOW}Redis was skipped during install. Choose SQLite in setup, or install Redis later.${NC}"
    printf "\n"
elif [[ ${REDIS_READY} -eq 0 ]]; then
    printf "%b\n" "  ${YELLOW}Redis is not currently responding. You can still continue with SQLite in setup.${NC}"
    printf "\n"
fi
printf "%s\n" "  Documentation: ${INSTALL_DIR}/GUIDE.md"
printf "%s\n" "  README:        ${INSTALL_DIR}/README.md"
printf "%s\n" "  Logs:          ${LOG_DIR}/"
printf "%s\n" "  Config:        ${INSTALL_DIR}/.env (after setup)"
printf "\n"
