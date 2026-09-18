# Game Forge Contract v1

The contract identifier is:

```
game-forge/v1
```

It is the stable, language-agnostic handshake between a game project and Game
Forge. It is deliberately small. It describes **what a project can do**, never
how Game Forge does its job.

Game Forge discovers a project by walking upward from the current directory
until it finds `game-forge.yaml`.

## Project manifest

`<project>/game-forge.yaml`:

```yaml
contract: game-forge/v1

project:
  id: spin-tower
  type: web-game

capabilities:
  simulation: true
  browser: true
  visual: true
  audio: true
  gpu: true

adapters:
  simulation:
    command: [npx, tsx, scripts/simulate.ts]
    protocol: jsonl
```

### Rules

- `contract` must be a version this build understands.
- `project.id` is required and stable.
- `project.type` is a free-form label (`web-game`, `native-game`, ...).
- `capabilities` declares which generic capabilities the project supports.
  Capability names are generic: `simulation`, `browser`, `visual`, `audio`,
  `gpu`. Game-specific nouns (floors, guns, waves, coins, ...) are forbidden.
- `adapters` maps a capability name to a transport declaration. Adapters carry
  no game semantics — only how to launch and speak to the project.

**Machine-specific values must never appear in this file.** Executable paths,
Chrome locations, PowerShell details and similar machine facts belong in
`~/.game-forge/config.yaml`.

## Capabilities

| Capability   | Meaning                                              |
|--------------|------------------------------------------------------|
| `simulation` | Exposes a deterministic headless runtime adapter.    |
| `browser`    | Exposes the browser contract (`window.__gameForge`). |
| `visual`     | Produces deterministic screenshots / named regions.  |
| `audio`      | Exposes audio inspection metadata.                   |
| `gpu`        | Provides a renderer/benchmark scenario.              |

## Headless adapter protocol

Game Forge controls a game's authoritative runtime over **JSONL over stdio**:
one JSON request per line, one JSON response per line. No HTTP, no MCP, no
service lifecycle. This keeps the contract inspectable, deterministic and
usable from TypeScript, Go, Rust, Python and others.

Request envelope:

```json
{"id": 1, "op": "capabilities", "params": {}}
```

Response envelope:

```json
{"id": 1, "ok": true, "result": {}, "error": null}
```

Generic operations:

| Operation           | Purpose                                             |
|---------------------|-----------------------------------------------------|
| `capabilities`      | Report supported operations and contract version.   |
| `scenario.list`     | List scenario ids and summaries.                    |
| `scenario.describe` | Describe one scenario: parameters, ticks, seeds.    |
| `scenario.run`      | Run one scenario and return authoritative results.  |
| `snapshot`          | Return the current authoritative state digest.      |
| `diagnostics`       | Return runtime diagnostics.                         |

Operations stay generic. Game Forge must never understand game entities such as
towers, guns, archers or waves.

## Browser contract

A browser-capable project exposes a small, generic bridge on the page:

```js
window.__gameForge = {
  capabilities(): { contract: "game-forge/v1", ops: [...] },
  scenarioList(): [...],
  scenarioDescribe(id): ...,
  scenarioRun(id, options): ...,
  advance(ticks): ...,
  snapshot(): ...,
  digest(): ...,
  diagnostics(): ...,
  metrics(): ...,
  region(name): ...,
  visual: { list(): [...], load(case, options): ... },
  benchmark(frames): ...,
  checks: { ... },   // project-owned assertions Game Forge invokes and reports
};
```

The bridge knows the **contract**. It does not know agent-browser, CDP,
PowerShell, Windows, or Chrome lifecycle. Game Forge drives those externally.

`checks` is deliberately open-ended: it exposes the project's own product
assertions (UI flow, lifecycle, presentation independence, audio, diagnostics
views). Game Forge executes them, times them and propagates their pass/fail
status without understanding what they mean.

The minimum surface is whatever the first consumer actually needs; it grows only
when a real consumer requires it.

## Browser audio output

Game Forge suppresses physical audio output for every managed browser session
(`--mute-audio` on the Chrome/agent-browser provider). This is a **provider-level**
guarantee, not a project concern:

- the game's own mute state (`toggleMute`, `setMuted`, persisted preference) is
  never touched by Game Forge;
- the page's `AudioContext` remains active, cue generation and rate limiting keep
  running, and audio counters stay testable;
- only system playback is suppressed.

Projects opt out per-invocation with `--unmuted`, or machine-wide with
`browser.agent_browser.unmuted: true`. A project must not contain
provider-specific mute flags.

## Versioning

- The identifier carries a major version: `game-forge/vN`.
- Additive, backward-compatible changes stay within a major version.
- A breaking change to the manifest shape, the JSONL envelopes or the browser
  bridge increments the major version and ships a new document.
- Game Forge rejects a project whose contract major it does not implement, with
  an explicit error — never a silent fallback.
