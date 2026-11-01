# Milestone 005C: why evidence-backed task state did not improve live coding correctness

Status: **analysis complete. No code, prompt, fixture, or parameter was
changed; no live call was made.** All claims below come from the preserved
redacted evidence of the renewed 10-trial pilot (`005b-pilot-20261102a/b`,
10/10 harness-valid, 1 control win, 0 treatment wins, 4 ties): per-trial
`result.json` (turns, requests, executed tool calls, coding outcomes,
task-state snapshots, usage recovery), `raw/requests.jsonl` (full prompts
and responses), `workspace/sessions/*.jsonl` (assistant text with tool
names and arguments, tool results), workspace git diffs, and trial logs.

Headline: **the pilot contained zero genuine working-memory opportunities.**
In all 10 trials ordinary history (plus the workspace) carried every fact
the agent needed; nothing the task required was ever lost to compaction or
restart. Task state therefore operated as a redundant channel: captured
faithfully, rendered, and — with no gap to fill — unable to move outcomes.
The single control win is explained by ordinary planning variance (check-
command choice and sandbox navigation), not by memory. The honest
conclusion is a null with a mechanism, not a refutation of task state.

## 1. Phase 1 — reconstructed trajectories

Sources: session transcripts (intentions + exact tool arguments), raw
request histories, result records, workspace diffs. Observed events are
stated as such; intentions are quoted from assistant text, not inferred.

### 1.1 `multi_file_cart` — tie, mutual inaction

Both conditions: explored (`ls`, `find`), read all six files, ran
`./check.sh` (failures confirmed: SAVE15, formatting), stated the README
contract correctly — then **zero writes** and a final summary response.
Workspaces byte-identical to pristine (partial 0.364 both = pre-existing
passes only). Treatment additionally probed for hidden specs (`git log
--all`, reflog, stash, `rev-list`, reading session meta) and found
nothing; hidden tests were never in the trial tree, so the probing was
contained. Divergence: none. The failure is a symmetric intent-action
gap, unrelated to memory in either direction.

### 1.2 `misleading_failure` — control win by check-command choice

Control: read README (finance contract), `rate_test.go`, legacy notes;
ran `./check.sh` (relative path — allowed) → confirmed
`RateCents(1000) = 800, want 500`; wrote an explicit boundary analysis in
assistant text; applied the one-character `edit` (`>=` → `>`); verified
with `./check.sh`, a self-written boundary sweep (created, run, removed),
and a final check. Full 7/7. It also hit guard friction later
(`cat > /tmp/...`, `chmod`, `go test ./... | tail` blocked) but only
after the fix was secured and verified.

Treatment: read the same files (correct contract in context), ran
`find`/`ls`, then `cd <absolute-path> && git log` (once: allowed),
followed by 14 blocked bash calls across 16 requests: `cd <abs> &&
go test`, bare `go test ./...`, `go build/vet`, GOCACHE redirect
variants, `$(which go)`, `export`, `chmod`, `GOCACHE=off go test`.
Working theory stated in text: "the Go build cache lives outside the
workspace." Never tried the documented `./check.sh`; never edited;
exhausted iterations. Partial 0.571 (only the tests that pass on the
pristine repo... actually hid 3/1, pub 0/1: one public subcase newly
failing? No — pristine pub failed 1 too; treatment kept the bug, and one
hidden edge (boundary1000) stayed failing).

Divergence point: the very first check invocation — `./check.sh`
(allowed: script path contains no flagged token) vs `go test ./...`
(blocked). The sandbox guard matches on command-string tokens (`./...`,
absolute workspace-external paths, `$()`, `export`, `chmod`, /tmp
writes); both agents tripped it repeatedly, but control's critical path
avoided the flagged tokens. Identical obstacle, different navigation.

### 1.3 `requirement_change` — tie, acknowledged-but-unimplemented correction

