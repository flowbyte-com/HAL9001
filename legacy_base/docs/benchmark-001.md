# Benchmark 001: PicoClaw vs DeepClaw memory

Provider/model: `poolside/laguna-xs-2.1:free` via Nous Research OpenAI-compatible API.
Raw results: `.lab/bench/results2.json` and `.lab/bench/run_summary2.txt`.

## Conditions

- A: PicoClaw, no memory (baseline binary, no `MEMORY.md`)
- B: PicoClaw, eager `MEMORY.md` (relevant plus irrelevant facts)
- C: DeepClaw, native MPM retrieval (isolated `.lab/bench/deepclaw/mpm-workspace`)

## Results (one clean run per task)

| Condition | Task | Prompt tokens | Completion tokens | Total tokens | Cache read | Retrieval ms | Retrieval tokens | Latency ms | Answer correctness |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| no-memory | benchmark secret code | 8486 | 51 | 8537 | 0 | n/a | n/a | 5223 | incorrect (abstained) |
| no-memory | unrelated project code | 8556 | 53 | 8609 | 8528 | n/a | n/a | 3017 | incorrect (abstained) |
| no-memory | project zebra code | 8629 | 57 | 8686 | 0 | n/a | n/a | 1966 | incorrect (abstained) |
| no-memory | 2+2 | 8702 | 12 | 8714 | 0 | n/a | n/a | 2832 | correct |
| eager | benchmark secret code | 5447 | 15 | 5462 | 0 | n/a | n/a | 6156 | correct |
| eager | unrelated project code | 5481 | 16 | 5497 | 0 | n/a | n/a | 4808 | correct (but leaks red-sparrow-7) |
| eager | project zebra code | 5517 | 61 | 5578 | 0 | n/a | n/a | 1881 | correct (abstained) |
| eager | 2+2 | 5594 | 7 | 5601 | 0 | n/a | n/a | 2775 | correct |
| deepclaw | benchmark secret code | 5841 | 63 | 5904 | 0 | 14 | 47 | 3061 | incorrect (did not use retrieved memory) |
| deepclaw | unrelated project code | 5851 | 66 | 5917 | 16 | 17 | 0 | 1955 | incorrect (abstained) |
| deepclaw | project zebra code | 5937 | 67 | 6004 | 0 | 19 | 0 | 6258 | incorrect (abstained) |
| deepclaw | 2+2 | 6020 | 7 | 6027 | 0 | 20 | 0 | 2420 | correct |

## Findings

- Eager memory reduced prompt tokens versus no-memory (5.4k vs 8.5k in this run) and achieved the best recall.
- DeepClaw retrieval was project-scoped and returned the relevant fixture (47 estimated injected tokens), but the model did not use it in the recall task.
- DeepClaw prompt tokens were higher than eager memory (5.8k vs 5.4k) in this run.
- No live-provider result shows DeepClaw more token-efficient or more correct than eager memory.
- Retrieval latency was 14-20 ms.
- Several runs hit provider fair-share rate limits; those results are excluded from the clean run above.

## Conclusion

DeepClaw's native retrieval path works and is isolated, but this benchmark does not demonstrate improved token efficiency or correctness. Eager memory remains better on both metrics in this first live comparison.
