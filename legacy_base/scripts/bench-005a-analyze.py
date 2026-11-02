#!/usr/bin/env python3
"""Summarize one DeepClaw 005A run without consulting agent claims.

Usage:
    python3 scripts/bench-005a-analyze.py .lab/bench/005a/<run-id>

The input is summary.json written by `bench run --experiment D`. All task
success values in that file come from the repository scorer; this script only
aggregates them. Provider-reported tokens and local estimates stay separate.
"""

from __future__ import annotations

import json
import statistics
import sys
from pathlib import Path


def mean(values):
    values = list(values)
    return statistics.mean(values) if values else 0.0


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__.strip())
        return 2
    run_dir = Path(sys.argv[1])
    summary = json.loads((run_dir / "summary.json").read_text())
    manifest = json.loads((run_dir / "manifest.json").read_text())
    trials = summary.get("trials", [])
    if not trials:
        print("no trials")
        return 1

    print(f"run       {summary['run_id']} ({summary['experiment']})")
    print(f"provider  {manifest.get('provider_kind')} model={manifest.get('model_requested') or 'n/a'}")
    print(f"seed      {manifest.get('seed')} pairs={manifest.get('pair_count')}")
    print(f"trials    {len(trials)}")
    print()

    for condition in ("control", "treatment"):
        group = [t for t in trials if t.get("condition") == condition]
        if not group:
            continue
        coding = [t.get("coding") or {} for t in group]
        print(
            f"{condition:10s} full {sum(1 for c in coding if c.get('full_success'))}/{len(group)} "
            f"partial {mean(c.get('partial', 0) for c in coding):.3f} "
            f"constraint-violations {sum(1 for c in coding if c.get('constraint_violations'))}"
        )
        print(
            f"           requests {mean(t['totals']['llm_requests'] for t in group):.1f} "
            f"tools {mean(t['totals']['tool_calls'] for t in group):.1f} "
            f"prompt {mean(t['totals'].get('prompt_tokens', 0) for t in group):.0f} "
            f"completion {mean(t['totals'].get('completion_tokens', 0) for t in group):.0f} "
            f"cache-read {mean(t['totals'].get('cache_read_tokens', 0) for t in group):.0f}"
        )
        print(
            f"           task-state prompt tokens {mean(t['totals'].get('task_state_prompt_tokens_estimate', 0) for t in group):.0f} "
            f"storage bytes {mean(t['totals'].get('task_state_storage_bytes', 0) for t in group):.0f} "
            f"repeated reads {mean(c.get('metrics', {}).get('repeated_reads', 0) for c in coding):.1f} "
            f"repeated failed investigations {mean(c.get('metrics', {}).get('repeated_failed_investigations', 0) for c in coding):.1f}"
        )
        print()

    print("per task")
    for task in sorted({t["scenario"] for t in trials}):
        for condition in ("control", "treatment"):
            group = [t for t in trials if t["scenario"] == task and t["condition"] == condition]
            if not group:
                continue
            coding = [t.get("coding") or {} for t in group]
            print(
                f"  {task:22s} {condition:10s} "
                f"full {sum(1 for c in coding if c.get('full_success'))}/{len(group)} "
                f"partial {mean(c.get('partial', 0) for c in coding):.3f} "
                f"time-to-pass {mean(c.get('time_to_full_success_ms', 0) for c in coding):.0f} ms"
            )
    print()

    coding = summary.get("coding")
    if coding:
        print("paired comparison")
        print(json.dumps(coding, indent=2, sort_keys=True))

    errors = [t["trial_id"] for t in trials if t.get("status") == "error"]
    if errors:
        print("\nerrored trials:", ", ".join(errors))
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
