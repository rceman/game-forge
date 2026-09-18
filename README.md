# game-forge

Open-source game development harness for deterministic simulation, browser
testing, visual validation, GPU benchmarking, and reusable tooling.

Game Forge provides the reusable engineering environment around a game without
owning game-specific mechanics or forcing projects onto one engine or runtime.

> **The game knows the game. Game Forge knows the development tooling.**

## The boundary

A **game** owns its gameplay, authoritative simulation, game-specific state,
scenarios and assertions, UI/presentation, runtime assets, and a thin adapter
that implements the Game Forge contract.

**Game Forge** owns the reusable development infrastructure: project discovery
and configuration, build/test orchestration, deterministic scenario execution,
headless and browser execution, screenshots and visual validation, browser
diagnostics and lifecycle, production smoke, GPU verification, process/resource
ownership and cleanup, scheduling, and future media/asset tooling.

A game must never need to know about agent-browser, Chrome/CDP, Playwright,
Windows interop, ffmpeg, GPU discovery, process cleanup, or scheduler internals.
Those live behind Game Forge providers.

The architectural test: *if we create a second game tomorrow, how many Spin
Tower infrastructure files must we copy?* The answer should be **zero**.

## Status

Early. The first vertical slice is proven: machine config is read, the native
Windows agent-browser provider is resolved and health-checked, a bounded
open/eval/close round trip runs headlessly, and browser resources are registered
and reclaimed. See [Roadmap](docs/ROADMAP.md).

## Configuration

Two layers, deliberately separate:

- **Machine/tool configuration** — `~/.game-forge/config.yaml`. Provider-specific
  executable paths (agent-browser, Chrome, ffmpeg) belong here and are never
  committed to a game repository.
- **Project configuration** — `<project>/game-forge.yaml`. Declares the contract,
  capabilities and adapters. No machine-specific paths.

Example machine config:

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

Example project manifest:

```yaml
contract: game-forge/v1

project:
  id: spin-tower
  type: web-game

capabilities:
  simulation: true
  browser: true
  visual: true

adapters:
  simulation:
    command: [npx, tsx, scripts/simulate.ts]
    protocol: jsonl
```

## Commands

```
game-forge doctor              # validate config and the browser provider
game-forge project info        # show the nearest project manifest
game-forge ps                  # list resources owned by Game Forge
game-forge gc                  # reclaim expired owned resources
game-forge version
game-forge help
```

Exit codes are stable: `0` success, `1` failure, `2` usage error.

## Build

```sh
go build ./cmd/game-forge
go test ./...
```

Requires Go 1.24+.

## Documentation

- [Vision](docs/VISION.md) — product direction and system boundary.
- [Architecture](docs/ARCHITECTURE.md) — contract, providers, resource ownership, scheduler, first slice.
- [Design Principles](docs/DESIGN_PRINCIPLES.md) — reusable-tooling and asset-pipeline principles.
- [Roadmap](docs/ROADMAP.md) — incremental extraction plan driven by real consumers.
- [Contract v1](docs/CONTRACT_V1.md) — the versioned project contract.

Spin Tower (`rceman/td-game`) is the first real integration consumer.

## License

MIT — see [LICENSE](LICENSE).
