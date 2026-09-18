# Spin Tower migration matrix

This document records how the development harness that originally lived inside
Spin Tower (`td-game/scripts/dev`) was separated into **Game Forge** (reusable
infrastructure) and **Spin Tower** (game semantics).

The boundary is unchanged:

```
THE GAME KNOWS THE GAME.
GAME FORGE KNOWS THE DEVELOPMENT TOOLING.
```

Game Forge controls through contracts; it never learns concepts such as Archer,
Floor, Gun or Wave.

## Classification key

| Code | Meaning |
| --- | --- |
| **A** | Generic infrastructure — belongs fully in Game Forge. |
| **B** | Generic orchestration + game-specific implementation — Game Forge owns the mechanism, Spin Tower supplies a thin adapter/bridge/profile. |
| **C** | Truly game-specific — stays in Spin Tower because its semantics are Spin Tower-specific. |

## Command matrix

Every command the old `scripts/dev` exposed, and where it lives now.

| Old command | Original implementation | Class | Game Forge mechanism | Remaining td-game responsibility | Status |
| --- | --- | --- | --- | --- | --- |
| `check` | `npx tsc --noEmit` | A | `check` profile (`game-forge check`) | command declaration in `game-forge.yaml` | migrated |
| `test [pattern]` | `npx vitest run` | A | `test` profile (`game-forge test -- <pattern>`) | command declaration | migrated |
| `build` | `npm run build` | A | `build` profile (`game-forge build`) | command declaration | migrated |
| `sim <scenario>` | `npx tsx scripts/simulate.ts --scenario` | B | `game-forge scenario run <id>` over the JSONL adapter | scenario definitions, arrangement, checks | migrated |
| `sim <policy>` / `--list` / `--selfcheck` | `npx tsx scripts/simulate.ts` | B | `policy` profile (`game-forge profile policy -- <args>`); Game Forge executes it | balance policy runner | retained |
| `scenarios` | `scripts/simulate.ts --list` | B | `game-forge scenario list` (discovered from the adapter) | scenario registry | migrated |
| `scenario <id>` | `scripts/simulate.ts --scenario` | B | `game-forge scenario run <id>` | scenario registry | migrated |
| `digest [fixture] [ticks]` | inline `ab eval` + `digestModule` | B | `game-forge eval --expr "window.__gameForge.checks.digest(...)"` | digest implementation, bridge check | migrated |
| `seeds` | inline `ab eval` + `b.seeds()` | B | `game-forge eval --expr "window.__gameForge.checks.seeds()"` | seed presets, bridge check | migrated |
| `shot <fixture> [ticks] [crop]` | `ready` + `ab eval` + `ab screenshot` + PIL crop | A | `game-forge shot <case> --ticks N --region R --out P` | fixture/scenario definitions, named regions | migrated |
| `colliders <fixture> [ticks]` | `ab eval` (overlay) + `ab screenshot` | B | `game-forge shot --expr "window.__spinTower.setColliderOverlay(true)"` | collider-overlay semantics | migrated |
| `diag <fixture> [ticks]` | `ab eval` + `diagnostics()` | B | `game-forge eval --expr "window.__gameForge.checks.diag(...)"` | diagnostics object | migrated |
| `ui <fixture> [ticks]` | `ab eval` + DOM visibility probe | B | `game-forge eval --expr "window.__gameForge.checks.ui(...)"` | UI selectors/overlay semantics | migrated |
| `pixel <x> <y> ...` | `ab screenshot` + PIL sample | A | `game-forge pixel <x,y> ...` (Go `image/png` decode, no Python) | capture case/region semantics | migrated |
| `eval [fixture] --expr` | `ready` + `ab eval` | A | `game-forge eval [--case] [--ticks] [--click] [--press] [--reload] --expr` | the expression itself | migrated |
| `reload` | `ab reload` + bridge probe | A | `game-forge eval --reload --expr ...` | — | migrated |
| `click <sel>` | `ab click` | A | `game-forge eval --click <sel> --expr ...` | — | migrated |
| `key <key>` | `ab press` | A | `game-forge eval --press <key> --expr ...` | — | migrated |
| `errors [--raw]` | `ab errors` + noise filter + dedupe | A | `game-forge errors` (or `game-forge eval --errors`) | — | migrated |
| `sweep [ticks]` | inline `ab eval` iterating `listFixtures()` | A | `game-forge sweep` over `visual.list` | visual case list | migrated |
| `bench [fixture] [ticks] [frames]` | `ab eval` + synchronous `present()` loop | B | `game-forge eval --expr "window.__gameForge.benchmark(N)"` / `game-forge gpu` | benchmark semantics + render metrics | migrated |
| `flow` | inline `ab eval` driving the DOM | B | `game-forge eval --expr "window.__gameForge.checks.flow()"` | floor-management UI assertions | migrated |
| `lifecycle [cycles]` | inline `ab eval` | B | `game-forge eval --expr "window.__gameForge.checks.lifecycle(N)"` | reset/restart assertions | migrated |
| `sound` | inline `ab eval` + synthetic click | B | profile stage with `click: ".start-btn"` (trusted input via the provider) | audio cue assertions | migrated |
| `present-check [id]` | inline `ab eval` | B | `game-forge eval --expr "window.__gameForge.checks.presentationIndependence(id)"` | clean-vs-noisy digest proof | migrated |
| `selfcheck` | `scripts/simulate.ts --selfcheck` | B | `selfcheck` profile (`game-forge profile selfcheck`), reused by `verify_full` | negative-case scenarios | retained |
| `prod` | build + `vite preview` + `ab open` + DOM probe | A | `prod` profile (`game-forge prod`) — Game Forge owns the prod server and browser | the production DOM assertions | migrated |
| `verify` | shell composition of stages | A | `verify` profile (`game-forge verify`) | stage list | migrated |
| `verify-full` | shell composition of stages | A | `verify_full` profile (`game-forge verify full`) | stage list | migrated |
| `serve [start\|stop]` | `npm run dev` + `pkill -f vite` | A | `game-forge serve start\|status\|stop`; Game Forge starts, waits, registers, reuses and stops it | server command declaration | migrated |
| `session [start\|stop]` | `agent-browser open/close` | A | owned by the provider; `game-forge ps` / `game-forge gc` | — | migrated |
| `browser [status\|stop\|prune]` | daemon/RSS inspection, watchdog control | A | `game-forge ps` / `game-forge gc` | — | migrated |
| `GPU probe` (implicit in `bench`/prod) | none — manual only | A | `game-forge gpu` | benchmark scenario + budget | migrated |

