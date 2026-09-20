# Game Forge Architecture Direction

Status: architectural direction for the first implementation. This document is intentionally concrete enough to guide bootstrap work while leaving exact APIs free to evolve through real consumer integration.

## 1. Top-level architecture

```text
                         Game Forge
                             |
          +------------------+------------------+
          |                  |                  |
       Project            Browser            System
       Runtime            Runtime            Runtime
          |                  |                  |
      adapters          browser provider    resources
      scenarios         screenshots         leases/TTL
      tests             diagnostics         scheduler
      builds            GPU                 cleanup
          |
          +------------------+
                             |
                          Game
```

Game Forge Core is reusable from multiple frontends, all over the same
Operation Registry:

```text
                 game-forged
                     |
     +---------------+---------------+
     |                               |
  Project Registry             Operation Registry
     |                               |
     +---------------+---------------+
                     |
                    Core

  CLI ----------------^
  MCP HTTP (/mcp) ----^
  MCP stdio ----------^
  future UI/CI -------^
```

Frontends never touch Core directly — they go through the per-user daemon,
which owns lifecycle and resource ownership:

```text
CLI        -> daemon HTTP client -> game-forged -> registry -> Core
MCP /mcp   -> in-process         -> game-forged -> registry -> Core
MCP stdio  -> daemon client      -> game-forged -> registry -> Core
```

The CLI is the first frontend; the canonical MCP endpoint is the daemon's own
`/mcp` streamable-HTTP route, served by an in-process frontend sharing the
daemon's dispatcher — MCP calls execute the identical `prepare`+`exec`
pipeline as `/v1/run` with no HTTP loop into the daemon. `mcp serve` remains
as a stdio compatibility frontend over the daemon client. All share operation
semantics and schemas; the daemon remains the single owner of browsers,
servers, leases and housekeeping.

### Project selection

Every MCP call carries an explicit `project_code` — there are no project
sessions, no `session_start`/`session_use`, no hidden current-project state,
and no per-project daemon or MCP endpoint. Codes map to projects through the
durable machine-local registry (`~/.game-forge/state/projects.json`,
`game-forge project add|list|show|remove`). The daemon resolves the code to a
canonical root per request, so registry changes are visible immediately and
concurrent calls for different projects never share mutable selection state.
See `DAEMON_PROTOCOL_V1.md`.

## 2. Repository shape

Initial Go repository direction:

```text
cmd/game-forge/
internal/
  config/
  project/
  contract/
  runner/
  scenario/
  browser/
  process/
  op/          # Operation Registry: names + schemas + handlers
  core/        # the operation implementations
  daemon/      # loopback HTTP transport, discovery, housekeeping
  client/      # in-process daemon client used by the CLI and MCP frontend
  mcpfrontend/ # the stdio MCP frontend over the same operation model
schemas/
  protocol/    # request/response/event/error envelopes
  ops/         # per-operation input/output JSON Schemas
docs/
examples/minimal-web-game/
```

Do not create packages until real code needs them.

## 3. Configuration model

### Global machine configuration

Path:

```text
~/.game-forge/config.yaml
```

Responsibilities:

- local provider selection;
- local executable discovery;
- Windows/WSL interop;
- Chrome path;
- agent-browser path;
- future ffmpeg/media tools;
- other machine-local preferences.

Example shape:

```yaml
version: 1

browser:
  provider: agent-browser
  host: windows

  agent_browser:
    cli: 'C:\path\to\agent-browser.cmd'
    chrome: 'C:\Program Files\Google\Chrome\Application\chrome.exe'
    headless: true
    namespace_prefix: game-forge
```

The exact schema may evolve.

### Project configuration

Path:

```text
<project>/game-forge.yaml
```

Responsibilities:

- contract version;
- project ID/type;
- capabilities;
- adapter entrypoints;
- test/build profiles;
- scenario/visual/GPU configuration.

Machine-specific executable paths are forbidden here.

## 4. Project discovery

Game Forge should locate a project by walking upward from the working directory until it finds `game-forge.yaml`.

Commands should behave consistently from project subdirectories.

A project manifest declares capabilities, not implementation tools.

## 5. Contract v1

Contract identifier:

```text
game-forge/v1
```

The contract should be:

- explicit;
- versioned;
- language neutral;
- small;
- capability driven.

