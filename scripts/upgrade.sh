#!/usr/bin/env bash
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

if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (sudo ./upgrade.sh)"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="/opt/relayra"
BIN_PATH="${INSTALL_DIR}/relayra"
BIN_LINK="/usr/local/bin/relayra"
BACKUP_DIR="${INSTALL_DIR}/backups"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
SERVICE_NAME="relayra"
WAS_ACTIVE=0

if [[ ! -f "${SCRIPT_DIR}/relayra" ]]; then
    err "Binary not found at ${SCRIPT_DIR}/relayra"
    err "Extract the release archive first, then run this script from that directory."
    exit 1
fi

if [[ ! -d "${INSTALL_DIR}" ]]; then
    err "${INSTALL_DIR} does not exist. Use install.sh for first-time installation."
    exit 1
fi

mkdir -p "${BACKUP_DIR}"

if systemctl is-active "${SERVICE_NAME}" &>/dev/null; then
    WAS_ACTIVE=1
    info "Stopping ${SERVICE_NAME} service..."
    systemctl stop "${SERVICE_NAME}"
    ok "Service stopped"
else
    info "${SERVICE_NAME} service is not running"
fi

if [[ -f "${INSTALL_DIR}/.env" ]]; then
    cp "${INSTALL_DIR}/.env" "${BACKUP_DIR}/.env.${TIMESTAMP}.bak"
    ok "Backed up .env to ${BACKUP_DIR}/.env.${TIMESTAMP}.bak"
else
    warn "No existing .env found at ${INSTALL_DIR}/.env"
fi

if [[ -f "${INSTALL_DIR}/relayra.db" ]]; then
    cp "${INSTALL_DIR}/relayra.db" "${BACKUP_DIR}/relayra.db.${TIMESTAMP}.bak"
    ok "Backed up SQLite DB to ${BACKUP_DIR}/relayra.db.${TIMESTAMP}.bak"
fi

if [[ -f "${BIN_PATH}" ]]; then
    cp "${BIN_PATH}" "${BACKUP_DIR}/relayra.${TIMESTAMP}.bak"
    ok "Backed up current binary to ${BACKUP_DIR}/relayra.${TIMESTAMP}.bak"
fi

install -m 0755 "${SCRIPT_DIR}/relayra" "${BIN_PATH}"
ln -sf "${BIN_PATH}" "${BIN_LINK}"
ok "Installed upgraded binary to ${BIN_PATH}"

if [[ -f "${SCRIPT_DIR}/README.md" ]]; then
    cp "${SCRIPT_DIR}/README.md" "${INSTALL_DIR}/README.md"
fi
if [[ -f "${SCRIPT_DIR}/GUIDE.md" ]]; then
    cp "${SCRIPT_DIR}/GUIDE.md" "${INSTALL_DIR}/GUIDE.md"
fi

info "Installed version:"
"${BIN_PATH}" version || true

if systemctl list-unit-files | grep -q "^${SERVICE_NAME}\.service"; then
    if [[ ${WAS_ACTIVE} -eq 1 ]]; then
        info "Starting ${SERVICE_NAME} service..."
        systemctl start "${SERVICE_NAME}"
        ok "Service started"
    else
        info "Service unit exists but was not running; leaving it stopped"
    fi

    info "Current service status:"
    systemctl --no-pager --full status "${SERVICE_NAME}" || true
else
    warn "Systemd unit ${SERVICE_NAME}.service was not found; binary upgrade completed without service restart"
fi

printf "\n%b\n" "${GREEN}${BOLD}Upgrade complete.${NC}"
printf "%s\n" "  Binary:  ${BIN_PATH}"
printf "%s\n" "  Backups: ${BACKUP_DIR}"