## Generic machinery removed from Spin Tower

| Removed from `td-game/scripts/dev` | Now owned by |
| --- | --- |
| `agent-browser` invocation, socket directory, launch flags | `internal/browser/agentbrowser.go` |
| Chrome executable path / launch arguments | `internal/config` + provider `LaunchArgs` |
| Windows/WSL PowerShell interop and quoting | `internal/browser/agentbrowser.go` |
| Browser daemon start/reuse/close | provider `Open`/`Close`, `OpenSession` |
| Detached watchdog + heartbeat + TTL | owned-resource leases (`internal/process`) |
| `pgrep`/`/proc` process counting and pruning | `game-forge ps` / `game-forge gc` |
| Vite dev/preview start, readiness, `pkill` | `internal/server` |
| Screenshot capture and PIL crop | `internal/visual` (`shot --region`) |
| Console/page error collection, noise filter, dedupe | `internal/browser` + `internal/visual` |
| GPU renderer probing | `internal/gpu` |
| Validation-stage runner and reporting | `internal/profile` |
| Scenario transport (JSONL) | `internal/adapter` |
| Resource registry / lease / `gc` / `tick` / scheduler | `internal/process`, `internal/scheduler` |

## Game-specific logic intentionally retained in Spin Tower

| Retained | Why it cannot move |
| --- | --- |
| `src/sim/scenarios.ts` scenario definitions, arrangement, parameters, checks | Product semantics; Game Forge must not know Archer/Floor/Gun/Wave. |
| `src/debug/bridge.ts` fixtures and the `__spinTower` debug bridge | Gameplay state construction and game-specific debug semantics. |
| `src/sim/digest.ts` state/layout/spawn-plan digests | Authoritative game digest definition. |
| `src/sim/seeds.ts` presets and resolution | Game-specific determinism policy. |
| `src/debug/gameForge.ts` `checks.*` (flow, lifecycle, presentation-independence, sound, ui, diag, digest, seeds) | Spin Tower assertions; Game Forge only invokes and reports them. |
| Named visual regions (`captureRegion`) | Game-specific framing. |
| Renderer metrics and the benchmark workload | Game-specific measurement. |
| Balance policy runner and self-check | Game balance semantics. |
| Audio cue semantics | Product behavior. |

## `scripts/dev` today

`scripts/dev` is a compatibility wrapper: it maps the familiar command names
onto `game-forge` and supplies the game-specific expressions. It contains no
browser provider logic, no agent-browser invocation, no Chrome flags, no
Windows/WSL interop, no daemon/TTL/watchdog management, no resource registry,
no server ownership, no screenshot orchestration, no GPU probing and no
validation runner.

Everything it does is either a one-line delegation or a single-line bridge
expression.

## Direction of control

```
Game Forge
    |
    | controls through contracts (game-forge.yaml, JSONL adapter, window.__gameForge)
    v
td-game adapters
    |
    v
Spin Tower semantics
```
