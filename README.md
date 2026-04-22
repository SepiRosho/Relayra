# Relayra

Relayra is a relay system for reaching services inside restricted networks without opening inbound access on the restricted side.

It works with two roles:

- `listener`: public-facing node that accepts relay requests
- `sender`: restricted node that polls the listener, executes requests locally, and returns the results

Relay traffic is encrypted, senders can use proxy chains, and the project includes both a CLI and a terminal UI.

## Why Relayra?

Relayra is useful when:

- the target machine cannot accept inbound connections
- outbound access must go through proxies
- you want a simple request/response relay over polling
- you need a lightweight self-hosted bridge instead of a full VPN or tunnel stack

## Features

- Listener/Sender topology with one-time pairing tokens
- AES-256-GCM encrypted poll payloads
- Long polling support for lower latency
- Async relay requests with concurrent execution
- Optional listener-side execution for two-way request flow
- Proxy management with health tracking and cooldowns
- Long-poll proxy reliability testing
- API token authentication on listener endpoints
- Result polling and webhook delivery
- Redis or SQLite storage backends
- Bubble Tea TUI for day-to-day operations

## How It Works

1. A client submits a relay request to the listener.
2. The listener stores the request for the destination peer.
3. The sender polls the listener, receives work, and executes it locally.
4. Results are sent back on the next available poll cycle.
5. The client fetches the result or receives it by webhook.

## Quick Start

### 1. Build a release bundle

On Windows:

```powershell
cmd /c build.bat
```

### 2. Install on the target machine

```bash
tar xzf relayra-*-linux-amd64.tar.gz
cd relayra-*/
chmod +x install.sh
sudo ./install.sh
```

### 3. Run setup

```bash
relayra
```

The setup wizard now supports:

- Listener or Sender role
- Redis or SQLite storage
- long polling defaults
- listener-side execution policy

### 4. Pair the machines

On the listener:

```bash
relayra pair generate --expires 1h
```

On the sender:

```bash
relayra proxy add socks5://proxy.example.com:1080
relayra pair connect <token>
```

### 5. Start the service

```bash
relayra run
```

Or install as a systemd service:

```bash
relayra service install
relayra service start
```

## Example Relay Request

```bash
curl -X POST http://listener-ip:port/api/v1/relay \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <api-token>" \
  -d '{
    "destination_peer_id": "<sender-peer-id>",
    "async": true,
    "request": {
      "url": "http://localhost:8080/api/data",
      "method": "GET"
    }
  }'
```

To target the listener itself, use `destination_peer_id` as `listener`, `self`, or the listener machine ID, if listener-side execution is enabled.

## Storage Backends

Relayra supports:

- `redis`: better for shared/networked deployments
- `sqlite`: better for simple single-node installs

Set the backend in `.env`:

```env
RELAYRA_STORAGE_BACKEND=sqlite
RELAYRA_SQLITE_PATH=/opt/relayra/relayra.db
```

## Useful Commands

```bash
relayra
relayra run
relayra peers
relayra proxy list
relayra proxy test-longpoll --samples 3 --wait 30
relayra token create my-app
relayra logs
```

## Documentation

- Detailed operator guide: [GUIDE.md](GUIDE.md)
- Example config: [.env.example](.env.example)

## Project Status

Relayra is now in its early public-release phase. It already supports the main relay workflow, but it is still a good idea to validate your topology and proxy behavior in staging before relying on it in production.
