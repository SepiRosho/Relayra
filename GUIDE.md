# Relayra — Quick Reference Guide

## What is Relayra?

Relayra bridges communication between two servers:
- **Listener** (Server A) — unrestricted internet access, exposes HTTP API
- **Sender** (Server B) — restricted/firewalled, connects to Listener through proxies

The Sender polls the Listener for queued HTTP requests, executes them locally, and returns results. All payloads are encrypted with AES-256-GCM.

## Installation

```bash
tar xzf relayra-*-linux-amd64.tar.gz
cd relayra-*/
chmod +x install.sh
sudo ./install.sh
```

The installer will:
1. Install Redis (if not present)
2. Create `/opt/relayra/` and `/opt/relayra/logs/`
3. Copy the binary and symlink to `/usr/local/bin/relayra`

## First-Time Setup

Run `relayra` with no arguments to launch the setup wizard:

```bash
relayra
```

The wizard will ask for:
- **Role**: Listener or Sender
- **Listen address/port**: Network binding
- **Public address**: External IP/domain for pairing tokens (Listener only)
- **Instance name**: Human-readable identifier (up to 32 chars)
- **Redis connection**: Address, port, password
- **Log level**: debug, info, warn, error

Configuration is saved to `/opt/relayra/.env`.

## Pairing Servers

### On the Listener:

```bash
relayra pair generate --expires 1h
```

This prints a one-time token. Copy it to the Sender server.

### On the Sender:

First, add at least one proxy:

```bash
relayra proxy add socks5://proxy.example.com:1080
# With authentication:
relayra proxy add socks5://user:password@proxy.example.com:1080
# or
relayra proxy add http://user:password@proxy.example.com:8080
```

Then connect:

```bash
relayra pair connect <token>
```

## Running the Service

### Foreground (for testing):

```bash
relayra run
```

### As a systemd service:

```bash
relayra service install
relayra service start
relayra service status
```

## Sending Relay Requests

Submit a request to the Listener's API:

```bash
curl -X POST http://listener-ip:port/api/v1/relay \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api-token>" \
  -d '{
    "destination_peer_id": "<sender-peer-id>",
    "request": {
      "url": "http://localhost:8080/api/data",
      "method": "GET",
      "headers": {"Authorization": "Bearer token123"}
    },
    "webhook_url": "http://your-server.com/callback"
  }'
```

> **Note:** The `Authorization: Bearer <token>` header is required when API tokens are configured. If no tokens exist, endpoints are open.
```

Response:
```json
{"request_id": "abc123def456"}
```

## Getting Results

### Poll for result:

```bash
curl http://listener-ip:port/api/v1/result/abc123def456
```

### Or use webhooks:

If `webhook_url` was specified, the result is POSTed to that URL automatically when ready. Retries 3 times with exponential backoff on failure.

## CLI Commands

| Command | Description |
|---------|-------------|
| `relayra` | Open TUI panel (or setup wizard on first run) |
| `relayra setup` | Re-run configuration wizard |
| `relayra run` | Run service in foreground |
| `relayra service install` | Install systemd service |
| `relayra service start` | Start the service |
| `relayra service stop` | Stop the service |
| `relayra service restart` | Restart the service |
| `relayra service status` | Check service status |
| `relayra pair generate` | (Listener) Generate pairing token |
| `relayra pair connect <token>` | (Sender) Connect to a Listener |
| `relayra peers` | List connected peers |
| `relayra proxy add <url>` | (Sender) Add a proxy |
| `relayra proxy remove <url>` | (Sender) Remove a proxy |
| `relayra proxy list` | (Sender) List proxies with health |
| `relayra proxy test [url]` | (Sender) Test proxy connectivity |
| `relayra proxy reset-cooldown` | (Sender) Reset all proxy failure cooldowns |
| `relayra token create <name>` | (Listener) Create an API token |
| `relayra token list` | (Listener) List all API tokens |
| `relayra token revoke <id>` | (Listener) Revoke an API token |
| `relayra logs` | View recent logs |
| `relayra logs --tail 50` | View last 50 log lines |
| `relayra logs --follow` | Stream logs in real-time |
| `relayra logs --level error` | Filter by level |
| `relayra logs --grep request_id` | Search logs |
| `relayra service uninstall` | Uninstall service, flush Redis data |
| `relayra reset` | Flush all Relayra data from Redis |
| `relayra reset --force` | Flush without confirmation prompt |
| `relayra version` | Show version info |

## API Authentication

Relayra uses Bearer token authentication to protect API endpoints on the Listener.

### Creating Tokens

```bash
relayra token create my-app
```

This outputs a 64-character hex token. **Save it immediately** — it cannot be retrieved again.

### Using Tokens

Include the token in requests:

```bash
curl -H "Authorization: Bearer <token>" http://listener:port/api/v1/relay ...
```

### Protected Endpoints

- `/api/v1/relay` — Submit relay requests
- `/api/v1/result/{id}` — Get results
- `/api/v1/peers` — List peers

### Exempt Endpoints

- `/health` — Always open
- `/api/v1/poll` — Uses peer encryption (Sender only)
- `/api/v1/pair` — Uses pairing tokens

### Graceful Activation

If no API tokens exist, all endpoints are open (backward compatible). Authentication activates automatically when the first token is created.

### Managing Tokens in TUI

On Listener instances, the TUI main menu includes "API Tokens" where you can create, view, and revoke tokens interactively.

## API Endpoints (Listener)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/health` | GET | Health check |
| `/api/v1/relay` | POST | Submit relay request |
| `/api/v1/result/{id}` | GET | Get request result |
| `/api/v1/poll` | POST | Polling endpoint (Sender) |
| `/api/v1/pair` | POST | Pairing endpoint (Sender) |
| `/api/v1/peers` | GET | List connected peers |

