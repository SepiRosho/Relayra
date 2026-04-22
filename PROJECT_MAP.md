# Relayra Project Map

## Purpose

Relayra is a Go CLI/TUI application for relaying HTTP requests between:

- `listener`: internet-reachable node exposing the public API
- `sender`: restricted node that polls the listener through configured proxies

The sender pulls queued requests, executes them locally, and sends results back. Poll traffic is encrypted with AES-256-GCM using a key derived during pairing. Redis is the system of record for queues, peers, results, tokens, and proxy state.

## Entry Points

- [`cmd/relayra/main.go`](/E:/Go/Relayra/cmd/relayra/main.go:1): binary entry point, delegates to Cobra CLI.
- [`internal/cli/root.go`](/E:/Go/Relayra/internal/cli/root.go:1): root command.
  - No config present: launches setup wizard.
  - Config present: launches Bubble Tea TUI.
- [`internal/cli/run.go`](/E:/Go/Relayra/internal/cli/run.go:1): foreground service runner.
  - `listener` role -> starts HTTP server.
  - `sender` role -> starts polling loop.

## Runtime Architecture

### Listener path

1. HTTP request arrives at `/api/v1/relay`.
2. Listener validates API token if token auth is enabled.
3. Request is stored in Redis and appended to the destination peer queue.
4. Sender polls `/api/v1/poll`.
5. Listener decrypts poll payload, stores returned results, optionally starts webhook delivery, acks previously received requests, and dequeues the next batch.
6. Client fetches results through `/api/v1/result/{id}` or receives them by webhook.

### Sender path

1. Sender loads paired listener info from Redis.
2. Sender chooses a proxy transport from the proxy manager.
3. Sender polls listener on a fixed interval.
4. Sender decrypts incoming batch, executes HTTP requests sequentially against local targets, stores results in a local pending-results queue, and includes request acknowledgements in the next poll.
5. Listener acknowledges result receipt in later poll responses.

## Main Packages

- [`internal/config`](/E:/Go/Relayra/internal/config/config.go:1)
  - Loads/saves `.env`.
  - `EnvPath()` prefers `/opt/relayra/.env`, otherwise repo-local `.env`.
  - Important for install-vs-dev behavior.

- [`internal/server`](/E:/Go/Relayra/internal/server/server.go:1)
  - Listener HTTP server and middleware.
  - Routes in [`handlers.go`](/E:/Go/Relayra/internal/server/handlers.go:1).
  - Auth middleware in [`middleware.go`](/E:/Go/Relayra/internal/server/middleware.go:1).

- [`internal/poller`](/E:/Go/Relayra/internal/poller/poller.go:1)
  - Sender polling loop.
  - Uses proxy manager to reach listener.
  - Executes requests via [`executor.go`](/E:/Go/Relayra/internal/poller/executor.go:1).

- [`internal/proxy`](/E:/Go/Relayra/internal/proxy/manager.go:1)
  - Stores proxies in Redis sorted set.
  - Chooses proxies by priority.
  - Tracks failure count and cooldown.
  - Supports `http`, `https`, `socks5`, `socks5h`.

- [`internal/store`](/E:/Go/Relayra/internal/store/redis.go:1)
  - Redis access layer.
  - Subareas:
    - queues/results: [`queue.go`](/E:/Go/Relayra/internal/store/queue.go:1), [`results.go`](/E:/Go/Relayra/internal/store/results.go:1)
    - peers/pairing: [`peers.go`](/E:/Go/Relayra/internal/store/peers.go:1)
    - API tokens: [`api_tokens.go`](/E:/Go/Relayra/internal/store/api_tokens.go:1)

- [`internal/cli`](/E:/Go/Relayra/internal/cli/root.go:1)
  - Operational commands: setup, run, service, pair, peers, proxy, logs, reset, token, version.

- [`internal/tui`](/E:/Go/Relayra/internal/tui/app.go:1)
  - Bubble Tea operator UI.
  - Main views:
    - dashboard
    - peers
    - proxies
    - API tokens
    - logs
    - settings
    - setup wizard

- [`internal/webhook`](/E:/Go/Relayra/internal/webhook/delivery.go:1)
  - Async webhook retries with exponential backoff.

- [`internal/crypto`](/E:/Go/Relayra/internal/crypto/keys.go:1)
  - Pairing secret generation, key derivation, JSON encrypt/decrypt.

