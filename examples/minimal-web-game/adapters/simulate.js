#!/usr/bin/env node
// Minimal JSONL-over-stdio headless adapter for the game-forge/v1 contract.
//
// Game Forge writes one JSON request per line and reads one JSON response per
// line. This stub answers `capabilities` and `scenario.list`; a real game
// answers `scenario.run`, `snapshot` and `diagnostics` from its authoritative
// simulation.
'use strict';

const readline = require('readline');

const CONTRACT = 'game-forge/v1';

function handle(req) {
  switch (req.op) {
    case 'capabilities':
      return { contract: CONTRACT, ops: ['capabilities', 'scenario.list', 'scenario.run'] };
    case 'scenario.list':
      return { scenarios: [{ id: 'smoke', summary: 'minimal deterministic run' }] };
    case 'scenario.run':
      return { scenario: req.params.id, ticks: req.params.ticks ?? 0, digest: 'stub' };
    default:
      throw new Error(`unsupported op: ${req.op}`);
  }
}

const rl = readline.createInterface({ input: process.stdin });
rl.on('line', (line) => {
  if (!line.trim()) return;
  let res;
  try {
    const req = JSON.parse(line);
    res = { id: req.id, ok: true, result: handle(req), error: null };
  } catch (err) {
    res = { id: null, ok: false, result: null, error: String(err && err.message || err) };
  }
  process.stdout.write(JSON.stringify(res) + '\n');
});
