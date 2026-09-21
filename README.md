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
single Operation Registry. The CLI and MCP frontends are thin layers that talk
to one persistent per-user daemon (`game-forged`) over loopback HTTP; the
daemon owns resource lifecycle and periodic housekeeping. See
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

game-forge start|stop|restart|status  # human-facing service lifecycle
game-forge daemon install|uninstall   # per-user autostart (systemd --user)
game-forge daemon serve|rebind        # worker process / port recovery
game-forge mcp info [--json] [--show-token]    # the canonical MCP endpoint
game-forge mcp serve                           # stdio compatibility frontend
game-forge mcp audit [--json]                  # MCP wire-efficiency vs budget

game-forge version
game-forge help
```

## Projects and MCP

MCP agents select a project per call by code — there is no session, no
"current project", and no per-project MCP process. Register once on this
machine:

```sh
cd ~/git/td-game
game-forge project add TDG        # shorthand: discover the project from cwd
# or explicitly, from anywhere:
game-forge project add --code TDG --folder ~/git/td-game

game-forge project list
game-forge project show TDG
game-forge project remove TDG
```

`project add` walks upward to the directory containing `game-forge.yaml`, so
it works from any nested directory, and refuses to silently remap an existing
code — pass `--replace` to update deliberately. The registry lives at
`~/.game-forge/state/projects.json` and is read by the daemon on every call,
so changes are visible immediately.

Three identities stay distinct: the **code** (`TDG`) is a stable alias you
choose; the **root** is the machine-local manifest directory; the
**project key** (`spin-tower-37de03`) is the internal collision-safe resource
identity.

The daemon binds **one durable loopback port** (`50000-59999`, persisted in
`~/.game-forge/state/endpoint.json`) and rebinds it across restarts, so an MCP
client is configured once:

```json
{ "url": "http://127.0.0.1:<port>/mcp" }
```

`/mcp` requires the durable MCP credential (`~/.game-forge/state/mcp.token`,
shown by `game-forge mcp info --show-token`) and rejects non-local `Origin`
headers. Every tool takes a required `project_code` argument:

```text
project_info(project_code="TDG")
scenario_run(project_code="TDG", id="weapons")
visual_shot(project_code="TDG", case="starting-tower")
```

If the persisted port is ever occupied at startup the daemon fails loudly —
run `game-forge daemon rebind` to choose a new port deliberately (MCP client
config then points at the new endpoint; the port never moves silently).

By default `/mcp` returns results as `structuredContent` only (compact mode).
If a client cannot consume `structuredContent`, opt that machine into text
mirroring with `mcp.compat_text: true` in `~/.game-forge/config.yaml` — an
explicit per-machine choice; the default stays compact.

`mcp serve` remains as a stdio compatibility frontend over the same daemon
and the same per-call `project_code` routing.

## Lifecycle

`game-forge start|stop|restart|status` are the human-facing lifecycle
commands; their meaning is the same whether or not a native service is
installed. Without one, `start` spawns a detached `daemon serve` and waits for
health. On Linux/WSL with a working `systemd --user`, `game-forge daemon
install` writes `~/.config/systemd/user/game-forged.service` pointing at the
current binary, enables it, and hands lifecycle to systemd — `stop`/`restart`
then go through `systemctl --user`, crashes are restarted by
`Restart=on-failure`, and `daemon uninstall` removes the unit while keeping
the registry, port and credentials.

Autostart boundary: an enabled unit starts when the user's systemd
environment comes up. On WSL that means once the distribution's systemd/user
environment starts — Windows login alone does not launch the WSL VM, so
WSL-side autostart only proves out after WSL itself is running.

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