- [`internal/models`](/E:/Go/Relayra/internal/models/request.go:1)
  - Shared payload and storage models for requests, results, polls, peers, and tokens.

## Important Commands

- `relayra`
  - Setup wizard if no config exists, otherwise TUI.
- `relayra run`
  - Start service in foreground.
- `relayra pair generate --expires 1h`
  - Listener creates one-time pairing token.
- `relayra pair connect <token>`
  - Sender pairs to listener, usually through proxy.
- `relayra proxy add <url>`
  - Sender adds transport options.
- `relayra token create <name>`
  - Listener enables/extends API auth.

## HTTP Surface

- `GET /health`
- `POST /api/v1/relay`
- `GET /api/v1/result/{requestID}`
- `POST /api/v1/poll`
- `POST /api/v1/pair`
- `GET /api/v1/peers`

Auth behavior:

- `/health`, `/api/v1/poll`, and `/api/v1/pair` are exempt.
- If zero API tokens exist, protected endpoints are open.
- Once at least one token exists, Bearer auth is enforced for relay/result/peers.

## Redis Key Map

- `relayra:queue:<peerID>`: queued outbound requests for a sender
- `relayra:request:<requestID>`: request metadata and status
- `relayra:result:<requestID>`: completed result with TTL
- `relayra:pending_results`: sender-side results waiting to be returned
- `relayra:peer:<peerID>`: peer record
- `relayra:peers`: peer ID set
- `relayra:pairing:<secretHash>`: one-time pairing token
- `relayra:listener:info`: sender-side stored listener info
- `relayra:apitoken:<id>`: API token record
- `relayra:apitokens`: API token ID set
- `relayra:apitoken:hash:<sha256>`: reverse lookup for auth
- `relayra:proxy:list`: sorted set of proxies
- `relayra:proxy:status:<hash>`: proxy fail count and cooldown info

## Config Model

Primary config lives in `.env` and maps to `internal/config.Config`.

Notable fields:

- identity: machine ID, instance name, role
- network: listen addr/port, public addr
- Redis: addr/port/password/db
- sender behavior: poll interval, batch size, request timeout
- ops: log level, log directory, retention
- retention/retries: result TTL, webhook max retries

## TUI Shape

Role-specific menu:

- listener: dashboard, peers, API tokens, logs, settings
- sender: dashboard, peers, proxies, logs, settings

The TUI mostly wraps existing Redis-backed operations rather than defining separate business logic. Future changes to runtime behavior will usually land in `server`, `poller`, `store`, or `proxy`, not primarily in `tui`.

## Build and Distribution

- [`Makefile`](/E:/Go/Relayra/Makefile:1): standard build targets.
- [`build.bat`](/E:/Go/Relayra/build.bat:1): Windows-friendly build.
- `dist/`: packaged Linux artifact and extracted bundle.
- `scripts/`: install and manual relay/webhook test scripts.

Current repo note:

- `build/` and `dist/` are untracked artifacts in the working tree.

## Likely Hotspots For Future Work

- `internal/server/handlers.go`
  - central business flow for relay, poll, result retrieval, pairing
- `internal/poller/poller.go`
  - delivery guarantees, retries, batching, sender execution behavior
- `internal/proxy/manager.go`
  - proxy rotation, cooldowns, transport creation
- `internal/store/*`
  - queue semantics, Redis schema changes, TTL/status tracking
- `internal/tui/*`
  - operator workflows and UX

## Risks / Things To Remember

- Sender executes relayed requests sequentially; throughput work will likely start in `poller`.
- The sender pops pending results before listener acknowledgement; the code comments note crash-loss risk in this area.
- Pairing is one-time-token based and consumes the token on first successful retrieval.
- Listener auth is intentionally disabled until the first API token exists.
- Config path behavior changes between installed Linux layout and local dev layout.
- Proxy status hashing in `hashURL()` is only a lightweight derived string, so any proxy-key refactor should check compatibility carefully.

## Good First Checks During Future Tasks

- Does the change affect listener-only, sender-only, or both roles?
- Is Redis schema/status behavior changing?
- Does the TUI need to reflect the new state or command?
- Will setup/install docs need updates in [`GUIDE.md`](/E:/Go/Relayra/GUIDE.md:1)?
- Do proxy, pairing, or auth assumptions change any operator workflow?
