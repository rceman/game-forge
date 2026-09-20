# Game Forge daemon protocol v1

This document defines the `game-forge-daemon/v1` control protocol: the wire
contract between the Game Forge CLI (and any future frontend) and the per-user
daemon `game-forged`.

## 1. Model

Every externally callable capability is a **schema-defined operation** behind a
single Operation Registry. One operation has:

- one canonical name (`scenario.run`);
- one input JSON Schema;
- one output JSON Schema;
- one Core handler.

The daemon's HTTP layer is a thin transport: authenticate, decode, schema
validate, dispatch to the registry, encode or stream. It contains no operation
semantics. The CLI is another adapter over the same registry — it parses
familiar syntax into a request and renders the result. The MCP frontends
enumerate the same registry and call the same handlers; they never parse CLI
output and never re-declare schemas.

## 2. Transport

One cross-platform mechanism: **HTTP over loopback TCP on one durable port**.

- Binds only `127.0.0.1`. Never `0.0.0.0`. No public listener.
- The port lives in `50000-59999`. First start scans from a randomized offset,
  binds the first free port, and persists it in
  `~/.game-forge/state/endpoint.json` (durable state, non-secret).
- Every later incarnation rebinds **exactly** the persisted port — an MCP
  client configured against `http://127.0.0.1:<port>/mcp` stays valid across
  restarts. If the persisted port is occupied, startup fails with a clear
  error naming the port; `game-forge daemon rebind` is the explicit recovery —
  the port never moves silently.
- No Unix-socket / named-pipe variants.
- Works identically on Linux, WSL, Windows and macOS.

## 3. Discovery

After binding, the daemon writes `~/.game-forge/run/daemon.json` **atomically**
(write-temp-then-rename, owner-only `0600`):

```json
{
  "protocol": "game-forge-daemon/v1",
  "endpoint": "http://127.0.0.1:43871",
  "pid": 18432,
  "token": "<ephemeral-random-secret>"
}
```

- `~/.game-forge/run/` is ephemeral; `~/.game-forge/state/` is durable.
- A client reads this file, then verifies liveness with an authenticated
  `GET /health`. A missing, malformed, wrong-protocol or unreachable record is
  treated as stale and replaced.
- Graceful shutdown removes the file. A crashed daemon leaves a stale file that
  the next client detects (health check fails) and recovers from.

## 4. Authentication

Loopback is not authorization. There are **two deliberately separate
credentials**:

- **Control-API token** — a cryptographically random 256-bit bearer token
  generated per daemon incarnation, stored only in `daemon.json` (0600).
  Authorizes `/health`, `/v1/*`. It is never logged, printed, committed, or
  placed in project configuration.
- **Durable MCP credential** — a cryptographically random 256-bit token
  persisted at `~/.game-forge/state/mcp.token` (0600), stable across daemon
  incarnations because MCP clients are configured once. Authorizes `/mcp`
  only; it does NOT authorize the control API, and the ephemeral token does
  NOT authorize `/mcp`. It is never emitted in ordinary output — explicit
  `game-forge mcp info --show-token` displays it.

```text
Authorization: Bearer <token>
```

Missing or wrong credential → `401` with a structured error body. `/mcp`
additionally rejects requests whose `Origin` header names a non-local host
(`403`) — a DNS-rebinding guard against hostile pages driving the local
endpoint through the user's browser.

## 5. Endpoints

The HTTP surface is deliberately small. The operation model supplies the
semantic namespace, so there is no large REST hierarchy.

| Method | Path | Purpose |
|---|---|---|
| `GET`  | `/health` | Liveness. `{ok, v, protocol, pid}`. |
| `GET`  | `/v1/capabilities` | `{v, ops:[names]}`. |
| `GET`  | `/v1/catalog` | Every operation's contract in ONE response: `{v, protocol, ops:[{op, summary, stream, input, output}]}`. Frontends initialize from it without N per-schema round trips. |
| `GET`  | `/v1/schema/<op>` | One operation's contract: `{op, summary, stream, input, output}`. Kept for debugging/external clients. |
| `POST` | `/v1/run` | Execute one operation (JSON or NDJSON). |
| `POST` | `/v1/shutdown` | Graceful stop. |
| `POST` | `/mcp` | Streamable-HTTP MCP endpoint (durable credential). Serves the same operations as MCP tools through the daemon's in-process dispatcher — identical pipeline to `/v1/run`, no HTTP loop. |

## 6. Request envelope

Compact but readable. `POST /v1/run` body:

```json
{
  "v": 1,
  "id": "42",
  "op": "scenario.run",
  "cwd": "/home/user/git/td-game",
  "args": { "id": "weapons", "ticks": 600 }
}
```

- `v` — protocol version. Always `1`; any other value → `unsupported_version`.
- `id` — optional request id, echoed on replies and events. When omitted the
  daemon assigns a unique `q_<inc>_<n>` so a correlation identifier is never
  ambiguously empty.
- `op` — canonical operation name.
- `cwd` — the caller's working directory. The daemon has its own, so project
  discovery is told where the caller was. Transport metadata, not semantics.