Game Forge should fail clearly on unsupported contract versions.

Detailed schema belongs in `docs/CONTRACT_V1.md` once the first vertical slice proves the required fields.

## 6. Authoritative/headless adapter

Preferred v1 transport:

```text
JSONL over stdio
```

Reasons:

- no daemon;
- no network port;
- easy manual inspection;
- process lifecycle naturally bounded;
- language independent;
- straightforward request/response correlation.

Conceptual request methods:

```text
capabilities
scenario.list
scenario.describe
scenario.run
snapshot
diagnostics
```

Responses should include stable machine-readable status and enough deterministic metadata to reproduce a run.

Game-specific state may be returned as opaque JSON structures or digests where appropriate. Game Forge should not interpret game semantics.

## 7. Browser bridge

A browser consumer may expose a small versioned bridge, conceptually:

```text
window.__gameForge
```

Potential operations:

```text
contractVersion
capabilities()
scenarioList()
scenarioLoad(input)
advance(ticks)
snapshot()
diagnostics()
metrics()
```

The bridge must not import or know agent-browser/CDP/PowerShell details.

Game Forge's browser provider calls the bridge externally.

## 8. Browser provider

Generic provider responsibilities:

```text
Open
Navigate
Eval
Screenshot
CollectErrors
Close
```

First provider:

```text
agent-browser
```

On the current development machine:

- repositories live in WSL;
- browser provider runs as native Windows agent-browser;
- Chrome runs natively on Windows;
- default browser mode is headless;
- GPU path uses native Windows ANGLE/D3D11.

WSL agent-browser is not required for this provider.

Provider-specific command construction belongs inside Game Forge.

## 9. Browser isolation

Each managed browser run should have a unique Game Forge-owned namespace/session identifier.

Example:

```text
game-forge-<project>-<run-id>
```

Do not attach to the user's normal Chrome profile/session by default.

Normal completion closes the owned namespace/session.

Abandoned sessions are tracked as resources and cleaned later.

## 10. Scenario execution

Scenario semantics remain inside the game.

Game Forge handles:

- scenario discovery;
- generic parameter transport;
- execution mode;
- deterministic run metadata;
- timeout/process lifecycle;
- result normalization;
- artifact/report collection.

Long-term execution modes:

```text
game-forge scenario run <id>
game-forge scenario run <id> --browser
game-forge scenario compare <id>
```

A headless/browser compare should compare authoritative outputs, not screenshots alone.

## 11. Determinism metadata

Scenario reports should preserve enough metadata to reproduce the run.

Generic metadata may include:

- scenario ID;
- adapter/contract version;
- explicit/resolved seeds;
- tick count or simulation time;
- scenario parameters;
- relevant project build/version identity.

Game Forge transports and reports seeds but does not define game-specific RNG semantics.

## 12. Testing and verification

Game Forge orchestrates native project tools.

It does not replace their assertion frameworks.

Conceptual profiles:

```text
game-forge test
game-forge verify fast
game-forge verify full
```

Project profiles may invoke:

- typecheck/build;
- Vitest;
- pytest;
- cargo test;
- go test;
- scenario verification;
- browser verification;
- production smoke;
- lifecycle;
- audio;
- GPU.

A provider's output may be summarized, but Game Forge must preserve the true underlying failure status.

## 13. Visual validation

Generic browser workflow:

```text
start/open browser
    -> load project
    -> load scenario
    -> deterministic advance
    -> present
    -> screenshot
    -> named crop/region if requested
    -> collect operation errors
    -> cleanup
```

Desired interface:

```text
game-forge shot <scenario>
game-forge shot <scenario> --ticks 120 --region tower
```

A screenshot file existing does not mean the operation passed. Fresh page/browser errors must fail the operation.

## 14. GPU verification

GPU verification is a Game Forge capability, not a game implementation detail.

Desired command:

```text
game-forge gpu
```

Generic sequence:

1. resolve browser provider;
2. launch isolated native browser;
3. load deterministic benchmark scenario;
4. inspect WebGL renderer;
5. reject software fallback;
6. warm up;
7. collect existing game rendering metrics;
8. print result;
9. cleanup resources.

Known software/fallback identifiers should include at least:

- SwiftShader;
- WARP;
- llvmpipe;
- Microsoft Basic Render Driver.

