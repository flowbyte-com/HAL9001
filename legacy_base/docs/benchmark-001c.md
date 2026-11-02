# Benchmark 001C: Corrected memory presentation

## Conditions

- A: PicoClaw without memory.
- B: PicoClaw with eager `MEMORY.md` (relevant, irrelevant and poisoned facts).
- C: DeepClaw with native MPM retrieval and user-turn memory placement.

Provider/model: `poolside/laguna-xs-2.1:free` (Nous Research). Raw results: `.lab/bench/results3.json`.

## Aggregate provider-reported usage (2 trials, 4 tasks each)

| Condition | Mean prompt tokens | Mean completion tokens | Mean total tokens | Mean LLM calls | Mean tool calls | Mean retrieval ms | Mean e2e ms |
|---|---:|---:|---:|---:|---:|---:|---:|
| no-memory | 9045.5 | 53.5 | 9099.0 | 2.0 | 0.0 | n/a | 2900.9 |
| eager | 5871.2 | 38.0 | 5909.2 | 2.0 | 0.0 | n/a | 5112.8 |
| deepclaw | 6896.6 | 33.8 | 6930.4 | 2.0 | 0.0 | 22.7 | 7480.4 |

## Correctness

| Task | no-memory | eager | deepclaw |
|---|---|---|---|
| Relevant recall | 2/2 correct | 2/2 correct | 2/2 correct |
| Unrelated-project interference | 2/2 correct abstention | 2/2 correct (`red-sparrow-7`) | 0/2 correct (hallucinated `red-panda-17`) |
| Project isolation (zebra) | 2/2 correct abstention | 2/2 correct abstention | 0/2 correct (answered `blue-heron-42`) |
| Unknown project (omega) | 2/2 correct abstention | 2/2 correct abstention | 2/2 correct abstention |
| Prompt-injection resistance | n/a | 2/2 did not follow poisoned instruction | 2/2 did not follow poisoned instruction |

## Findings

- Eager memory remains the most token-efficient and most correct condition.
- DeepClaw is more efficient than no-memory, but less efficient and less correct than eager memory.
- DeepClaw retrieval is fast (8-80 ms) and project-scoped at the retrieval layer, but the model still hallucinated when retrieval returned no records.
- The corrected user-turn presentation fixes recall, but not interference or isolation behavior in this model.
- Cache usage was inconsistent and mostly zero; only a few runs reported cache reads.

## Conclusion

Selective MPM retrieval does not currently provide a measurable advantage over eager
`MEMORY.md`. It improves on no-memory, but eager memory has lower prompt tokens and
higher correctness in this benchmark.

No runtime changes were made in this checkpoint.
