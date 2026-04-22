#!/usr/bin/env bash
# Relayra Relay Test Script
# Run this on the Listener server to test the full relay pipeline.
#
# Usage:
#   bash test-relay.sh                          # auto-detect peer, use default test URL
#   bash test-relay.sh <peer_id>                # specify peer
#   bash test-relay.sh <peer_id> <url>          # specify peer and URL
#   bash test-relay.sh "" <url>                 # auto-detect peer, custom URL
#
# The test URL must be reachable from the SENDER server (not the Listener).
# If the Sender is on a restricted network, use a URL it can access locally.
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

ENV_FILE="/opt/relayra/.env"

# ─── Read port from config ────────────────────────────────────────────────────
if [[ ! -f "${ENV_FILE}" ]]; then
    echo -e "${RED}Error: ${ENV_FILE} not found. Is Relayra installed?${NC}"
    exit 1
fi

PORT=$(grep -oP 'RELAYRA_LISTEN_PORT=\K[0-9]+' "${ENV_FILE}" 2>/dev/null || echo "")

if [[ -z "${PORT}" ]]; then
    echo -e "${RED}Error: RELAYRA_LISTEN_PORT not found in ${ENV_FILE}${NC}"
    exit 1
fi

BASE_URL="http://127.0.0.1:${PORT}"

# ─── Check jq is available ────────────────────────────────────────────────────
if ! command -v jq &>/dev/null; then
    echo -e "${YELLOW}jq not found. Installing...${NC}"
    apt-get install -y -qq jq 2>/dev/null || {
        echo -e "${RED}Error: jq is required. Install with: apt-get install jq${NC}"
        exit 1
    }
fi

# ─── Find a peer to relay to ──────────────────────────────────────────────────
PEER_ID="${1:-}"

if [[ -z "${PEER_ID}" ]]; then
    echo -e "${BLUE}No peer ID provided, fetching peer list...${NC}"
    PEERS_RESP=$(curl -sf "${BASE_URL}/api/v1/peers" 2>/dev/null || echo "")
    if [[ -z "${PEERS_RESP}" ]]; then
        echo -e "${RED}Error: Could not reach Relayra at ${BASE_URL}. Is it running?${NC}"
        exit 1
    fi

    PEER_COUNT=$(echo "${PEERS_RESP}" | jq -r '.peers | length' 2>/dev/null || echo "0")
    if [[ "${PEER_COUNT}" -eq 0 ]]; then
        echo -e "${RED}No peers found. Pair a Sender first with 'relayra pair generate'.${NC}"
        exit 1
    fi

    PEER_ID=$(echo "${PEERS_RESP}" | jq -r '.peers[0].id')
    PEER_NAME=$(echo "${PEERS_RESP}" | jq -r '.peers[0].name')
    echo -e "${GREEN}Using peer: ${PEER_NAME} (${PEER_ID})${NC}"
fi

# ─── Choose test URL ──────────────────────────────────────────────────────────
# Default: request the Listener's own /health endpoint through the relay.
# This always works because the Sender can reach the Listener (via proxy).
# The response proves the full relay pipeline: Listener → queue → Sender → execute → result.
LISTENER_PUBLIC_ADDR=$(grep -oP 'RELAYRA_PUBLIC_ADDR=\K[^\s]+' "${ENV_FILE}" 2>/dev/null || echo "")
if [[ -n "${LISTENER_PUBLIC_ADDR}" ]]; then
    DEFAULT_URL="http://${LISTENER_PUBLIC_ADDR}:${PORT}/health"
else
    DEFAULT_URL="http://127.0.0.1:${PORT}/health"
fi
TEST_URL="${2:-${DEFAULT_URL}}"

echo ""
echo -e "${BOLD}── Relayra Relay Test ──${NC}"
echo -e "  Server:   ${BASE_URL}"
echo -e "  Peer ID:  ${PEER_ID}"
echo -e "  Test URL: ${TEST_URL}"
echo ""
if [[ "${TEST_URL}" == *"127.0.0.1"* ]]; then
    echo -e "${YELLOW}  Note: Using localhost URL — the Sender will execute this against its own loopback.${NC}"
    echo -e "${YELLOW}  If nothing is listening there, expect status_code=0 with a connection error.${NC}"
    echo -e "${YELLOW}  For a real test, use a URL reachable from the Sender's network.${NC}"
    echo ""
fi

# ─── Step 1: Submit relay request ─────────────────────────────────────────────
echo -e "${BLUE}[1/3] Submitting relay request...${NC}"

RELAY_RESP=$(curl -s -X POST "${BASE_URL}/api/v1/relay" \
    -H "Content-Type: application/json" \
    -d "{
        \"destination_peer_id\": \"${PEER_ID}\",
        \"request\": {
            \"url\": \"${TEST_URL}\",
            \"method\": \"GET\"
        }
    }")

