#!/usr/bin/env bash
# Relayra Webhook Test Script
# Run on the Listener server. Starts a temporary webhook receiver,
# submits a relay request with webhook_url, and waits for the callback.
#
# Usage:
#   bash test-webhook.sh                  # auto-detect peer + port
#   bash test-webhook.sh <peer_id>        # specify peer
set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

ENV_FILE="/opt/relayra/.env"

if [[ ! -f "${ENV_FILE}" ]]; then
    echo -e "${RED}Error: ${ENV_FILE} not found. Is Relayra installed?${NC}"
    exit 1
fi

PORT=$(grep -oP 'RELAYRA_LISTEN_PORT=\K[0-9]+' "${ENV_FILE}" 2>/dev/null || echo "")
PUBLIC_ADDR=$(grep -oP 'RELAYRA_PUBLIC_ADDR=\K[^\s]+' "${ENV_FILE}" 2>/dev/null || echo "")

if [[ -z "${PORT}" ]]; then
    echo -e "${RED}Error: RELAYRA_LISTEN_PORT not found in ${ENV_FILE}${NC}"
    exit 1
fi

BASE_URL="http://127.0.0.1:${PORT}"

# Pick a port for the webhook receiver (different from Relayra)
WEBHOOK_PORT=9999

# Check dependencies
for cmd in curl jq python3; do
    if ! command -v "${cmd}" &>/dev/null; then
        echo -e "${RED}Error: '${cmd}' is required but not found.${NC}"
        exit 1
    fi
done

# ─── Find peer ────────────────────────────────────────────────────────────────
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
        echo -e "${RED}No peers found. Pair a Sender first.${NC}"
        exit 1
    fi

    PEER_ID=$(echo "${PEERS_RESP}" | jq -r '.peers[0].id')
    PEER_NAME=$(echo "${PEERS_RESP}" | jq -r '.peers[0].name')
    echo -e "${GREEN}Using peer: ${PEER_NAME} (${PEER_ID})${NC}"
fi

# Build test URL (Sender relays a request to Listener's /health)
if [[ -n "${PUBLIC_ADDR}" ]]; then
    TEST_URL="http://${PUBLIC_ADDR}:${PORT}/health"
else
    TEST_URL="http://127.0.0.1:${PORT}/health"
fi

# Webhook URL — the Listener itself will receive the webhook callback
WEBHOOK_URL="http://127.0.0.1:${WEBHOOK_PORT}/webhook"
WEBHOOK_LOG=$(mktemp /tmp/relayra-webhook-XXXXXX.json)

echo ""
echo -e "${BOLD}── Relayra Webhook Test ──${NC}"
echo -e "  Relayra:      ${BASE_URL}"
echo -e "  Peer ID:      ${PEER_ID}"
echo -e "  Test URL:     ${TEST_URL}"
echo -e "  Webhook URL:  ${WEBHOOK_URL}"
echo -e "  Webhook log:  ${WEBHOOK_LOG}"
echo ""

# ─── Step 1: Start webhook receiver ──────────────────────────────────────────
echo -e "${BLUE}[1/4] Starting webhook receiver on port ${WEBHOOK_PORT}...${NC}"

# Python one-liner HTTP server that logs POST body to file
python3 -c "
import http.server, json, sys, os

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get('Content-Length', 0))
        body = self.rfile.read(length)
        with open('${WEBHOOK_LOG}', 'wb') as f:
            f.write(body)
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(b'{\"status\":\"received\"}')
        # Signal main script via marker file
        open('${WEBHOOK_LOG}.done', 'w').close()
    def log_message(self, format, *args):
        pass  # suppress access logs

server = http.server.HTTPServer(('127.0.0.1', ${WEBHOOK_PORT}), Handler)
server.timeout = 90
try:
    while not os.path.exists('${WEBHOOK_LOG}.stop'):
        server.handle_request()
except:
    pass
" &
WEBHOOK_PID=$!

# Give it a moment to bind
sleep 1

# Verify it's running
if ! kill -0 "${WEBHOOK_PID}" 2>/dev/null; then
    echo -e "${RED}Failed to start webhook receiver on port ${WEBHOOK_PORT}.${NC}"
    echo -e "${RED}Port may be in use. Try: lsof -i :${WEBHOOK_PORT}${NC}"
    exit 1
