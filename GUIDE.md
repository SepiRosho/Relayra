# Relayra Guide

## Overview

Relayra bridges HTTP requests between two machines:

- `listener`: reachable machine that exposes the public Relayra API
- `sender`: restricted machine that polls the listener, executes requests locally, and returns the results

Relay payloads are encrypted with AES-256-GCM. Senders can connect through HTTP or SOCKS5 proxies, and Relayra can store state in Redis or SQLite.
Relayra uses durable lease/ack delivery with request/result ID dedupe so temporary proxy outages bias toward replay instead of silent loss.

## Core Concepts

### Listener

The listener:

- accepts relay requests from clients
- stores queued work
- returns results to clients
- pairs with senders using one-time tokens
- can optionally execute requests on its own side if listener execution is enabled

### Sender

The sender:

- connects outbound to the listener
- can use configured proxies
- receives relay jobs over interval polling, long polling, or WebSocket
- executes requests locally
- sends finished results back over the next successful sync cycle

### Async Requests

Relay requests can include:

```json
{
  "async": true
}
```

When enabled, the sender can execute that request without waiting for earlier synchronous jobs to finish.

## Installation

```bash
tar xzf relayra-*-linux-amd64.tar.gz
cd relayra-*/
chmod +x install.sh
sudo ./install.sh
```

The installer creates `/opt/relayra/`, installs the binary, and places logs under `/opt/relayra/logs`.

## First-Time Setup

Run Relayra with no arguments:

```bash
relayra
```

The setup wizard will ask for:

- role: Listener or Sender
- listen address and port
- public address for pairing tokens on listeners
- instance name
- storage backend: Redis or SQLite
- Redis settings if Redis is selected
- sender transport mode and proxy cooldown on sender nodes
- log level
- listener-side execution policy on listener nodes

Configuration is saved to `/opt/relayra/.env` on installed systems, or to the local working directory in development mode.

## Pairing

### On the Listener

Generate a one-time pairing token:

```bash
relayra pair generate --expires 1h
```

### On the Sender

Add at least one proxy if your environment requires it:

```bash
relayra proxy add socks5://proxy.example.com:1080
relayra proxy add socks5://user:password@proxy.example.com:1080
relayra proxy add http://user:password@proxy.example.com:8080
```

Then pair:

```bash
relayra pair connect <token>
```

Pairing exchanges capabilities as well, so each side can see whether the other supports long polling, WebSocket transport, async execution, storage type, and listener-side execution.

## Running Relayra

### Foreground

```bash
relayra run
```

### systemd

```bash
relayra service install
relayra service start
relayra service status
```

## Sending Relay Requests

### Standard Request

```bash
curl -X POST http://listener-ip:port/api/v1/relay \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api-token>" \
  -d '{
    "destination_peer_id": "<sender-peer-id>",
    "request": {
      "url": "http://localhost:8080/api/data",
      "method": "GET",
      "headers": {
        "Authorization": "Bearer token123"
      }
    }
  }'
```

### Async Request

```bash
curl -X POST http://listener-ip:port/api/v1/relay \
  -H "Content-Type: application/json" \
  -d '{
    "destination_peer_id": "<sender-peer-id>",
    "async": true,
    "request": {
      "url": "http://localhost:8080/jobs/run",
      "method": "POST"
    }
  }'
```

### Listener-Side Execution

If the listener has listener-side execution enabled, target the listener by using:

- `listener`
- `self`
- the listener machine ID

Example:

```bash
curl -X POST http://listener-ip:port/api/v1/relay \
  -H "Content-Type: application/json" \
  -d '{
    "destination_peer_id": "listener",
    "request": {
      "url": "http://127.0.0.1:9000/internal/health",
      "method": "GET"
    }
  }'
```

If listener-side execution is disabled, the request is refused.

## Getting Results

### Poll For Result

```bash
curl http://listener-ip:port/api/v1/result/<request-id>
```

### Webhook Delivery

If `webhook_url` is included in the relay request, Relayra will POST the result when it is ready and retry on failure.
Results remain durable on the sender until the listener acknowledges receipt.

## API Authentication

Listener endpoints become protected automatically after the first API token is created.

Create a token:

```bash
relayra token create my-app
```

Use it with:

```bash
Authorization: Bearer <token>
```

Protected endpoints:

- `POST /api/v1/relay`
- `GET /api/v1/result/{id}`
- `GET /api/v1/peers`

Open endpoints:

- `GET /health`
- `POST /api/v1/poll`
- `GET /api/v1/ws`
- `POST /api/v1/pair`

## Proxy Operations

List proxies:

```bash
relayra proxy list
```

Test connectivity:

