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

Game Forge Core should be reusable from multiple frontends:

```text
CLI ---------\
MCP ----------> Core
CI ----------/
scheduler ---/
```

The CLI is the first frontend.

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
  scheduler/
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

`tick` is the idempotent scheduler entrypoint.

## 17. Scheduler

No permanently running cleanup daemon is needed for v1.

Desired interface:

```text
game-forge scheduler install
game-forge scheduler status
game-forge scheduler uninstall
```

The OS scheduler periodically invokes:

```text
game-forge tick
```

Windows direction:

- per-user Scheduled Task;
- no SYSTEM service requirement.

Linux direction:

- per-user systemd timer where available;
- cron-compatible fallback may be considered if needed.

A one-minute cadence is adequate for early cleanup use cases.

## 18. Long-lived services

Future long-running capabilities such as:

```text
game-forge mcp serve
```

are not the same as scheduler cleanup.

They may use a proper service lifecycle later.

Do not introduce a generic permanent daemon merely because future MCP may require a server.

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
