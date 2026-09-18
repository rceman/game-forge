# Game Forge Roadmap

Status: directional roadmap. Milestones should be implemented only when backed by a real consumer need.

## Milestone 0 — Bootstrap

Goal: establish the repository and architectural boundary.

Deliverables:

- Go module and CLI skeleton;
- public README and docs;
- global config loader for `~/.game-forge/config.yaml`;
- project discovery via `game-forge.yaml`;
- contract version parsing;
- `game-forge doctor`;
- deterministic unit tests for core config/project discovery.

Exit criterion:

Game Forge can locate a project, load global + project config, and report provider readiness without knowing any game-specific concepts.

## Milestone 1 — First real vertical slice with Spin Tower

Goal: prove that Game Forge can replace one real project-local workflow end to end.

Deliverables:

- `game-forge/v1` project contract;
- headless adapter protocol over JSONL/stdin-stdout;
- browser bridge contract;
- native Windows agent-browser provider;
- headless browser launch by default;
- scenario discovery;
- one shared Spin Tower scenario executed headlessly;
- same scenario executed in browser;
- deterministic metadata/reporting;
- one screenshot;
- browser operation error propagation;
- resource registration and cleanup;
- real Windows GPU renderer detection.

Suggested consumer scenario:

`archer-standoff` or another already stable deterministic Spin Tower case.

Exit criterion:

The same game-specific scenario definition can be driven through Game Forge in headless and browser modes without duplicating arrangement logic.

## Milestone 2 — Browser and visual workflow extraction

Goal: move reusable browser development infrastructure out of Spin Tower.

Candidate deliverables:

- `game-forge shot`;
- named regions/crops;
- deterministic screenshot metadata;
- browser diagnostic collection/deduplication;
- visual sweep orchestration;
- lifecycle/reset orchestration;
- production smoke helper;
- compatibility wrappers from `td-game/scripts/dev`.

Exit criterion:

A second web game can obtain deterministic browser screenshots and browser diagnostics without implementing browser automation.

## Milestone 3 — Validation profiles

Goal: standardize reusable verification without replacing native test frameworks.

Candidate deliverables:

- `game-forge test`;
- `game-forge verify fast`;
- `game-forge verify full`;
- provider-neutral command/test runner abstraction;
- stable exit status propagation;
- compact standardized summaries;
- build/typecheck/test/scenario/browser profile composition.

Exit criterion:

Different game stacks can map their native validation tools into the same Game Forge workflow and report model.

## Milestone 4 — Resource registry and scheduled cleanup

Goal: make agent-driven development robust when commands crash or forget cleanup.

Candidate deliverables:

- durable resource records;
- run IDs;
- leases/TTL;
- `game-forge ps`;
- `game-forge gc`;
- `game-forge tick`;
- per-user scheduler install/status/uninstall;
- Windows Scheduled Task backend;
- Linux per-user timer backend.

Exit criterion:

Abandoned Game Forge-owned browser/server resources are recoverable without killing unrelated user processes and without a permanently running cleanup daemon.

## Milestone 5 — Hardware/GPU acceptance

Goal: make real-hardware rendering verification reusable across games.

Candidate deliverables:

- `game-forge gpu`;
- hardware renderer interrogation;
- software fallback rejection;
- deterministic warmup/measurement contract;
- standardized renderer/benchmark report;
- Windows native Chrome/ANGLE provider support;
- optional headed diagnostic mode;
- project-defined canonical benchmark and budget.

Exit criterion:

A game can request hardware rendering acceptance without implementing Windows/browser/GPU orchestration.

## Milestone 6 — Simulation Lab

Goal: generalize deterministic simulation workflows where real consumers need them.

Candidate deliverables:

- scenario describe/run/compare;
- parameter schema transport;
- headless/browser authoritative comparisons;
- deterministic run IDs and reproduction metadata;
- replay/snapshot hooks where projects expose them;
- batch scenario execution.

Non-goal:

Game Forge must not implement a generic game simulation DSL.

Exit criterion:

Multiple games can expose their own authoritative scenario systems through one external Game Forge interface.

## Milestone 7 — Asset Lab: audio first

Goal: move reusable asset generation/validation out of game runtime/development scripts.

Candidate deliverables:

- `game-forge audio inspect`;
- `game-forge audio validate`;
- audio policy/config contract;
- peak/true-peak/RMS/LUFS/clipping/duration analysis;
- family loudness consistency;
- duplicate/similarity analysis;
- optional deterministic procedural audio build;
- format conversion/normalization through a provider such as ffmpeg.

Spin Tower candidate:

Use its current Web Audio procedural cue definitions as a source/generator prototype, render deterministic variants, validate them, then consider migrating the game to approved static runtime assets.

Exit criterion:

The same audio QA pipeline can validate files or generated artifacts for multiple games.

## Milestone 8 — Asset Lab: image/texture pipeline

Candidate deliverables:

- image/texture inspection;
- dimension/alpha/color-space validation;
- duplicate detection;
- compression/format conversion;
- atlas checks;
- procedural/AI source -> validated static artifact pipeline.

Exit criterion:

A second game can reuse image/texture QA and build steps without copying project-specific scripts.

## Milestone 9 — MCP and CI interfaces

Goal: expose the same Game Forge Core through additional frontends.

Candidate deliverables:

- `game-forge mcp serve`;
- CI-friendly machine-readable output;
- stable capability discovery;
- safe action boundaries;
- no duplicate implementation between CLI and MCP.

Architecture rule:

```text
CLI ---------\
MCP ----------> Game Forge Core
CI ----------/
scheduler ---/
```

MCP must not become the internal game adapter transport.

## Prioritization rule

Milestones are not a promise to implement everything in order.

Before adding a capability ask:

1. Is there a real current game workflow that needs it?
2. Is the behavior generic across games?
3. Can the minimum contract be proven with a real consumer?
4. Can we avoid copying infrastructure into the next game?

If the answer is no, keep the functionality project-local until the boundary is clearer.

## Extraction rule

Never perform a big-bang migration.

For every extracted feature:

```text
working project implementation
    ->
identify generic boundary
    ->
Game Forge implementation
    ->
real consumer integration
    ->
regression proof
    ->
remove/reduce duplicate local tooling
```

Spin Tower remains the first integration consumer throughout the early roadmap.
