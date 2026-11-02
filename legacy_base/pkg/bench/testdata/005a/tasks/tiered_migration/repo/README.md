# Tiered migration

The legacy table (`legacy.go`, codes `G1`..`G7`) is retired. `Migrate`
maps each legacy code to its current rate; an edge amount belongs to its
own tier. The dry-run test prints the full mapping table for diagnosis.

`tiered_migration` edge cases:

- `G4` (edge 1000) migrates to 650, not the next tier up.
- Unknown codes migrate to -1.
- `RoundEdge` uses banker's rounding (half to even) for exact ties,
  matching finance policy for tier-edge amounts.

Run `./check.sh` to verify.
