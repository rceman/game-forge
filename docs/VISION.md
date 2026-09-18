# Game Forge Vision

Status: canonical product direction for the Game Forge project.

## What Game Forge is

Game Forge is a language-agnostic game development and validation harness.

It is not a game engine and it is not a replacement for a game's runtime, simulation, renderer, or test framework.

Game Forge provides the reusable engineering environment around games so that a second game does not have to copy the development infrastructure of the first.

The core architectural test is:

> If we create another game tomorrow, zero infrastructure files should need to be copied from the previous game.

A new game should provide only its own product code, assets, scenarios, tests, and small Game Forge adapters/contracts.

## Core boundary

### The game owns

- gameplay and product behavior;
- authoritative simulation;
- game-specific state;
- game-specific scenarios and assertions;
- UI and presentation;
- final runtime assets;
- a small versioned Game Forge contract implementation.

### Game Forge owns

- project discovery and configuration;
- build/test orchestration;
- deterministic scenario execution;
- headless execution;
- browser execution;
- screenshots and visual validation;
- browser diagnostics and lifecycle;
- production smoke validation;
- GPU verification and benchmarking;
- process/resource ownership and cleanup;
- scheduled maintenance;
- reusable asset generation/validation tooling;
- future MCP and CI interfaces.

The game must not know whether Game Forge internally uses agent-browser, Chrome/CDP, ffmpeg, a specific test runner, PowerShell, Windows interop, or another provider.

Providers are implementation details of Game Forge.

## One contract, many execution modes

A game-specific scenario should be defined once and reusable through multiple execution modes:

```text
                    Game scenario
                         |
                authoritative runtime
                  /              \
             headless            browser
                |                  |
              Game Forge orchestration
```

Game Forge should be able to execute the same semantic scenario headlessly and in a browser without duplicating arrangement logic.

Long-term examples:

```text
game-forge scenario list
game-forge scenario run archer-standoff
game-forge scenario run archer-standoff --browser
game-forge scenario compare archer-standoff
```

The game owns scenario semantics. Game Forge owns discovery, invocation, deterministic metadata, reporting, and execution mode.

Game Forge must never learn game-specific concepts such as Tower, Archer, Gun, Coins, or Wave.

## Language-agnostic by design

Game Forge itself is implemented in Go, but consumers may be implemented in:

- TypeScript/JavaScript;
- Rust/WASM;
- Go;
- Python;
- other runtimes.

The contract must therefore be process/language neutral.

For authoritative/headless adapters, JSONL over stdio is the preferred v1 transport because it is simple, inspectable, deterministic, and does not require a background service.

MCP is a future external interface, not the internal project protocol.

## Browser automation belongs to Game Forge

A game should expose a small browser-side contract, conceptually:

```text
window.__gameForge
```

with capabilities such as scenario loading, deterministic advancement, snapshots, diagnostics, and rendering metrics.

The game does not know what controls that bridge.

Game Forge owns:

- browser provider selection;
- browser launch/close;
- navigation;
- JavaScript evaluation;
- screenshots;
- error collection;
- native GPU verification;
- cleanup.

This allows browser tooling to be replaced without changing game code.

## Machine config and project config are separate

Project-level configuration belongs in the project, for example:

```text
game-forge.yaml
```

It describes capabilities, adapters, scenarios, validation profiles, and project expectations.

Machine/provider configuration belongs in:

```text
~/.game-forge/config.yaml
```

It may contain:

- selected browser provider;
- path to native agent-browser;
- Chrome path;
- Windows/Linux provider settings;
- local tool discovery results.

User-specific executable paths must never be committed to game repositories.

## Current browser direction

For the current WSL-based development machine, Game Forge should use native Windows agent-browser and native Windows Chrome.

The WSL copy of agent-browser is not required for browser/GPU workflows.

The browser provider is configured globally under `~/.game-forge/`.

Headless Windows Chrome is the default because it avoids popup windows while still using real hardware rendering.

The current hardware path has been manually proven with:

```text
ANGLE
NVIDIA GeForce RTX 4070
Direct3D11 / D3D11
```

Game Forge should verify the renderer itself before accepting GPU benchmark results and reject software fallbacks such as SwiftShader, WARP, llvmpipe, or Microsoft Basic Render Driver.

## Validation philosophy

Game Forge should orchestrate existing native test/build tools instead of replacing them.

A project may use Vitest, cargo test, go test, pytest, or another runner.

Game Forge normalizes the workflow and reporting:

```text
PASS build
PASS unit
PASS scenarios
PASS browser
PASS visual
PASS lifecycle
PASS production
PASS audio
PASS gpu
```

Not every project needs every capability.

