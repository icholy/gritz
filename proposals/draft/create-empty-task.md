# Create empty task

Issue: https://github.com/icholy/gritz/issues/1606

## Problem

The create-task page (`webui/src/routes/tasks.new.tsx`) has a required **Instructions**
textarea. Every task therefore has to be born with its first instruction typed into a
form that is not the place you actually talk to the task — the task page's chat composer
is. The two inputs do the same thing, in two different shapes, and the form one is the
worse of the pair: no timeline context, no history, and it is the only thing standing
between "pick a runner and a workspace" and "have a task".

The ask is to drop the field: creating a task should produce an **empty task**, and the
first instruction should be sent from the composer on the task page like every
instruction after it.

## Design

The field cannot simply be deleted. `CreateTask` hardcodes `Command: TaskCommandStart`
(`internal/server/apiserver/task.go:97`), so a task created with no instructions is
immediately picked up by the runner and a container is launched for an agent that has
nothing to do. It would produce a completed run before the user has said a word.

So the real change is a **task that exists but has not been asked to do anything yet**,
and a first instruction that starts it.

### The idle state: `PENDING` + `TaskCommand.NONE`

No new status is needed. The runner's work queue is
`WHERE runner = $1 AND org_id = $2 AND command != 0 AND archived = FALSE`
(`internal/store/sql/queries/task.sql:38`) — a task with no command is invisible to the
runner. And `(PENDING, NONE)` is currently unreachable: every transition in
`internal/model/task.go` that lands on `PENDING` sets a command with it (`Start`,
`Restart`, and the `Running`+start run boundary in `applyRunnerEventStopped`), and every
transition that clears the command lands on `RUNNING`, `CANCELLED`, or `FAILED`. The pair
is free, and it already means exactly what we want: *pending, with nothing pending*.

Call it **idle**. An idle task is a normal row — org, runner, workspace, namespace,
auto-archive, version 1 — with a `Created` lifecycle event in its stream and nothing else.

```go
// IsIdle reports whether the task has never been asked to run: created, but
// with no command for the runner to pick up. This is the state a task created
// with no instructions starts in.
func (t *Task) IsIdle() bool {
	return t.Status == TaskStatusPending && t.Command == TaskCommandNone
}
```

### Starting an idle task

`CanStart` today excludes `PENDING` outright; it must admit the idle case, and `Start`
must not bump the version for it — version 1 is still the first run, unlike a start from a
terminal status which provisions run N+1.

```go
func (t *Task) CanStart() bool {
	if t.Archived {
		return false
	}
	switch t.Status {
	case TaskStatusRunning, TaskStatusCompleted, TaskStatusFailed, TaskStatusCancelled:
		return true
	case TaskStatusPending:
		// Idle: created but never asked to run. Pending *with* a command is
		// already provisioned — starting it again is a no-op.
		return t.Command == TaskCommandNone
	default:
		return false
	}
}

func (t *Task) Start() bool {
	if !t.CanStart() {
		return false
	}
	// Running: the wake is queued and the bump happens at the run boundary.
	// Idle: run 1 was never started, so version 1 is still the run to start.
	// Terminal: provision the next run now.
	if t.Status != TaskStatusRunning && !t.IsIdle() {
		t.Status = TaskStatusPending
		t.Version++
	}
	t.Command = TaskCommandStart
	return true
}
```

Nothing downstream changes. `UpdateTask` already appends the instruction event with
`Wake: req.Start && task.CanStart()` and calls `task.Start()`
(`internal/server/apiserver/task.go:218-232`), and the composer already sends
`start: true`. Once `CanStart()` is true for an idle task, the first instruction wakes it,
the update publishes with `Runner: task.PendingRunner()`, and the runner sees a
`PENDING`/`START` task at version 1 — byte-identical to what it sees today for a
freshly created task. **The runner, driver, and agent prompt need no changes at all.**

### Creating without instructions

`CreateTask` starts the task only when it was given something to do:

```go
task := &model.Task{
	...
	Status: model.TaskStatusPending,
	// An empty task is idle: no command, so no runner picks it up. The first
	// instruction (UpdateTask with start) is what starts it.
	Version: 1,
	OrgID:   caller.OrgID,
}
if len(req.Instructions) > 0 {
	task.Command = model.TaskCommandStart
}
```

The rule is implicit — *instructions ⇒ start* — which is what every existing caller
already means. `CreateTaskRequest` is unchanged, so the scheduler
(`internal/model/schedule.go:77`) and the event router
(`internal/eventrouter/eventrouter.go:406`), which build their task rows directly and
always have instructions or events, are untouched. `gritz task create` without `-i` and
MCP `create_task` become able to create an idle task for free.

