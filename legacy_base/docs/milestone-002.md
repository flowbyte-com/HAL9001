# Milestone 002: Minimal coding profile

## Phase A: Benchmark 001C failure classification

The interference and isolation failures were unsupported model-generated claims,
not MPM leaks. Benchmark traces show retrieval returned zero records for
unrelated and zebra queries, while the model still produced `red-panda-17` and
`blue-heron-42` answers. No evidence of out-of-scope memory reaching the model
was found. No isolation fix was required.

## Phase B: Existing tools

PicoClaw already provides file, search and shell tools. The minimal profile reuses
them through thin aliases instead of duplicating implementations.

Baseline exposed tool schemas: 18 tools, 16957 bytes.
Minimal coding profile: 4 tools (`read`, `write`, `edit`, `bash`), 5644 bytes.

## Phase C: Implementation

Opt-in via `DEEPCLAW_CODING_PROFILE=1`. After shared tool registration, each
agent's tool registry is replaced with aliases for `read_file`, `write_file`,
`edit_file` and `exec`.

MPM remains underneath the agent and is not exposed as a default coding tool.

## Phase D: Measurement

Schema overhead fell from 16957 to 5644 bytes (66.7% reduction). End-to-end
coding-task measurements with a live provider are not included in this checkpoint.

## Tests

- `TestAgentCodingProfileExposesOnlyCoreTools` passed.
- Existing `DeepClawMPM` tests passed.
