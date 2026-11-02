# Milestone 005G-live: first paired memory-continuity trial

Status: **first pair run, gate applied, study stopped per protocol.**
One control/treatment pair on `tiered_migration` completed harness-valid
(2/2 pass, 0 fail, 0 error, fully metered, zero guard interventions).
Eligibility split: **control ineligible (evidence retained), treatment
ineligible (no dependence on lost evidence)** — different criteria,
same verdict. Per the preregistered rule the remaining four pairs do
**not** run. No reruns, substitutions, or fixture changes.

- Run: `005g-live-20261105a`, seed 20261105 (order recorded: control
  first, treatment second), caps 8M total / 2M per trial.
- Model: MiniMax-M3.1-Flash, international endpoint, existing
  project-scoped credential; four-tool profile; 8K context (task-defined
  memory-test sizing, identical both sides); MPM and resident cache
  disabled; task state OFF (control) vs 004C boundary ON (treatment).
- Spend: 434,347 provider-reported tokens of 8M (5.4%); max trial
  269,354 of 2M; reserved 0; unknown 0; missing usage 0.
- Codebase: `5ebc3a5c` (005G design). No production, fixture, scoring,
  or instruction changes for this run.

## 1. Preregistration (as authorized)

Pair 1 only, then inspect: a pair qualifies iff **both** trials pass
the five-criterion gate on pre-decision evidence alone —
(1) real compaction boundary, (2) identifiable prior finding,
(3) markers absent from post-boundary history **and** summary,
(4) a scored t3 decision depending on the missing evidence,
(5) capturability from existing events — with workspace
recoverability recorded, not disqualifying. Abort conditions:
auth/quota/isolation/ledger failures, missing usage, invalid state.
No outcome may enter the gate.

## 2. Execution and validity

Both trials harness-valid, pre/postflight clean, MPM retrieval 0,
usage 100% reported, ledgers and tool traces fully reconciled
(unknown 0, reserved 0), no guard denials (005E bypass active).
Control: 39 requests, 46 tools, 269,354 tokens. Treatment: 24
requests, 31 tools, 164,993 tokens, 7,972 task-state prompt tokens,
44 KB sidecar. Instrumentation healthy throughout.

## 3. Gate application (outcome-blind)

**Shared premises.** (1) Boundary: yes, both — explicit `/compact`
before t3 (compacted=true), plus one threshold auto-compaction in t3
at the 8K window; summaries 897/705 chars. (2) Prior finding: yes,
both — t1's verbose dry-run mapping (tool-observed, task-state
captured in treatment). (5) Capturable: yes, both — dry-run and check
outputs are tool observations.

**Control: INELIGIBLE — evidence retained (criterion 3 fails).**
t2 re-ran the verbose suite (`go test -v ./...`), so the full dry-run
table sat in the protected tail at compaction time; G6/G7 marker rows
(`tier G7: legacy 10000 delta +0`, etc.) are present verbatim in
post-boundary visible history. The summary additionally preserves the
G4→800 headline, the fix, and the deferred rounding failure with
concrete values (`RoundEdge(105.5) = 105, want 106`).

**Treatment: INELIGIBLE — no dependence on lost evidence
(criterion 4 fails).** t1 ran the verbose dry run four times (task
state captured the runs); t2 burned 4 requests on blocked absolute
paths; post-compaction history and summary contain **none** of the
dry-run table rows — genuine loss of the mapping detail. But t3's
scored decisions do not require it: the rounding fix needs only the
generic banker's rule (stated verbatim in the t3 prompt, with examples
in the repo tests), and the prescribed regression-pin needs "G4" and
"800" — both preserved in the summary. The lost rows (per-tier
mappings) are relevant background, not required evidence. (Criterion 3
would pass on markers alone; criterion 4 fails on dependence.)