A game provides the benchmark scenario and any canonical budget. Game Forge must not invent a budget.

## 15. Resource registry

Persistent state root:

```text
~/.game-forge/state/
```

Suggested resource directory:

```text
~/.game-forge/state/resources/
```

A resource record should carry enough identity for safe recovery:

- run ID;
- project ID/path;
- resource type;
- provider;
- namespace/session;
- PID/process identity when meaningful;
- creation time;
- lease expiry;
- launch metadata needed for provider-specific cleanup.

Never clean unrelated processes by executable name.

## 16. Lease and cleanup model

Temporary resources receive a lease.

Normal completion:

```text
start resource
-> register resource
-> run
-> graceful cleanup
-> remove record
```

Crash/abandonment:

```text
start resource
-> register resource
-> caller disappears
-> lease expires
-> later tick/gc detects stale record
-> provider-specific safe cleanup
-> remove record
```

Initial user-facing commands:

```text
game-forge ps
game-forge gc
game-forge tick
```

`gc` performs explicit stale cleanup.

`tick` performs one idempotent housekeeping pass.

## 17. Daemon

Game Forge now owns a persistent per-user control daemon, `game-forged`. It
replaces the earlier cron/OS-scheduler housekeeping model entirely: there is no
cron entry, systemd timer, Windows Scheduled Task, or SYSTEM service.

Transport is one cross-platform mechanism — HTTP over loopback TCP. The daemon
binds **one durable port** in `50000-59999`: first start picks a free port,
persists it in `~/.game-forge/state/endpoint.json`, and every later
incarnation rebinds exactly it — so MCP client configuration is stable across
restarts. An occupied persisted port fails startup loudly; `game-forge daemon
rebind` is the deliberate recovery (it never silently moves). There are no
Unix-socket / named-pipe variants and no public listener.

Ordinary commands transparently start and reuse the daemon:

```text
read ~/.game-forge/run/daemon.json
-> authenticated GET /health
-> healthy: use it
-> absent/stale: start game-forge daemon serve, wait, re-check
```

Discovery state is ephemeral and written atomically; it carries a 256-bit
bearer token that authorizes every request. The token is never logged or
printed.

The daemon owns periodic housekeeping: an in-process loop calls the same Core
`Tick` that `game-forge tick` exposes, reclaiming expired owned resources while
skipping resources whose run is in-flight. It never shells out to the binary.

Daemon lifecycle commands:

```text
game-forge daemon status
game-forge daemon stop
game-forge daemon restart
game-forge daemon rebind   # choose a new durable port (MCP endpoint changes)
```

See `DAEMON_PROTOCOL_V1.md` for the wire contract.

## 18. MCP frontend

The canonical MCP transport is the daemon's own streamable-HTTP endpoint —
`http://127.0.0.1:<stable-port>/mcp` — so agents configure one URL once. It is
a thin frontend over the daemon's in-process dispatcher:

```text
MCP client --HTTP--> game-forged /mcp --in-process dispatch--> Operation Registry -> Core
MCP client --stdio--> game-forge mcp serve --daemon client--> game-forged -> same pipeline
```

- `/mcp` is mounted inside the daemon's one listener. The frontend calls the
  daemon's dispatcher **in process** — no HTTP loop into `/v1/run` — but runs
  the identical `prepare`+`exec` pipeline: same project resolution, argument
  and output validation, run ids, cancellation and progress.
- `/mcp` authenticates with the **durable MCP credential**
  (`~/.game-forge/state/mcp.token`, 0600) — separate from the ephemeral
  per-incarnation control-API token, so client configuration survives
  restarts. Non-local `Origin` headers are rejected (DNS-rebinding guard).
  `game-forge mcp info [--show-token]` reports the endpoint and, only when
  asked, the credential.
- Tool names, descriptions and schemas derive from the Operation Registry
  catalog — one fetch at frontend construction, never per-operation requests.
- **Project selection is explicit per call**: every tool's input schema gains
  a required `project_code` property, injected at the transport layer and
  stripped before canonical validation. The daemon resolves it through the
  project registry; `unknown_project`, `project_unavailable` and
  `project_changed` are structured results. There is no session, no
  current-project state, and no per-project MCP process or endpoint.
