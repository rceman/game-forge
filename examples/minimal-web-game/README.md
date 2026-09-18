# minimal-web-game

A skeleton showing the minimum a project needs to be a Game Forge consumer.

The whole project surface is:

- `game-forge.yaml` — the `game-forge/v1` contract: id, capabilities, adapters.
- `adapters/simulate.js` — a JSONL-over-stdio headless adapter (stub).
- `adapters/browser.js` — a JSONL-over-stdio browser adapter (stub).
- the game itself — gameplay, simulation, UI, assets (not shown).

There is **no** Game Forge code here. A second game copies none of Spin Tower's
infrastructure; it writes only its manifest, its adapters, its scenarios and its
assets.

Try it from this directory:

```sh
game-forge project info
```