- `project` — a registered project code (e.g. `"TDG"`). Mutually exclusive
  with `cwd` (`invalid_request` if both are sent). The daemon resolves it
  through the durable project registry to the registered canonical root
  before validation — see §6a. MCP calls always use this selector.
- `args` — the operation's own argument object, validated against its input
  schema. `project` never reaches the operation's canonical schema.

### 6a. Project registry

The durable machine-local registry (`~/.game-forge/state/projects.json`) maps
stable codes to project roots:

```text
game-forge project add TDG                          # discover from cwd upward
game-forge project add --code TDG --folder ~/git/td-game
game-forge project list | show TDG | remove TDG
```

Registration discovers the manifest directory (walks up to `game-forge.yaml`),
canonicalizes it, loads the manifest, and records `{code, root, projectId,
projectKey}` — the key is the existing collision-safe identity, reused, not
reinvented. Registration is local admin state: it never requires the daemon,
and a running daemon sees changes immediately because resolution re-reads the
registry per request.

Resolution failures are structured:

- `unknown_project` — the code is not registered (400).
- `project_unavailable` — the registered root is missing/unloadable (400).
- `project_changed` — the manifest's identity no longer matches the
  registration; re-register with `--replace` (400).

There are no project sessions: no `session_start`, no `session_use`, no
session id. `project_code` on each MCP call (mapped to `project` in the
envelope) is the complete selection mechanism.

## 7. Response envelope

For a non-streamed request (`Accept` not `application/x-ndjson`), one reply:

```json
{ "id": "42", "ok": true, "data": { "...": "..." } }
```

On failure:

```json
{ "id": "42", "ok": false, "err": { "code": "invalid_args", "path": "/ticks", "msg": "must be >= 0" } }
```

An operation that completes but asserts a failure returns `ok:false` **with**
`data` and `err` together — the result is still meaningful (e.g. a scenario
report) but the command exits non-zero.

### Error codes

`invalid_request`, `unsupported_version`, `unknown_op`, `invalid_args`,
`invalid_output`, `unknown_project`, `project_unavailable`, `project_changed`,
`canceled`, `failed`, `internal`. These are stable.

- `invalid_args` — args failed the operation's input schema (client error).
- `invalid_output` — the handler produced a schema-invalid result (contract
  bug; it is never sent as if valid).
- `unknown_project` / `project_unavailable` / `project_changed` — the
  `project` selector failed registry resolution (§6a).
- `internal` — a daemon/protocol fault.

## 8. NDJSON streaming

For a long-running operation, send `Accept: application/x-ndjson`. The daemon
replies `Content-Type: application/x-ndjson` with one JSON object per line:

```json
{"id":"42","ev":"start","run":"r_a1b2c3_1"}
{"id":"42","ev":"stage","name":"check","status":"pass","ms":1180,"data":"exit=0"}
{"id":"42","ev":"artifact","kind":"image","ref":"shot","path":"/tmp/shot.png"}
{"id":"42","ev":"done","run":"r_a1b2c3_1","status":"pass","data":{...},"code":0}
```

Event types: `start`, `stage`, `artifact`, `done`.

- `start` — the run began; carries `run`, the daemon-assigned run id
  `r_<incarnation>_<n>`: unique per run, stable for every event of one stream,
  and distinct across daemon restarts (the incarnation differs). It never
  carries token material.
- `stage` — one named step finished; `name`, `status` (`pass`/`fail`), `ms`,
  and a short `data` detail.
- `artifact` — a large result by reference (`kind`, `ref`, `path`), never by
  value. Screenshots and logs are referenced, not embedded.
- `done` — terminal; `status`, `code` (0/1), and `data` (the operation result)
  or `err`.

Unchanged metadata (protocol, cwd, version, timestamps) is never repeated per
line. Successful raw subprocess logs are not streamed; they are retained as
artifacts and surfaced on failure.

## 9. Validation

JSON Schema 2020-12 is the contract source. Schemas are checked in under
`schemas/` and embedded into the binary; no remote `$ref` is fetched at
runtime.

- `schemas/protocol/` — `request`, `response`, `event`, `error`.
- `schemas/ops/` — `<op>.input.schema.json` and `<op>.output.schema.json` per
  operation.

Boundaries are validated: request envelope → operation lookup → args → handler
→ output → encode. Streamed events are validated against the event schema. A
schema-invalid Core result is rejected as `invalid_output`, never sent as valid.

The same per-operation input schemas are what a future MCP frontend advertises
as tool `inputSchema`s — one schema, every frontend.

## 10. Housekeeping

The daemon owns periodic housekeeping: an in-process loop invokes the same Core
`Tick` that `game-forge tick` exposes, reclaiming expired owned resources while
skipping resources whose run is in-flight. It never shells out to the binary.
At startup the daemon runs one reconciliation pass over the durable resource
registry to reclaim resources abandoned by a previous incarnation.

## 11. Cancellation

`POST /v1/run` runs under the HTTP request context. If the client disconnects,
the context cancels and the running operation's owned child processes are
cleaned safely. Long-lived daemon-owned resources (the shared dev server and
browser) are *not* torn down — they follow their normal reuse/lease policy.