The `Created` lifecycle event is still written, so the timeline is not empty. The
create notification carries `Runner: task.PendingRunner()` — `""` for an idle task, which
the SSE runner filter (`internal/server/notifyserver/sse.go:85`) drops for runner
subscribers and delivers to UI subscribers. Exactly right: the UI should see the new
task, no runner should be woken for it.

### Disposing of an idle task

`Cancel()` already accepts `PENDING` and takes it straight to `CANCELLED` with no runner
round trip, and a cancelled task can be archived. So an abandoned empty task is two
clicks from gone, with no new operation.

### Web UI

`tasks.new.tsx` loses the `instruction` state, the `Instructions` textarea, and the
`!instruction.trim()` guard in `handleSubmit`; `createTask` is called with no
`instructions`. The page keeps name, runner, workspace, namespace and auto-archive, and
still navigates to `/tasks/$id` on success — which now lands on a task whose composer is
the next thing the user touches.

Two small renderings of the new state:

- **Status badge** (`webui/src/components/status-badge.tsx`): `PENDING` with
  `command === NONE` reads **draft**, not **pending**. Both badges derive from the same
  helper so the tasks list, the sidebar and the compact `StatusDot` agree. A
  `isIdleTask(task)` helper in `webui/src/lib/task.ts` mirrors `model.Task.IsIdle`.
- **Composer placeholder** (`webui/src/routes/tasks.$id.tsx`): "Send the first
  instruction to start the task…" while the task is idle, and autofocus the composer so
  arriving from the create page puts the cursor where the instruction goes.

## Implementation Plan

1. **Model: the idle state** — Delivers: `Task.IsIdle`, and `CanStart`/`Start` accepting
   an idle task without bumping the version. Depends on: nothing. Verifiable by: unit
   tests in `internal/model/task_test.go` — idle is startable, idle start keeps version 1
   and sets `START`, `PENDING`+`START` is *not* startable, idle is cancellable.
   Safe to merge alone: nothing constructs an idle task yet, so every transition is
   unreachable in production.

2. **Server: create without instructions leaves the task idle** — Delivers: the
   conditional `Command` in `CreateTask`. Depends on: (1). Verifiable by: an apiserver
   test that creates a task with no instructions and asserts `command == NONE`,
   `actions.start == true`, that `ListRunnerTasks` does not return it, and that a
   subsequent `UpdateTask{start: true, add_instructions: [...]}` flips it to
   `PENDING`/`START` at version 1 with a waking instruction event. Also update the
   existing tests that create instruction-less tasks incidentally
   (`internal/server/apiserver/task_test.go:188,218,288`) — they now get idle tasks.

3. **Web UI: drop the instructions field** — Delivers: the create page without the
   textarea. Depends on: (2). Verifiable by: create a task, land on the task page, send
   an instruction from the composer, watch the container come up — and confirm no
   container is launched before that.

4. **Web UI: render the idle state** — Delivers: the "draft" badge label, `isIdleTask`,
   the idle composer placeholder and autofocus. Depends on: (2). Verifiable by: an idle
   task renders as draft in the list and the sidebar and reads as "send the first
   instruction"; a pending-with-command task still reads as pending.

## Trade-offs

**Implicit "instructions ⇒ start" vs. an explicit flag.** A `start` field on
`CreateTaskRequest` would be explicit, but proto3 bools default to `false`, so every
existing client would silently stop starting its tasks; `optional bool` avoids that at the
cost of three-valued logic for a distinction no caller has ever wanted. The implicit rule
needs no proto change and preserves every current caller's behavior exactly.

**Reusing `(PENDING, NONE)` vs. a new `IDLE` status.** A new `TaskStatus` would be
self-describing, but it is a proto enum change plus a DB value plus an arm in every status
switch (`IsTerminal`, `CanCancel`, `CanArchive`, `CanRestart`, the three runner-event
folds), and older clients would render it as "unknown". The unused pair costs one helper
and is already exactly the semantics the runner query enforces.

**Not starting at all vs. starting an empty agent.** Letting the empty task start and the
agent sit idle needs no backend change, but it burns a container and an agent session to
produce a run that ends before the user's first instruction — and that run's exit would
have to be un-completed when the instruction lands.

**Removing the field vs. making it optional.** Keeping an optional textarea would be a
one-line variant on top of the same backend work, since `CreateTask` would still have to
handle the empty case. The issue asks for removal, and one instruction input is better
than two.

## Open Questions

- Should an idle task be archivable directly? `CanArchive` requires a terminal status, so
  today the flow is cancel-then-archive. Allowing archive on idle (nothing to reclaim) is
  a one-line relaxation but adds a third arm to `CanArchive`.
- Wording for the badge: **draft**, **idle**, or **new**?
- Should MCP `create_task` make `instruction` optional, so an agent can hand a prepared
  empty task to a human? Nothing needs it yet.
