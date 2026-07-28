# Idempotent restart handling

Issue: https://github.com/icholy/xagent/issues/1520

## Problem

A task (1421) was marked `Failed` with reason `sandbox exited with status code 137`
even though the run actually completed successfully. The 137 was not an OOM kill or a
real failure — the runner processed a single restart command twice and the second pass
SIGKILLed the run the first pass had just launched.

The restart command is designed to survive on the server until the new run's `started`
event consumes it (`internal/runner/runner.go`). But the driver takes ~12s to boot
(dockerd under sysbox), so any poll landing in the launch→`started` window still sees
`Command=Restart` and re-runs the handler. Three defects compound:

1. **Root cause — the restart handler is not idempotent.** The `Start` handler guards
   against a double-launch ("sandbox already running, do nothing"), but the `Restart`
   handler has no equivalent guard, so its `Kill` fires against the run the previous
   pass launched.
2. **Contributing — a SIGTERM during driver boot is lost.** As PID 1 the driver
   ignores SIGTERM until its handler is installed, so a stop landing in the boot window
   always escalates to SIGKILL → exit 137 → the runner's `failed` backstop instead of a
   clean stop.
3. **Consequence — a zombie run.** A sandbox whose `started` event the server rejects
   keeps running with no way to report. Here it did real work against a task the server
   had already marked `Failed`.

Reconstructed runner log (task 1421):

```
14:13:31 INFO killing container container=96074ccc2eec...   <- pass 1: old container already exited
14:13:31 INFO adopting existing container task=1421          <- pass 1: launches run v5
14:13:36 INFO killing container container=96074ccc2eec...   <- pass 2: kills the run it just launched
14:14:06 WARN SIGTERM timed out, sending SIGKILL
14:14:14 ERROR sandbox exited without reporting task=1421 exitCode=137
14:14:15 INFO adopting existing container task=1421          <- pass 2: relaunch (started rejected, zombie run)
```

## Design

The three defects are layered: fixing (1) alone resolves the reported incident, and
(2) and (3) close the remaining latent windows (e.g. a genuine cancel during boot).
Each is an independent slice.

### 1. Idempotent restart handler (root cause)

The taskstate record already carries the version of the run it launched
(`taskstate.Record.Version`, stamped by `Start` with `task.Version`). That is exactly
the bit needed to tell "old run to kill" from "new run already launched for this
restart":

- `Restart()` bumps `task.Version` to N when the command is issued.
- Pass 1 kills the old run (recorded version N-1) and calls `Start`, which writes a
  record stamped with version **N**.
- Pass 2 reads a record whose version already equals `task.Version` (N) — the restart
  has already been carried out, so it must **not** kill or relaunch. It waits for the
  new run's `started` to consume the command, exactly like the `Start` handler's
  already-running guard.

Change the `TaskCommandRestart` arm of `Runner.Poll` to read the record first and short
circuit when it is already on the current version:

```go
case model.TaskCommandRestart:
    g.Go(func() error {
        // Idempotency guard: if the record is already stamped with the task's
        // current version, this restart's new run has already been launched by
        // an earlier poll pass. Do nothing and wait for its "started" event to
        // consume the command — killing here would SIGKILL the run we just
        // started (issue #1520).
        rec, ok, err := r.store.Read(task.ID)
        if err != nil {
            r.log.Error("restart: failed to read task record", "task", task.ID, "err", err)
            return nil
        }
        if ok && rec.Version == task.Version {
            r.log.Debug("restart already processed, waiting for started", "task", task.ID, "version", task.Version)
            return nil
        }

        // First pass: kill the old run, then start the new one.
        if _, err := r.Kill(ctx, task); err != nil {
            r.log.Error("failed to kill task for restart", "task", task.ID, "err", err)
        }
        // ... existing sem.TryAcquire + Start + failed-on-error path unchanged
        return nil
    })
```

Notes on correctness:

- **No record / pruned sandbox** (restart of a completed/failed task whose sandbox was
  removed): `ok == false` on the first pass, so it proceeds to kill (a no-op — `Kill`
  returns `false, nil` with no handle) and start, which writes the version-N record.
  The second pass then sees version N and short circuits.
- **`Start` fails after `Kill`**: `Start` writes the record only after `Launch`
  succeeds, so on failure the record stays at N-1 and a `failed` event is enqueued. The
  `failed` fold clears the command to `None`, so no second restart pass runs.
- **Legacy version-0 records**: a record written before run-versioning unmarshals to
  version 0, which never equals a live task's version (≥1), so the guard is a no-op and
  behaviour is unchanged (kill+start) — safe.

This mirrors the existing `Start` handler and needs no schema or proto change; the
record already carries `Version`.

### 2. Make a stop during driver boot survivable (defect #2)

Two independent improvements, either or both:

