# Benchmark 003: Active working memory

## Conditions

- A: PicoClaw without memory.
- B: PicoClaw with eager `MEMORY.md`.
- C: DeepClaw retrieval-only MPM.
- D: DeepClaw active working memory + MPM.

Provider/model: `poolside/laguna-xs-2.1:free` (Nous Research). Raw results: `.lab/bench/results_active.json`.

## Two-turn continuity results

| Condition | Turn 1 prompt tokens | Turn 2 prompt tokens | Two-turn prompt total | Turn 1 total tokens | Turn 2 total tokens | Retrieval ms (turn 1 / turn 2) |
|---|---:|---:|---:|---:|---:|---|
| no-memory | 9367 | 9404 | 18771 | 9388 | 9419 | n/a |
| eager | 6083 | 6114 | 12197 | 6098 | 6129 | n/a |
| deepclaw | 7268 | 7225 | 14493 | 7289 | 7246 | 11 / 19 |
| active | 7340 | 7375 | 14715 | 7361 | 7396 | 12 / 17 |

## Findings

- All four conditions answered the recall task correctly in this small run.
- Eager memory remains the most token-efficient.
- Active memory did not reduce prompt tokens versus retrieval-only; it slightly increased them due to the resident working-set text.
- Active memory did carry the MPM record across the second turn (retrieval returned zero new records on turn 2, while the resident fact remained present).
- Retrieval latency remained low (8-80 ms in prior runs, 11-19 ms here).

## Limitations

- Only one two-turn trial was run per condition.
- The free model has rate limits and variable latency.
- No improvement in token efficiency is demonstrated.
- Correctness is limited to a simple recall task.

## Conclusion

Active working memory improves continuity in principle and preserves the fact across turns, but it does not yet show a measurable token or correctness advantage over eager memory or retrieval-only DeepClaw.
