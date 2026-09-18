# Game Forge Design Principles

Status: architectural direction for Game Forge and its game consumers.

## 1. Core boundary

Game Forge is the reusable engineering environment around a game.

A game owns its product behavior and finished runtime inputs:

- gameplay code and authoritative simulation;
- game-specific scenarios and assertions;
- UI and presentation code;
- final runtime assets;
- a small versioned Game Forge adapter/contract implementation.

Game Forge owns development infrastructure:

- scenario orchestration;
- headless and browser execution;
- browser lifecycle and automation;
- screenshots and visual validation;
- native test-runner orchestration;
- build/tool invocation;
- GPU verification;
- process/resource cleanup;
- scheduled maintenance;
- asset generation, conversion, validation, and optimization;
- future MCP/CI integration.

The game must not know whether Game Forge internally uses agent-browser, Chrome/CDP, ffmpeg, a particular compiler, an audio generator, or another provider.

Changing a Game Forge provider must not require changing game logic.

## 2. A game consumes capabilities, not tools

Project configuration should describe what the game exposes or requires, not how external utilities are invoked.

Bad project coupling:

```yaml
browser:
  command: agent-browser ...
audio:
  command: ffmpeg ...
```

Preferred boundary:

```yaml
contract: game-forge/v1

capabilities:
  simulation: true
  browser: true
  visual: true
  audio: true
  gpu: true
```

Machine-specific provider configuration belongs under the user's Game Forge configuration, for example:

```text
~/.game-forge/config.yaml
```

Project repositories must not contain user-specific executable paths or provider-specific machine setup.

## 3. Reuse test

The architectural test for Game Forge is simple:

> If a second game is created tomorrow, no infrastructure files should need to be copied from the first game.

The new game should supply only its own:

- manifest;
- adapters;
- scenarios;
- tests;
- assets;
- product code.

Game Forge supplies the reusable harness.

## 4. Asset philosophy: generate before runtime by default

The default Game Forge asset pipeline is:

```text
Source / Recipe
     |
     v
Generate / Import
     |
     v
Convert / Compile
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

The shipped game should normally consume finished static artifacts.

Examples:

- audio -> WAV/OGG/etc. prepared before gameplay;
- textures -> PNG/WebP/KTX/etc. prepared before gameplay;
- sprites/atlases -> packed before gameplay;
- generated music/SFX -> rendered and validated before gameplay.

A generator is a development/build tool. It is not automatically part of the shipped game.

This is a design default, not a claim that procedural runtime generation is always expensive. Simple procedural audio or textures can be computationally cheap. Baking is preferred primarily because it improves:

- reproducibility;
- artistic review;
- deterministic QA;
- cross-browser/device consistency;
- asset validation;
- caching and startup/runtime simplicity;
- production debugging;
- ability to compare exact artifacts;
- separation of development tooling from game runtime.

## 5. Runtime procedural generation is an explicit exception

Runtime procedural generation is appropriate when the generation itself is part of the desired runtime behavior.

Examples include:

- particles;
- dynamic shader noise;
- water/fire/cloud effects;
- dynamic decals;
- terrain variation that must change during play;
- other effects whose variability is intentional product behavior.

It should not be used merely because generating a static artifact during development is convenient.

For ordinary SFX, music, painted textures, sprites, and similar content, prefer prebuilt artifacts unless there is a concrete product reason not to.

## 6. Audio direction

Game Forge should treat audio generation and audio playback as separate concerns.

### Development/build side

Game Forge may eventually provide:

```text
game-forge audio generate
game-forge audio build
game-forge audio inspect
game-forge audio validate
game-forge audio compare
```

Audio sources may be:

- hand-authored/imported files;
- procedural synthesis recipes;
- AI-generated material;
- other generator outputs.

The result should normally be one or more finished runtime audio files.

For variation, Game Forge can generate deterministic families ahead of time:

```text
hit-01.ogg
hit-02.ogg
hit-03.ogg
hit-04.ogg
```

The game may choose among these at runtime without synthesizing the sound itself.

### Validation side

Generic audio validation can eventually include:

- duration;
- sample rate and channels;
- peak / true peak;
- RMS / LUFS;
- clipping;
- crest factor / dynamic range;
- silence at head/tail;
- DC offset;
- frequency/spectral characteristics;
- loudness consistency within a sound family;
- duplicate/similarity detection;
- music loop quality.

The objective is to validate the exact artifacts that will ship.

## 7. Spin Tower procedural audio as the first extraction case

Spin Tower currently synthesizes its SFX at runtime with Web Audio primitives.

That implementation is useful as a prototype and as a potential first source/generator case for Game Forge, but it does not establish runtime synthesis as the preferred production architecture.

A future migration can be:

```text
current procedural cue definitions
        |
        v
Game Forge audio generator
        |
        v
deterministic rendered variants
        |
        v
Game Forge audio validation
        |
        v
approved static runtime assets
        |
        v
Spin Tower playback
```

The generated output should be listened to and evaluated artistically; a mathematically valid oscillator/noise recipe is not automatically a good game sound.

Until an audio asset pipeline exists, the current Spin Tower audio implementation can remain as-is. Building Game Forge must not block product progress.

## 8. Texture and image direction

The same principle applies to procedural textures and generated imagery.

Prefer:

```text
procedural / AI / authored source
        ->
Game Forge build pipeline
        ->
validated static texture/sprite
        ->
game runtime
```

Keep runtime procedural graphics where they provide actual dynamic value, especially shader/effect-driven visuals.

Do not make every player's machine regenerate static-looking content that can be prepared once during development.

## 9. Game Forge must remain language-agnostic

Game Forge itself is expected to be a Go toolchain, but game projects may use:

- TypeScript/JavaScript;
- Rust/WASM;
- Go;
- Python;
- other runtimes.

Project adapters provide the versioned boundary.

Game Forge may use different providers internally without exposing those providers to game code.

## 10. Incremental extraction rule

Do not build speculative subsystems merely because they fit this vision.

For each capability:

1. start from a real workflow already required by a game;
2. identify the generic part;
3. define the smallest contract;
4. implement it in Game Forge;
5. integrate the real game consumer;
6. prove the replacement works;
7. only then remove duplicated project-local infrastructure.

Spin Tower is the first real consumer and integration test.

Asset Lab, audio generation, image tooling, and MCP are future capabilities unless current product work provides a concrete reason to implement them now.