Validation should remain layered:

```text
fast edit loop
    -> targeted check
    -> targeted scenario
    -> continue

visual loop
    -> deterministic scenario
    -> screenshot
    -> inspect
    -> recapture same case

milestone boundary
    -> broad/full verification
```

A concise log must never hide a failing exit status.

## Resource ownership and recovery

Any external resource started by Game Forge must be ownable and recoverable.

Examples:

- browser namespace/session;
- Chrome process;
- local web server;
- benchmark process.

Game Forge should persist resource records under `~/.game-forge/` with enough identity to clean only resources it owns.

It must never kill arbitrary processes by executable name.

Normal completion cleans resources immediately.

If an agent crashes or forgets cleanup, stale resources are recovered later through a lease/TTL model.

## Daemon, not OS scheduler

Game Forge owns a persistent per-user control daemon, `game-forged`, rather
than relying on an OS scheduler (cron / systemd timer / Scheduled Task).

The preferred model is:

```text
game-forge CLI
    -> loopback HTTP (dynamic port, bearer auth)
    -> game-forged
    -> Operation Registry -> Core
    -> periodic in-process Tick() reclaims expired owned resources
```

Ordinary commands transparently start and reuse the daemon; the daemon owns
resource lifecycle and housekeeping. There is no cron entry, systemd timer,
Windows Scheduled Task, or SYSTEM service.

User-facing commands:

```text
game-forge ps
game-forge gc
game-forge tick

game-forge daemon status
game-forge daemon stop
game-forge daemon restart
```

`game-forge tick` remains as a manual one-shot housekeeping primitive; the
daemon invokes the same Core Tick internally on a short cadence.

See [DAEMON_PROTOCOL_V1.md](DAEMON_PROTOCOL_V1.md) for the wire contract.

## Asset philosophy

The default direction is development/build-time generation followed by static runtime artifacts.

```text
Source / Recipe
      |
      v
Generate / Import
      |
      v
Compile / Convert
      |
      v
Validate / Normalize / Optimize
      |
      v
Built Artifact
      |
      v
Game Runtime
```

Runtime procedural generation is an explicit choice when dynamic generation is itself part of the product.

For ordinary SFX, music, textures, sprites, and similar content, prefer prepared artifacts.

See [DESIGN_PRINCIPLES.md](DESIGN_PRINCIPLES.md) for the detailed asset/audio rationale.

## Future Asset Lab

Game Forge should eventually provide generic asset tooling without forcing games to understand the underlying tools.

Possible future commands:

```text
game-forge audio generate
game-forge audio build
game-forge audio inspect
game-forge audio validate
game-forge audio compare

game-forge image inspect
game-forge image validate
game-forge asset build
```

Potential audio validation includes:

- duration;
- sample rate/channels;
- peak/true peak;
- RMS/LUFS;
- clipping;
- dynamic range/crest factor;
- head/tail silence;
- DC offset;
- spectral characteristics;
- family loudness consistency;
- duplicate/similarity detection;
- loop quality.

Spin Tower's current procedural Web Audio cues are a useful prototype/source-generator case, but runtime synthesis is not the default long-term production architecture.

## Future MCP and CI

CLI, MCP, CI, and scheduler should be interfaces over the same Game Forge core.

Conceptually:

```text
CLI ---------\
MCP ----------> Game Forge Core
CI ----------/
scheduler ---/
```

MCP should not become the internal project protocol.

## Incremental extraction rule

Game Forge is extracted from real workflows, not designed as a speculative framework.

For every capability:

1. identify the existing working implementation in a real game;
2. separate generic behavior from game-specific behavior;
3. define the minimum contract;
4. implement the generic capability in Game Forge;
5. integrate the real game consumer;
6. prove no regression;
7. only then remove duplicated project-local infrastructure.

Spin Tower is the first real consumer and integration test.

## First consumer

Spin Tower (`rceman/td-game`) is the first project used to prove the architecture.

The initial extraction targets capabilities already proven there:

- deterministic scenarios;
- headless execution;
- browser scenarios;
- screenshots;
- browser diagnostics;
- lifecycle/reset checks;
- production smoke;
- sound verification;
- Windows GPU validation;
- resource cleanup;
- validation profiles.

Spin Tower must continue progressing as a game during extraction.

Game Forge should not become a reason to freeze product development.

## Non-goals

Game Forge is not intended to:

- implement game mechanics;
- replace a game's simulation;
- replace Vitest/pytest/cargo test/go test;
- become a generic game scripting language;
- force every game onto one engine/runtime;
- expose provider-specific tooling to game code;
- require a permanently running cleanup service;
- implement speculative subsystems before a real consumer needs them.