fi

echo -e "${GREEN}  Webhook receiver running (PID ${WEBHOOK_PID})${NC}"

# Cleanup on exit
cleanup() {
    touch "${WEBHOOK_LOG}.stop" 2>/dev/null || true
    kill "${WEBHOOK_PID}" 2>/dev/null || true
    wait "${WEBHOOK_PID}" 2>/dev/null || true
    rm -f "${WEBHOOK_LOG}.stop" "${WEBHOOK_LOG}.done" 2>/dev/null || true
}
trap cleanup EXIT

# ─── Step 2: Submit relay request with webhook ───────────────────────────────
echo ""
echo -e "${BLUE}[2/4] Submitting relay request with webhook...${NC}"

RELAY_RESP=$(curl -s -X POST "${BASE_URL}/api/v1/relay" \
    -H "Content-Type: application/json" \
    -d "{
        \"destination_peer_id\": \"${PEER_ID}\",
        \"webhook_url\": \"${WEBHOOK_URL}\",
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
echo -e "  Status:     ${STATUS}"
echo -e "  Webhook:    ${WEBHOOK_URL}${NC}"

# ─── Step 3: Wait for webhook callback ───────────────────────────────────────
echo ""
echo -e "${BLUE}[3/4] Waiting for webhook delivery (max 90s)...${NC}"

ELAPSED=0
MAX_WAIT=90

while [[ ${ELAPSED} -lt ${MAX_WAIT} ]]; do
    if [[ -f "${WEBHOOK_LOG}.done" ]]; then
        echo -e "${GREEN}  Webhook received after ~${ELAPSED}s!${NC}"
        break
    fi
    sleep 3
    ELAPSED=$((ELAPSED + 3))
    if (( ELAPSED % 9 == 0 )); then
        # Check request status periodically
        RESULT_STATUS=$(curl -s "${BASE_URL}/api/v1/result/${REQUEST_ID}" | jq -r '.status // empty')
        echo -e "  ${ELAPSED}s elapsed, request status: ${RESULT_STATUS:-unknown}"
    fi
done

# ─── Step 4: Show results ────────────────────────────────────────────────────
echo ""
echo -e "${BLUE}[4/4] Webhook payload:${NC}"
echo ""

if [[ -f "${WEBHOOK_LOG}.done" && -s "${WEBHOOK_LOG}" ]]; then
    echo -e "${GREEN}${BOLD}Webhook delivered successfully!${NC}"
    echo ""
    jq . "${WEBHOOK_LOG}"
    echo ""

    WH_STATUS=$(jq -r '.result.status_code // 0' "${WEBHOOK_LOG}")
    WH_ERROR=$(jq -r '.result.error // empty' "${WEBHOOK_LOG}")
    WH_DURATION=$(jq -r '.result.duration_ms // 0' "${WEBHOOK_LOG}")
    WH_BODY_LEN=$(jq -r '.result.body | length // 0' "${WEBHOOK_LOG}")

    echo -e "  ${GREEN}Status Code:   ${WH_STATUS}${NC}"
    echo -e "  ${GREEN}Duration:      ${WH_DURATION}ms${NC}"
    echo -e "  ${GREEN}Body Size:     ${WH_BODY_LEN} bytes${NC}"
    if [[ -n "${WH_ERROR}" ]]; then
        echo -e "  ${YELLOW}Error:         ${WH_ERROR}${NC}"
    fi
    echo ""
    echo -e "${GREEN}${BOLD}Full pipeline verified: Relay → Queue → Sender → Execute → Result → Webhook ✓${NC}"
else
    echo -e "${RED}Webhook was NOT received within ${MAX_WAIT}s.${NC}"
    echo ""
    echo -e "Check the result directly:"
    echo -e "  curl -s ${BASE_URL}/api/v1/result/${REQUEST_ID} | jq ."
    echo ""
    echo -e "Possible causes:"
    echo -e "  1. Sender hasn't executed the request yet (URL unreachable, timing out)"
    echo -e "  2. Result hasn't been polled back to Listener yet"
    echo -e "  3. Webhook delivery failed (check Listener logs)"
fi

# Cleanup temp file
rm -f "${WEBHOOK_LOG}" 2>/dev/null || true
