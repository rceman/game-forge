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
ownership and cleanup, a per-user control daemon, and future media/asset tooling.

A game must never need to know about agent-browser, Chrome/CDP, Playwright,
Windows interop, ffmpeg, GPU discovery, process cleanup, or daemon internals.
Those live behind Game Forge providers.

The architectural test: *if we create a second game tomorrow, how many Spin
Tower infrastructure files must we copy?* The answer should be **zero**.

## Status

The reusable harness is implemented and consumed by Spin Tower: project
discovery and configuration, native test/build orchestration, deterministic
scenario execution headless and in-browser, headless/browser comparison,
screenshots and visual sweep, browser diagnostics, dev/prod server ownership,
validation profiles, production smoke, GPU verification and benchmarking, an
owned-resource registry, and a per-user control daemon that owns housekeeping.

Every externally callable capability is a schema-defined **operation** behind a
single Operation Registry. The CLI and the MCP stdio server are thin frontends
that talk to a persistent per-user daemon (`game-forged`) over loopback HTTP;
the daemon owns resource lifecycle and periodic housekeeping. See
[Roadmap](docs/ROADMAP.md),
[Daemon protocol](docs/DAEMON_PROTOCOL_V1.md), and the
[Spin Tower migration matrix](docs/SPIN_TOWER_MIGRATION.md).

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
    # Automated runs are silent by default (--mute-audio). Set this to opt out.
    unmuted: false
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

game-forge scenario list       # discover scenarios from the adapter
game-forge scenario describe <id>
game-forge scenario run <id> [--browser] [--seed p] [--world p] [--ticks N] [--param k=v]
game-forge scenario compare <id|--all>

game-forge shot <case> [--ticks N] [--region R] [--out P] [--expr '<js>']
game-forge pixel <x,y> ...     # sample real rendered pixels (Go image decode)
game-forge sweep               # every declared visual case
game-forge eval [--case c] [--ticks N] [--click sel] [--press key] --expr '<js>'
game-forge errors              # fresh page/console diagnostics
game-forge gpu                 # renderer verification + benchmark

game-forge check               # project-declared profiles
game-forge test [-- <native filter>]
game-forge build
game-forge profile <name> [-- <native args>]
game-forge verify              # fast gate
game-forge verify full         # full acceptance
game-forge prod                # production build + smoke

game-forge serve start|status|stop   # own the declared dev/prod server
game-forge ps                  # list resources owned by Game Forge
game-forge gc                  # reclaim expired owned resources
game-forge tick                # one idempotent housekeeping pass

game-forge daemon status|stop|restart  # control the per-user daemon
game-forge mcp serve [--cwd DIR]         # expose Game Forge over MCP stdio

game-forge version
game-forge help
```

Most commands accept `--json` for a structured result and `--ndjson` for the
raw event stream. Ordinary commands transparently start and reuse the daemon;
you never need `daemon start` in normal use.

Exit codes are stable: `0` success, `1` failure, `2` usage error.

### Audio output

Game Forge launches the managed browser with `--mute-audio` by default, so
automated runs never reach your speakers. This suppresses physical output only:
the page's `AudioContext`, cue generation and audio counters stay live, so audio
checks remain meaningful. Pass `--unmuted` to any browser command (or set
`unmuted: true` in the machine config) to hear it deliberately.

### agent-browser provider notes

Two agent-browser behaviours are load-bearing, and both are covered by tests:

- `--args` is a **global** option. It must precede the subcommand; a copy after
  the subcommand is silently ignored. It is also split on commas, so no flag
  value may contain one.
- The daemon **relaunches Chrome with its default flags whenever an invocation
  omits `--args`**. Game Forge therefore repeats the managed flags on *every*
  call, not just on the launching one. Supplying them once and then calling
  `set viewport` was enough to lose audio suppression entirely.

A browser-launching call is also expected to write its response before the
launch finishes, so the provider reads the response and detaches rather than
killing the launcher immediately.

## Build

```sh
go build ./cmd/game-forge
go test ./...
```

Requires Go 1.24+.

## Documentation

- [Vision](docs/VISION.md) — product direction and system boundary.
- [Architecture](docs/ARCHITECTURE.md) — contract, providers, operation registry, daemon, resource ownership.
- [Daemon protocol](docs/DAEMON_PROTOCOL_V1.md) — loopback HTTP, discovery, auth, operations, NDJSON streaming.
- [Design Principles](docs/DESIGN_PRINCIPLES.md) — reusable-tooling and asset-pipeline principles.
- [Roadmap](docs/ROADMAP.md) — incremental extraction plan driven by real consumers.
- [Contract v1](docs/CONTRACT_V1.md) — the versioned project contract.
- [Spin Tower migration matrix](docs/SPIN_TOWER_MIGRATION.md) — what moved where, and what deliberately stayed.

Spin Tower (`rceman/td-game`) is the first real integration consumer.

## License

MIT — see [LICENSE](LICENSE).