- **Install the driver's SIGTERM handler before any slow startup work.** In
  `Driver.Run`, `signal.Notify` is already called before `GetTask`, but the boot cost
  is the sandbox init that precedes the driver process. Move the `signal.Notify`
  registration to the earliest point in the driver's `main`/command entry so the
  handler is live the instant the driver binary starts, shrinking the PID-1 window.
- **Do not turn a boot-window kill into `failed`.** When the runner signals a sandbox
  that never emitted `started`, an escalation to SIGKILL is expected, not a driver
  crash. The `supervise` backstop currently reads any non-zero exit as a lost report
  and enqueues `failed`. Scope it so a sandbox killed by the runner before its run was
  acknowledged does not produce a spurious `failed`. (With slice 1 in place the
  reported incident no longer reaches this path, but a real cancel-during-boot still
  can.)

The exact mechanism is an open question (see below) — a proposal-review decision.

### 3. Stop zombie runs whose `started` is rejected (defect #3)

`SubmitRunnerEvents` already computes `applied` per event but returns an empty
`SubmitRunnerEventsResponse` (`internal/server/apiserver/runner.go`). A driver whose
`started` event is rejected has no signal that it is orphaned and runs the whole job
against an already-terminal task.

Surface the rejection so the driver can self-terminate:

- Extend `SubmitRunnerEventsResponse` (`proto/xagent/v1/xagent.proto`) with a per-event
  `applied` result (e.g. `repeated bool applied = 1;` or a richer status enum).
- In `Driver.Run`, if the `started` submit reports `applied == false`, the task has been
  superseded or terminated — log it and exit without doing the run, so no work is done
  against a task the server has moved on from.

This is defense in depth: with slice 1 the incident's zombie never spawns, but any
future path that rejects a `started` (archived task, superseded version) is then
self-healing.

## Implementation Plan

1. **Idempotent restart handler** — Delivers: the version guard in the
   `TaskCommandRestart` arm of `Runner.Poll` so a duplicate restart pass is a no-op.
   Depends on: nothing (uses the existing `taskstate.Record.Version`). Verifiable by: a
   runner unit test that issues a restart, runs two poll passes within the
   launch→`started` window, and asserts exactly one kill + one launch (and that the
   task is not marked `Failed`). This slice alone fixes the reported incident and is
   independently shippable.
2. **Boot-window stop hardening** — Delivers: earlier `signal.Notify` registration in
   the driver and/or a `supervise` change so a runner-initiated kill of a not-yet-
   started run does not emit `failed`. Depends on: nothing (independent of 1).
   Verifiable by: a driver/runner test that stops a sandbox before `started` and
   asserts a clean cancel rather than `Failed`.
3. **Reject-aware driver** — Delivers: the `applied` result on
   `SubmitRunnerEventsResponse` and the driver self-terminating when its `started` is
   rejected. Depends on: the proto/response change (foundation), then the driver
   wire-up. Verifiable by: a driver test where `SubmitRunnerEvents` returns
   `applied=false` for `started` and the driver exits without running the agent.

Order: land 1 first (fixes the bug), then 2 and 3 as independent hardening PRs.

## Trade-offs

- **Version guard vs. re-probing the backend.** An alternative is to have the restart
  handler probe the backend for a running sandbox (like the `Start` handler does) and
  skip the kill if one is running. That is racier and semantically wrong for restart —
  restart *wants* to kill a running old run; only a run already launched *for this
  restart* must be spared. The record version distinguishes the two precisely and with
  no extra backend round-trip, which is why the issue calls it out as the fix.
- **Returning `applied` vs. a dedicated "am I current?" RPC.** The driver could poll a
  new RPC to check whether its version is still live, but the `started` submit already
  round-trips to the server and already knows the answer; returning it is strictly
  cheaper and race-free.
- **Scope.** Slices 2 and 3 are not strictly required to fix issue #1520 (slice 1 is),
  but they close latent windows that would surface on a genuine cancel-during-boot. They
  are kept as separate, optional slices so the critical fix ships without waiting on the
  proto change.

## Open Questions

- **Defect #2 mechanism.** Should the boot-window fix live in the driver (earlier signal
  handler registration), the runner (`supervise` not emitting `failed` for a kill of an
  unacknowledged run), or the backend (`Signal` semantics)? Each has different blast
  radius; the runner-side guard is the most general but needs a way to know the run was
  never acknowledged.
- **Shape of the `applied` result.** A plain `repeated bool` is minimal; a status enum
  (`APPLIED` / `STALE_VERSION` / `TERMINAL`) is more self-describing and lets the driver
  log a precise reason. Preference?
- Should slice 1 also cover the symmetric case for the `Start` command's boot window, or
  is the existing running-guard sufficient there? (The `Start` handler already guards on
  a running sandbox, so it appears sufficient, but worth confirming.)