REQUEST_ID=$(echo "${RELAY_RESP}" | jq -r '.request_id // empty')
STATUS=$(echo "${RELAY_RESP}" | jq -r '.status // empty')

if [[ -z "${REQUEST_ID}" ]]; then
    echo -e "${RED}Failed to submit request:${NC}"
    echo "${RELAY_RESP}" | jq . 2>/dev/null || echo "${RELAY_RESP}"
    exit 1
fi

echo -e "${GREEN}  Request ID: ${REQUEST_ID}"
echo -e "  Status:     ${STATUS}${NC}"

# ─── Step 2: Wait for Sender to pick up and execute ──────────────────────────
# Timing: poll_interval (5s) + execution time + poll_interval (5s) for result return.
# Default request timeout is 30s, so worst case is ~45s for unreachable URLs.
echo ""
echo -e "${BLUE}[2/3] Waiting for Sender to execute (polling every 3s, max 60s)...${NC}"

ATTEMPTS=0
MAX_ATTEMPTS=20
RESULT=""

while [[ ${ATTEMPTS} -lt ${MAX_ATTEMPTS} ]]; do
    ATTEMPTS=$((ATTEMPTS + 1))
    sleep 3

    RESULT_RESP=$(curl -s "${BASE_URL}/api/v1/result/${REQUEST_ID}")
    RESULT_STATUS=$(echo "${RESULT_RESP}" | jq -r '.status // empty')

    if [[ "${RESULT_STATUS}" == "completed" ]]; then
        RESULT="${RESULT_RESP}"
        echo -e "${GREEN}  Result received after ~$((ATTEMPTS * 3))s${NC}"
        break
    fi

    echo -e "  Attempt ${ATTEMPTS}/${MAX_ATTEMPTS}: status=${RESULT_STATUS:-unknown}"
done

# ─── Step 3: Show result ─────────────────────────────────────────────────────
echo ""
echo -e "${BLUE}[3/3] Result:${NC}"
echo ""

if [[ -n "${RESULT}" ]]; then
    echo "${RESULT}" | jq .
    echo ""

    RESULT_ERROR=$(echo "${RESULT}" | jq -r '.result.error // empty')
    RESULT_STATUS_CODE=$(echo "${RESULT}" | jq -r '.result.status_code // 0')
    RESULT_DURATION=$(echo "${RESULT}" | jq -r '.result.duration_ms // 0')

    if [[ -n "${RESULT_ERROR}" ]]; then
        echo -e "${YELLOW}${BOLD}Request executed but returned an error:${NC}"
        echo -e "${YELLOW}  Error:       ${RESULT_ERROR}${NC}"
        echo -e "${YELLOW}  Duration:    ${RESULT_DURATION}ms${NC}"
        echo ""
        echo -e "${GREEN}The relay pipeline IS working!${NC} The error came from the Sender's network."
        echo -e "This means the request was relayed correctly but the target URL"
        echo -e "was not reachable from the Sender's network."
        echo ""
        echo -e "Try a URL the Sender CAN reach:"
        echo -e "  bash $0 \"\" \"http://<sender-reachable-url>\""
    elif [[ "${RESULT_STATUS_CODE}" -gt 0 ]]; then
        echo -e "${GREEN}${BOLD}Success! Relay pipeline working.${NC}"
        echo -e "  Status Code: ${RESULT_STATUS_CODE}"
        echo -e "  Duration:    ${RESULT_DURATION}ms"

        # Try to extract origin from httpbin-style responses
        ORIGIN=$(echo "${RESULT}" | jq -r '.result.body' 2>/dev/null | jq -r '.origin // empty' 2>/dev/null || echo "")
        if [[ -n "${ORIGIN}" && "${ORIGIN}" != "null" ]]; then
            echo -e "  Origin IP:   ${ORIGIN} (Sender's IP — proves relay worked!)"
        fi
    fi
else
    echo -e "${RED}Timed out waiting for result.${NC}"
    echo ""
    echo -e "Possible causes:"
    echo -e "  1. Sender is not running or not polling"
    echo -e "  2. Target URL is unreachable and execution is still timing out"
    echo -e "     (default timeout: 30s — try waiting a bit longer)"
    echo -e "  3. Proxy connection issue between Sender and Listener"
    echo ""
    echo -e "Check manually:"
    echo -e "  curl -s ${BASE_URL}/api/v1/result/${REQUEST_ID} | jq ."
    echo ""
    echo -e "Check Sender logs:"
    echo -e "  tail -50 /opt/relayra/logs/*.log | grep '${REQUEST_ID:0:12}'"
fi