```bash
relayra proxy test
relayra proxy test socks5://proxy.example.com:1080
```

Measure long-poll stability:

```bash
relayra proxy test-longpoll --samples 3 --wait 30
```

Reset failed proxy cooldowns:

```bash
relayra proxy reset-cooldown
```

## TUI Sections

Listener menu:

- Status Dashboard
- Manage Peers
- API Tokens
- View Logs
- Settings

Sender menu:

- Status Dashboard
- Manage Peers
- Manage Proxies
- View Logs
- Settings

The sender peers screen shows the connected listener and its advertised capabilities.

## Configuration

### Identity and Role

| Variable | Default | Description |
|---|---|---|
| `RELAYRA_MACHINE_ID` | auto | Generated machine identifier |
| `RELAYRA_INSTANCE_NAME` | required | Human-readable node name |
| `RELAYRA_ROLE` | required | `listener` or `sender` |

### Network

| Variable | Default | Description |
|---|---|---|
| `RELAYRA_LISTEN_ADDR` | `0.0.0.0` | Bind address |
| `RELAYRA_LISTEN_PORT` | random | Bind port |
| `RELAYRA_PUBLIC_ADDR` | empty | Public listener address used in pairing tokens |

### Storage

| Variable | Default | Description |
|---|---|---|
| `RELAYRA_STORAGE_BACKEND` | `redis` | `redis` or `sqlite` |
| `RELAYRA_SQLITE_PATH` | `/opt/relayra/relayra.db` | SQLite path when SQLite is enabled |
| `RELAYRA_REDIS_ADDR` | `127.0.0.1` | Redis host |
| `RELAYRA_REDIS_PORT` | `6379` | Redis port |
| `RELAYRA_REDIS_PASSWORD` | empty | Redis password |
| `RELAYRA_REDIS_DB` | `0` | Redis DB number |

### Polling and Execution

| Variable | Default | Description |
|---|---|---|
| `RELAYRA_POLL_INTERVAL` | `5` | Standard poll interval in seconds |
| `RELAYRA_POLL_BATCH_SIZE` | `10` | Max requests returned per poll |
| `RELAYRA_REQUEST_TIMEOUT` | `30` | HTTP execution timeout in seconds |
| `RELAYRA_TRANSPORT_MODE` | `long-poll` | `interval`, `long-poll`, or `websocket` |
| `RELAYRA_LONG_POLLING` | `true` | Legacy compatibility shim if `RELAYRA_TRANSPORT_MODE` is unset |
| `RELAYRA_LONG_POLL_WAIT` | `30` | Max long-poll wait window in seconds |
| `RELAYRA_PROXY_COOLDOWN_SECONDS` | `300` | Proxy cooldown after 3 consecutive failures |
| `RELAYRA_ASYNC_WORKERS` | `4` | Max concurrent async request workers |
| `RELAYRA_ALLOW_LISTENER_EXECUTION` | `false` | Allow listener-side request execution |

## Reliability and Upgrades

- Relayra now keeps queued requests and pending results durable until explicit acknowledgement.
- Sender reconnects reconcile request states and pending results using request/result IDs.
- Delivery is intentionally biased toward at-least-once behavior, not exactly-once execution against downstream HTTP services.
- Upgrade this release during a drained maintenance window: finish queued work before rolling out the new binaries.

### Logging and Results

| Variable | Default | Description |
|---|---|---|
| `RELAYRA_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `RELAYRA_LOG_DIR` | `/opt/relayra/logs` | Log directory |
| `RELAYRA_LOG_MAX_DAYS` | `7` | Log retention in days |
| `RELAYRA_RESULT_TTL` | `86400` | Result retention in seconds |
| `RELAYRA_WEBHOOK_MAX_RETRIES` | `3` | Webhook retry count |

## Troubleshooting

### Listener Not Reachable

- verify `relayra run` or `relayra service status`
- check the listener bind port in `.env`
- confirm `RELAYRA_PUBLIC_ADDR` is correct
- confirm firewall rules allow the listener port

### Pairing Fails

- verify the token has not expired
- confirm the sender can reach the listener through at least one proxy
- run `relayra proxy test`
- run `relayra proxy test-longpoll --samples 3 --wait 30`

### No Proxies Available

- inspect `relayra proxy list`
- test each proxy
- reset cooldowns with `relayra proxy reset-cooldown`

### Storage Problems

For Redis:

```bash
redis-cli ping
```

For SQLite:

- confirm the database path is writable
- confirm the configured file exists or its parent directory can be created

### Logs

```bash
relayra logs
```

Or read the file directly:

```bash
tail -f /opt/relayra/logs/relayra-YYYY-MM-DD.log
```