- Operation `scenario.run` becomes tool `scenario_run` — dots map to
  underscores deterministically and collisions are rejected at startup.
- Results map to `structuredContent` (the authoritative canonical operation
  result); failures become `isError` results carrying the
  canonical `code`/`path`/`msg`. Semantic stage/artifact events become MCP
  progress notifications when the client supplied a progress token; MCP
  cancellation propagates to the daemon request and cancels the Core operation
  without harming reusable owned resources.
- Artifacts stay referenced (`kind`, `ref`, `path`), never inlined.
- `game-forge mcp serve` remains as the stdio compatibility frontend — same
  dispatcher contract (over the daemon HTTP client), same `project_code`
  routing, for MCP clients that cannot speak streamable HTTP.

### MCP efficiency contract

The interface is consumed by coding agents, so wire efficiency is part of
correctness — the same discipline as "no second Runtime":

- **Initialization**: one catalog fetch (in-process on `/mcp`, one
  `/v1/catalog` request over stdio) — never one schema request per operation.
- **project_code overhead is measured**: the injected transport property costs
  ~58 B/tool; the audit reports its serialized cost as `projectCodeBytes`.
- **Model surface**: `tools/list` carries name + description + input schema.
  Output schemas are omitted by default — the daemon still validates every
  result canonically, and a model needs input shape, not result shape, to
  choose and call a tool. `--full-schemas` re-enables them for clients that
  validate structured results.
- **Compact schema projection**: advertised schemas are the canonical
  documents minus `$schema`/`$id` — wire metadata with no validation
  semantics. The projection is deterministic and derived, never a second
  handwritten schema; a document containing `$ref` is left untouched because
  `$id` participates in resolution there.
- **No duplicated results**: compact mode returns `structuredContent` with an
  empty `content` array (standards-valid). `--compat-text` opts into mirroring
  the JSON for clients that predate `structuredContent`.
- **Dense errors/progress**: errors stay `code: msg (path)` capped at 512 B;
  identical consecutive progress notifications are suppressed and messages
  capped; artifacts never inline bytes.
- **Budget**: `internal/mcpfrontend/efficiency-budget.json` holds named limits
  (catalog/model-surface bytes, per-tool and average size, init requests,
  latency ceilings, representative call results). `game-forge mcp audit`
  measures the real serialized surface against it; `--json` feeds CI. The
  file is never auto-rebaselined — a raised limit needs a justification in
  the commit.

Out of scope for now: MCP resources/prompts, remote (non-loopback) MCP, OAuth,
public listeners. A future resource layer can expose artifacts more richly
over the same registry.


## 19. Asset pipeline architecture

Asset tools should live behind generic Game Forge capabilities.

The default model is:

```text
source/recipe
-> generator/importer
-> converter/compiler
-> validator
-> optimizer
-> built artifact
-> game runtime
```

Providers such as ffmpeg or AI generation services remain Game Forge implementation details.

Project runtime code should normally consume final artifacts.

See `DESIGN_PRINCIPLES.md` for audio/texture policy.

## 20. Migration from project-local tooling

Spin Tower currently contains proven development infrastructure in `./scripts/dev` and associated TypeScript helpers.

Migration is incremental.

Allowed transition:

```text
./scripts/dev shot ...
    -> game-forge shot ...
```

or the reverse wrapper direction temporarily when needed.

Do not remove a working project-local path until the Game Forge replacement is proven with the real consumer.

## 21. First vertical slice

The first implementation milestone should prove a narrow end-to-end slice:

1. Game Forge builds/tests.
2. global config is read;
3. native Windows agent-browser provider is resolved;
4. `game-forge doctor` validates it;
5. Spin Tower exposes `game-forge/v1`;
6. scenario list works;
7. one scenario runs headlessly;
8. the same scenario runs in native Windows headless Chrome;
9. authoritative output can be compared;
10. one screenshot is captured;
11. real Windows GPU renderer is detected;
12. owned resources are registered and cleaned;
13. abandoned-resource recovery can be demonstrated safely.

Do not implement the whole long-term CLI before this slice passes.

## 22. Provider replacement test

A useful architectural check:

> If agent-browser is replaced tomorrow, does a game repository need to change?

Desired answer: no.

Only global Game Forge configuration/provider implementation should change.

The same principle applies later to media tools, test tooling, and other external utilities.