## Configuration (.env)

| Variable | Default | Description |
|----------|---------|-------------|
| `RELAYRA_MACHINE_ID` | auto | SHA256 machine identifier |
| `RELAYRA_INSTANCE_NAME` | — | Human-readable name (required) |
| `RELAYRA_ROLE` | — | `listener` or `sender` |
| `RELAYRA_LISTEN_ADDR` | `0.0.0.0` | Bind address |
| `RELAYRA_LISTEN_PORT` | random | Bind port |
| `RELAYRA_REDIS_ADDR` | `127.0.0.1` | Redis host |
| `RELAYRA_REDIS_PORT` | `6379` | Redis port |
| `RELAYRA_REDIS_PASSWORD` | empty | Redis password |
| `RELAYRA_REDIS_DB` | `0` | Redis database number |
| `RELAYRA_POLL_INTERVAL` | `5` | Poll interval in seconds (Sender) |
| `RELAYRA_POLL_BATCH_SIZE` | `10` | Max requests per poll (Sender) |
| `RELAYRA_REQUEST_TIMEOUT` | `30` | HTTP request timeout in seconds |
| `RELAYRA_LOG_LEVEL` | `info` | debug, info, warn, error |
| `RELAYRA_LOG_DIR` | `/opt/relayra/logs` | Log file directory |
| `RELAYRA_LOG_MAX_DAYS` | `7` | Days to keep log files |
| `RELAYRA_RESULT_TTL` | `86400` | Result TTL in seconds (24h) |
| `RELAYRA_PUBLIC_ADDR` | — | Public IP/domain for pairing tokens (Listener) |
| `RELAYRA_WEBHOOK_MAX_RETRIES` | `3` | Max webhook delivery attempts |

## Troubleshooting

### "Redis not responding"
```bash
systemctl status redis-server
systemctl start redis-server
redis-cli ping    # should return PONG
```

### "Connection refused" during pairing
- Verify the Listener is running: `relayra service status`
- Check the Listener's listen port: `grep LISTEN_PORT /opt/relayra/.env`
- Verify the proxy is working: `relayra proxy test`
- Check firewall rules on the Listener

### "All proxies exhausted"
- Check proxy health: `relayra proxy list`
- Test proxies: `relayra proxy test`
- Add working proxies: `relayra proxy add <url>`

### Check logs
```bash
relayra logs --tail 100 --level error
relayra logs --follow
# Or read the log file directly:
tail -f /opt/relayra/logs/relayra.log
```

### Service won't start
```bash
# Check what's wrong:
relayra run   # runs in foreground, shows errors
journalctl -u relayra -n 50   # systemd journal
```

## TUI Panel

Run `relayra` (after setup) to open the interactive TUI panel with an ASCII banner:

### Listener Menu
- **Status Dashboard** — Server status, role, peers count, Redis status
- **Manage Peers** — List peers, press Enter for details (ID, role, address, last seen, queue size), delete
- **API Tokens** — Create, view, and revoke API tokens for relay request authentication
- **View Logs** — Browse log files with color-coded severity; `f` switch files, `g`/`G` top/bottom
- **Settings** — View and edit configuration; editable fields marked with ✎, saves to .env

### Sender Menu
- **Status Dashboard** — Server status, role, **connected Listener info** (name, address, peer ID)
- **Manage Peers** — Same as Listener
- **Manage Proxies** — List proxies, press Enter for details (edit URL/credentials, test, reset cooldown, delete)
- **View Logs** — Same as Listener
- **Settings** — Same as Listener

### Editable Settings (via TUI)
The following settings can be edited inline (press Enter on ✎ fields):
- Listen address, Redis address/port/DB
- Poll interval, poll batch size, request timeout
- Result TTL, log max days, webhook max retries

Changes are saved to `.env`. Restart the service for changes to take effect.

## Test Scripts

Two test scripts are included in `scripts/` for verifying the relay pipeline:

### Relay Test

```bash
sudo bash /opt/relayra/scripts/test-relay.sh
```

Auto-detects port from `.env`, picks the first available peer, and relays a GET request to the Listener's own `/health` endpoint via the Sender. Polls for up to 60 seconds.

### Webhook Test

```bash
sudo bash /opt/relayra/scripts/test-webhook.sh
```

Starts a temporary Python HTTP server on port 9999, submits a relay request with `webhook_url`, and waits up to 90 seconds for the callback.

## Uninstalling

```bash
sudo ./install.sh --uninstall
```

This will stop the service, remove all files from `/opt/relayra/`, remove the symlink, and flush all `relayra:*` keys from Redis.

Alternatively, to just remove the systemd service and flush Redis:

```bash
relayra service uninstall
```

## Architecture Notes

- All poll payloads are encrypted with AES-256-GCM (application-layer)
- Keys are derived via HKDF from the pairing secret + both machine IDs
- Requests are executed sequentially on the Sender (no parallelism)
- Results are stored for 24 hours, then expire from Redis
- Proxies support HTTP and SOCKS5 protocols with optional username/password authentication
- Failed proxies are automatically rotated with a 5-minute cooldown
- Webhooks retry 3 times with exponential backoff (5s, 15s, 45s)
- API token authentication protects Listener endpoints (graceful: disabled if no tokens exist)
- Tokens are stored as SHA256 hashes in Redis — plaintext tokens are never persisted