**Recoverability (recorded, not disqualifying):** both conditions can
re-derive the mapping by re-running the verbose dry run (control did:
t2 cost ≈40k tokens over 3–4 requests); treatment paid no such cost
in t3 — it also performed no t3 edits (see §4).

**Verdict: 0 of 2 eligible → STOP.** Pairs 2–5 do not run. The split
is systematic noise, not signal: identical task, divergent t2
trajectories (re-verification vs absolute-path flailing), different
compaction leftovers.

## 4. Outcomes (reported separately from the gate)

Control: full success 12/12 — rounding fixed, regression test written
*and mutation-tested* (reintroduced the G4 bug, confirmed the test
catches it, restored; same for rounding), tree clean. Treatment:
partial 5/12 — t3 read four files, wrote a correct plan ("use
`math.RoundToEven`..."), then ended the turn with **zero edits and
zero test runs**, the same intent-action gap seen in 005B/005F
(cart, billing). Task state was captured (32 entries: 27 observations,
4 failures, 1 compaction event) and rendered (~2–2.5k prompt tokens
per t3 request, 10k-char block), but the bounded evidence quotes
truncate tool output — the rendered block contains the dry-run
*invocations*, not the G4→800 values (those survived only via the
abstractive summary). So: captured ✓, rendered (partially — values
truncated by the token budget) ✓/✗, used ✗ (no action at all).

No causal claim is made from this pairing: the failure mode
(non-attempt) is orthogonal to evidence availability, and the gate
had already excluded the trial before outcomes were considered.

## 5. Analysis

- **Why the split happened.** Control's t2 re-verification refreshed
  the evidence into the protected tail; treatment's t2 did not.
  Summaries in both conditions kept the headline (G4→800) while
  dropping the table body — abstractive summarizers preserve gist,
  not verbatim rows. Eligibility therefore hinges on whether the
  *scored decision needs gist or verbatim detail*: this task's t3
  needed only gist (plus repo/prompt sources). A qualifying task
  needs a scored decision that requires dropped verbatim detail.
- **The 8K window works as designed** (compaction fired, loss is real
  and measurable), but loss alone is insufficient — dependence is the
  binding constraint, and it must be designed against a live
  summarizer whose abstraction level cannot be fixed in advance.
- **Task-state overhead, measured:** 7,972 prompt tokens + 44 KB
  sidecar for zero demonstrated use in this trial. The mechanism ran
  correctly; the model did not act.
- **No evidence supports expanding the sample** on this fixture:
  with 0/2 eligible, more repetitions cannot produce the comparison
  the study was designed for.

## 6. What would qualify (recommendation, not a run)

A task whose scored t3 decision requires verbatim detail that (a) the
dry run emits, (b) abstractive summaries demonstrably drop, and (c)
files/prompts do not contain — e.g., a regression pin whose expected
values appear only mid-table, with hidden tests asserting exactly
those values, verified in a mock dry run where markers demonstrably
vanish from both history and summary. Do not weaken the gate to
count "potentially useful background" as dependence; that would
manufacture eligibility.

## 7. Limitations and artifacts

- n=1 pair; no significance possible or claimed.
- Gate markers are task-specific strings; paraphrased-but-present
  evidence (e.g., "G4 mapped wrong" vs the exact row) counts as
  absent by construction — conservative by design, documented here.
- The mock summarizer is content-free, so mock trials cannot
  validate summary retention behavior; only the loss branch was
  mock-validated (005G). Live summaries are abstractive and
  unpredictable — the gate must be re-applied per live trial.
- Treatment non-attempts (0 edits in t3) mirror 005B/005F; the
  mechanism cannot fix a model that stops before acting.

Artifacts: `.lab/bench/005g/005g-live-20261105a/` (manifest with
seed/order/caps, per-trial results with task-state snapshots,
raw/session traces, ledgers, tool traces, redacted logs).
Previous runs, fixtures, and baselines untouched. No push, deploy,
or further live trials. A new authorization is required for any
follow-up, including a redesigned eligible task.