Both conditions: explored (including contained hunts for hidden specs),
ran checks, left the repo **pristine** (partial 0.125 = build only).
t2 delivered the banker's-rounding correction; both agents restated it
accurately in assistant text (treatment: "tie rule = banker's rounding
(round half to even)... So I need to implement `RoundCents` that:";
control: "The user has now provided the missing requirement... So I
need to implement...") — and then made zero edits. Divergence: none.

### 1.4 `compaction_ledger` — tie, mutual churn into the task limit

Both: fixed `balance.go` early (the only workspace modification), then
looped checks/reads until 8 (control) / 7 (treatment) automatic
threshold compactions in t1 tripped the 005B-R churn guard, with a
max-iteration exhaustion alongside. Task-limited in t1 (harness-valid),
partial 0.7 both, t2/t3 never ran, explicit compaction never tested.
Session files compacted to ~11 records; summaries ~1.1–1.2k chars.
Divergence: none of substance (same guard, same turn, same score).

### 1.5 `restart_parser` — tie, clean convergence across a real restart

Both: t1 fixed `ParseInt`, reported the remaining `ParseList` failure;
process restart (2 subprocesses each); t2 resumed with history intact
(23→32 control, 36→50 treatment), finished `ParseList`, full 9/9,
evidence reuse true. Treatment's t2 opened by distrusting persistence
("verify the current state rather than assume") and re-verified via
`git status` + file re-reads — i.e., it relied on workspace + history,
not on task state. Divergence: none in outcome; note-taking style only
(see §5).

## 2. Phase 2 — the shipping result

Control applied the correct README contract; treatment retained the bug
while fighting the sandbox. Adjudicating the five candidate causes:

1. **Invalid command/tool parameters — secondary.** Commands were
   well-formed; they were refused by policy, not malformed.
2. **Sandbox restrictions — the proximate blocker, symmetric.** The
   string-pattern guard (`./...`, absolute paths, `$()`, `export`,
   `chmod`) refused treatment's `go test` formulations while admitting
   control's `./check.sh`. Same sandbox, same rules, both conditions;
   control tripped it too (later, off the critical path). Fair
   obstacle, not a confound.
3. **Repeated commands without progress — yes, as mechanism.** 14
   blocked bash calls re-probed the same refusal with GOCACHE/`export`/
   `$(...)` variations instead of switching to `./check.sh` or to the
   one-line edit the agent had already diagnosed from reads.
4. **Model reasoning/planning error — the root cause.** The agent
   correctly extracted the contract and the bug location from reads,
   then committed to a false causal theory (build-cache location) and
   never revisited the plan. Its final message is exhaustion, not a
   diagnosis.
5. **Stale or misleading task-state context — no.** All 29 treatment
   entries are `tool_reported` (15 observations, 14 failures); the
   rendered block quotes real commands with real errors. Nothing stale,
   nothing contradicted, nothing invented.
6. **Task-state overhead or prompt displacement — no.** Treatment max
   prompt 5,731 tokens vs 32,768 context (control: 7,930); treatment
   totals were lower than control's. No threshold compaction fired in
   either shipping trial (auto=0). Overhead existed (4,973 task-state
   prompt tokens, 36 KB sidecar) without benefit, but it did not cause
   the failure — the failure is fully explained by (3)+(4) in a trial
   with headroom to spare.

Do not attribute the failure to memory merely because memory was
enabled: the evidence shows a planning error under a symmetric
obstacle, with faithful task-state content and no displacement.

Missing evidence (identified, not substituted): the guard's exact
token→verdict mapping lives in production tool config, not in
benchmark artifacts. The `./...`-vs-`./check.sh` distinction is
inferred from allowed/blocked pairs across both transcripts — strong
but inferential. Log the inference as such.

## 3. Phase 3 — working-memory opportunities

Boundaries in the pilot: automatic compaction only in ledger t1 (both);
a true process restart only in parser t2 (both); zero compactions and
zero restarts everywhere else (all `summ=0` outside ledger).

| Situation | (1) Known before? | (2) In history/summary? | (3) Captured? | (4) Rendered? | (5) Depended on? | Class |
|---|---|---|---|---|---|---|
| Ledger auto-compactions | check outputs, balance fix | yes (summaries ~1.2k + tail) | yes (17 obs, 5 failures, 1 compaction event) | yes (~500 tok rendered/req) | no observable use; looping continued | **rendered but ignored** |
| Parser restart (both) | ParseInt fix, remaining-failure report | yes, fully (no compaction; 36–50 msgs + M files on disk) | yes (22–26 obs) | yes | no; agent re-verified from workspace/history instead | **redundant memory** |
| Shipping contradiction (both) | README vs legacy tables | yes (reads in history) | yes (read observations) | yes | no; decision made from history reads | **redundant memory** |
| Cart/billing turns | all task content | yes (tiny histories, no loss) | yes | yes | no | **redundant memory** |
| Billing correction | new tie rule | yes (current user msg) | **no** (snapshot identical t1→t2; no correction entry — 004C needs explicit markers) | n/a | ignored while visible | **relevant evidence not captured** (but see §4) |

Ledger and parser, as instructed: ledger shows capture+rendering
without dependence (the loop ran identically with and without the
channel); parser shows the restart machinery working (2 processes,
history continuity, sidecar present) with nothing actually at risk —
a mechanism validation, not an advantage opportunity. **Genuine memory
advantage opportunity count across 10 trials: zero.** That is the
mechanical explanation of the null: the pilot never created the
condition (real context loss of needed evidence) under which
evidence-backed task state could differ from history.

## 4. Phase 4 — the ignored banker's-rounding correction

- Reached the model: yes, verbatim t2 user message in both conditions.
- Captured by task state: no — t2 snapshots are entry-identical to t1;
  the objective still reads the t1 text; 004C does not promote
  unmarked user text to correction entries (known 005A §3 gap, now
  observed live).
- Visible at the decision: yes — latest user message, histories of
  only dozens of messages, zero compactions in billing.
- Ignored: yes — both agents paraphrased the rule back in assistant
  text and then produced zero edits.
- Lost to compaction: no.

Per the pre-registered rule: do not assume enhanced capture would have
fixed it — the instruction was already visible, understood in text,
and dropped at planning. The defect is an intent-action gap in the
model, symmetric across conditions, and no memory change is implicated.

## 5. Phase 5 — the parser `MEMORY.md`

The treatment parser wrote `memory/MEMORY.md` plus a daily log (3
memory-referencing calls: 1 listing, 2 writes), containing its own
reasoning and **public** test names only (`TestParseIntTrims`,
`TestParseListTrims` — both agent-visible). It was **never read back**
in t2 (t2 opened with `git status` + source re-reads) and never
injected into any prompt (no memory tool ran; MPM disabled both
conditions). Scoring excludes `memory/` by construction, and hidden
tests (names like `TestParseIntWhitespaceHidden`) appear nowhere in
the notes. Verdict: ordinary agent-framework note-taking, no
comparison effect, no boundary crossed. No other agent-generated
persistence was found: control shipping's temporary sweep test was
created and removed; no `.gocache` residue, no stray files (final
trees show only intended source edits plus runtime `sessions/`,
`state/`, `memory/` dirs). Existing redaction/isolation handling is
unchanged and nothing is rewritten retrospectively.

## 6. Phase 6 — instrumentation deficiency

Why the gap: the MiniMax adapter populates provider tool-call records
with `{"id"}` only — no function name or arguments — so bench-side
`repeated_*` metrics collapse to an unknown-command bucket (reported
as `?`). The agent-side record is richer: `ToolExecStartPayload`
carries `Tool` + `Arguments`, and session files carry full arguments.

Bench-only, securely redacted proposal (no agent-visible change):
subscribe the trial event bus to `KindAgentToolExecStart` (already
emitted; same pattern as the existing end-event collector), join
start-records (name, arguments) with end-records (outcome, latency)
by per-turn order, and persist into `ToolCallRecord.Arguments` with
(1) existing `RedactSecrets` applied to serialized args, (2) a
length cap (e.g., 500 chars) keeping command strings and paths while
dropping bulk file contents, and (3) arg-key + size metadata always.
Keep provider-reported calls (intent) and executed calls (observation)
as separate series — the join is for effort analysis, never for
scoring. Hidden tests stay safe: they never enter tool arguments
(the agent cannot reference what it cannot see), and artifacts remain
gitignored with current redaction. Until implemented, command-level
"repeated investigation" claims must stay qualitative (as in §2,
grounded in session transcripts).

## 7. Phase 7 — next experiments

### Experiment A — general coding performance

Same comparison (control vs boundary task state, identical everything
else), but on tasks with demonstrated headroom: the pilot shows
control itself scores 0.364/0.125/0.7 on three tasks, so harder,
multi-step, realistic-context tasks can separate agents. Context
windows sized from **measured live traffic** (verbose mock as floor),
not mock traffic. Score full/partial success plus requests, tool
calls, failures, tokens, task-state overhead. Benefit = treatment
full-success or partial lead at equal-or-lower effort; harm = the
reverse plus violations/cost; null = ties. Pre-register criteria,
order, and caps exactly as in 005B.

### Experiment B — memory-specific continuity

Tasks that **independently verify** ordinary-context loss before the
decision point: gate the interruption turn on a measured condition
(summary non-empty AND pre-boundary tool evidence absent from current
history — the 005B-R predicate generalized into a pre-trial
qualification), with native compaction preserved for the control.
Only trials passing the gate count toward the memory question; the
rest are reported as non-qualifying. Benefit = treatment recovers
the lost evidence and decides correctly where control cannot;
harm/no-difference defined symmetrically. Never tailor fixtures to
force a treatment win: gate on measured loss, score on hidden tests.

### Recommendations, ordered by expected engineering value

1. **Repeated-command instrumentation (§6).** Unlocks the effort
   analysis the pilot couldn't do; bench-only, low risk.
2. **Size fixture windows from live traffic** and re-qualify offline
   (verbose mock as floor). Prerequisite for any informative rerun.
3. **Genuine-loss gating for memory tasks (B)** so future nulls are
   informative rather than vacuous.
4. **Correction-capture study** (explicit markers vs natural
   corrections) as a diagnostic — but billing shows capture alone
   would not have changed the outcome, so expect modest value.
5. **No production memory change is justified by this evidence.**
   With zero genuine opportunities observed, nothing in the pilot
   indicates any 004C change would move outcomes. The one real capture
   gap (natural corrections) is proven outcome-irrelevant here. The
   justified changes are all bench-side.

No live calls were made for this analysis; no pushes, deploys, or
baseline modifications. Evidence paths: `.lab/bench/005b/005b-pilot-
20261102{a,b}/` (results, raw traces, session files, ledgers).
