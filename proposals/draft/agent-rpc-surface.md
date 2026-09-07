# Dedicated agent RPC surface

Issue: https://github.com/icholy/xagent/issues/915

Builds on:
- [`proposals/implemented/eliminate-runner-socket-proxy.md`](../implemented/eliminate-runner-socket-proxy.md) — folded per-task auth into the general handlers (the situation this proposal cleans up).
- [`proposals/accepted/driver-owned-events.md`](../accepted/driver-owned-events.md) — the driver becomes the source of truth for `started`/`stopped`/`failed`; its `SubmitRunnerEvents` call is the canonical "awkward shared RPC".
- [`proposals/draft/scope-based-permissions.md`](./scope-based-permissions.md) — the `authscope` engine, which stays on the *user-facing* surface.

## Problem

`eliminate-runner-socket-proxy` deleted the runner's out-of-process `AgentFilter` and moved per-task authorization **into the general apiserver handlers**: the in-container agent now holds a narrow, server-minted app JWT and calls the same `XAgentService` RPCs (`GetTaskDetails`, `UpdateTask`, `CreateLink`, `UploadLogs`, `CreateGitHubToken`, `SubmitRunnerEvents`) that admins and API keys call. To make those shared methods simultaneously correct for a `*.*` admin and a narrow task token, every task handler grew per-instance auth machinery that serves no human caller:

- **Two-tier gating in every task handler.** `GetTask`/`UpdateTask`/`ArchiveTask`/… do a fail-fast `AllowOp(op)`, then a post-load `Allow(op, task.ScopeAttr()...)`. `ListLinks`/`ListLogs` add an `if !Allow(op) { load row; deeper check }` fast-path so admins skip the read. Three auth checks and a branch where there used to be one (`internal/server/apiserver/task.go`, `link.go`, `log.go`).
- **Request-only handlers need fudges.** `CreateTask` passes a literal `authscope.WithTaskArchived(false)` (no row to supply it).
- **`SubmitRunnerEvents`** is a batch, dual-caller RPC (runner *and* driver) that doesn't fit the one-task-per-handler model; it authorizes per-event against each loaded row, an accepted "no all-or-nothing" wart (`internal/server/apiserver/runner.go`).
- **Behavior drift.** Cross-org `ListLinks`/`ListLogs`/`ListEventsByTask` changed from empty-list to `NotFound` as a side effect of adding the org-scoped row load for auth.
- **Forget-proofing tax.** `model.Task.ScopeAttr()`, the `task.archived:"false"` predicate stamped on every minted scope (`internal/auth/agentauth/scope.go`), and the `Allow`-vs-`AllowOp` correctness subtleties (`eliminate-runner-socket-proxy` §5) all exist *only* because the narrow token rides the general handlers.

None of this complexity serves the human/admin/API-key callers. It is pure tax from making the general surface agent-safe.

## Design

Give the in-container agent a **dedicated Connect service** whose methods map 1:1 to the `xagent` MCP tool surface, where **"my task" is resolved from the caller's token identity, never from a request field**. Authorization concentrates in this one small surface; the general handlers revert to plain admin/user enforcement.

Three pieces:

1. A new `AgentService` Connect service (`proto/xagent/v1/agent.proto`), mounted behind an interceptor that requires an **agent token**.
2. The agent token carries a **task-identity claim** (`TaskID` + capability flags) instead of instance-predicated task scopes. The handler resolves the task from the claim; there is no `task_id` to re-authorize, so the confused-deputy surface disappears.
3. The general `XAgentService` task handlers shed the per-instance branching and go back to a single coarse op check.

### 1. `AgentService` — the agent's own surface

A separate Connect service rather than dedicated methods on `XAgentService`, so agent traffic is distinguishable at the service boundary (one interceptor guards the whole surface; audit/telemetry split by service path):

```proto
// proto/xagent/v1/agent.proto
service AgentService {
  // "My task" — resolved from the token, no id parameter.
  rpc GetMyTask(GetMyTaskRequest) returns (GetMyTaskResponse);
  rpc UpdateMyTask(UpdateMyTaskRequest) returns (UpdateMyTaskResponse);
  rpc Report(ReportRequest) returns (ReportResponse);
  rpc CreateMyLink(CreateMyLinkRequest) returns (CreateMyLinkResponse);

  // Driver-owned lifecycle event (replaces the driver's SubmitRunnerEvents call).
  rpc ReportMyTaskEvent(ReportMyTaskEventRequest) returns (ReportMyTaskEventResponse);

  // GitHub token — gated by the github_token capability.
  rpc GetMyGitHubToken(GetMyGitHubTokenRequest) returns (GetMyGitHubTokenResponse);
}
```

Note what's **absent**: `GetMyTaskRequest`, `UpdateMyTaskRequest`, `ReportRequest`, `CreateMyLinkRequest`, `ReportMyTaskEventRequest` carry **no `task_id`**. The handler reads it from the identity:

```go
// internal/server/apiserver/agent.go (new file)
func (s *Server) GetMyTask(ctx context.Context, req *xagentv1.GetMyTaskRequest) (*xagentv1.GetMyTaskResponse, error) {
    agent := apiauth.MustAgentCaller(ctx) // TaskID + OrgID + Capabilities, from the token
    task, err := s.store.GetTask(ctx, nil, agent.TaskID, agent.OrgID)
    if err != nil {
        return nil, ... // NotFound (the task was deleted)
    }
    if task.Archived {
        // Revocation: an archived task's token is dead. One explicit boolean
        // check in one place — no archived *predicate* on any scope.
        return nil, connect.NewError(connect.CodePermissionDenied, errors.New("task is archived"))
    }
    // ... assemble events/links exactly as GetTaskDetails does today ...
}
```

`UpdateMyTask`, `Report` (a log upload of type `llm`/`mcp`), and `CreateMyLink` follow the same shape: resolve `agent.TaskID`, load the row org-scoped, reject if archived, act. There is **no scope predicate** in this surface — the token *is* the authority to act on exactly one task, and "archived ⇒ revoked" is a plain field check at the single choke point. Because every method names "my task," **no request id is ever re-validated against the token** — there is no second task an agent token can address.

#### Capability gating

The agent surface has exactly one capability-gated method left: `GetMyGitHubToken`, behind the `github_token` capability (the `child_tasks` capability was removed with child tasks in #954). The handler calls `requireCapability(agent, agentauth.CapabilityGitHubToken)`; without the capability it returns `PermissionDenied`, and the MCP layer simply doesn't register the `get_github_token` tool, as it does today via `hasCapability`.

#### `ReportMyTaskEvent` — the driver-owned-events cleanup

This is the concrete payoff for the `SubmitRunnerEvents` wart. The driver only ever reports about **its own** task, with the version-bypass convention (`driver-owned-events` uses `Version: 0`). So the agent surface gets a single-event, no-`task_id`, no-`version` method:

```proto
message ReportMyTaskEventRequest {
  string event = 1;  // "started", "stopped", "failed"
}
```

```go
func (s *Server) ReportMyTaskEvent(ctx context.Context, req *xagentv1.ReportMyTaskEventRequest) (*xagentv1.ReportMyTaskEventResponse, error) {
    agent := apiauth.MustAgentCaller(ctx)
    // Same state-machine application as SubmitRunnerEvents, but for one task
    // (agent.TaskID), version-bypass, no per-event auth loop.
}
```

The **runner** keeps its own batched, versioned event path (renamed `SubmitRunnerEvents` → it stays on `XAgentService` / the runner surface), used for the cases only the runner can report: a container that died without the driver reporting, dispatch failures, and reconcile (`driver-owned-events` §"The runner speaks only when the driver can't"). The runner authenticates with its org-scoped `xat_` admin key, so that handler drops to a single coarse op check — its per-event `Allow(..., task.ScopeAttr()...)` loop (which only ever mattered for the now-removed driver caller) goes away. The dual-caller / batch / "no all-or-nothing" wart is resolved by *splitting the callers*, not by making one RPC serve both.

### 2. Token shape: a task-identity claim, not instance scopes

`eliminate-runner-socket-proxy` §2 deliberately dropped a `TaskID` claim in favor of instance-predicated scopes, because the agent shared the general surface and "match the request's `task_id` against the token's `task.id` predicate" was the only way to constrain a shared handler. A dedicated surface inverts that trade: the handler resolves identity, so it wants **identity**, not a bag of predicates.

The token stays an **ordinary app JWT signed with the existing `appKey`** — no second key, one verification path, exactly as today. The only change is *what it carries*: add optional `TaskID` and `Capabilities` to `apiauth.AppClaims` instead of the narrow `Scopes` set:

```go
// internal/auth/apiauth/jwt.go
type AppClaims struct {
    jwt.RegisteredClaims
    Email        string           `json:"email"`
    Name         string           `json:"name"`
    OrgID        int64            `json:"org_id"`
    Role         string           `json:"role,omitempty"`
    Scopes       authscope.Scopes `json:"scopes,omitempty"`
    TaskID       int64            `json:"task_id,omitempty"`      // agent tokens only
    Capabilities []string         `json:"capabilities,omitempty"` // agent tokens only
}
```

`CreateTaskToken` (`internal/server/apiserver/tasktoken.go`) is unchanged in *interface* — the runner still calls it with `task_id` + capabilities, the server still derives everything from the authoritative task row — but it now mints a token carrying `TaskID = task.ID` and `Capabilities` instead of calling `agentauth.Scopes(...)`. `apiauth.authenticate` populates a caller from the claim; an `AgentService` interceptor requires `TaskID != 0`:

```go
func RequireAgentInterceptor() connect.UnaryInterceptorFunc { /* reject if caller has no TaskID */ }

func MustAgentCaller(ctx) *AgentInfo { /* {TaskID, OrgID, Capabilities} from the claim */ }
```

**Why identity is better here** (the issue's "Why it's better"): authorization is implicit and unforgeable. A token for task N can *only* ever name task N (the handler reads `agent.TaskID`); there is no request field to mismatch, so the confused-deputy class of bug is structurally absent rather than defended against. Revocation stays "archive the task" but becomes a direct `if task.Archived` check at the choke point instead of a `task.archived:"false"` predicate stamped on every scope and re-evaluated against `ScopeAttr()` in every handler.

What about `task_token.create` and `github_token.create`? `OpTaskTokenCreate` stays a real scope — it gates the **runner's** `xat_` key calling `CreateTaskToken` (a no-instance capability check, unchanged). `github_token.create` moves from a minted scope to a **capability flag** on the agent token (`GetMyGitHubToken` checks `requireCapability(agent, CapabilityGitHubToken)`), so it leaves the scope engine entirely.

### 3. General handlers revert to plain enforcement

Once the agent is off the general surface (migration below), every wart the issue lists is deleted, not refactored:

| Handler | Before (#911) | After |
|---|---|---|
| `GetTask`, `GetTaskDetails`, `UpdateTask`, `Archive/Unarchive/Cancel/RestartTask` | `AllowOp(op)` pre-gate + post-load `Allow(op, task.ScopeAttr()...)` | single `Allow(op)` op-level check |
| `CreateTask` | `Allow(OpTaskCreate, WithTaskWorkspace/Runner/Archived(false))` | `Allow(OpTaskCreate)` |
| `CreateLink`, `UploadLogs` | `AllowOp` + row-load + `Allow(op, task.ScopeAttr()...)` | `Allow(op)` |
| `ListLinks`, `ListLogs`, `ListEventsByTask` | `AllowOp` + conditional row-load for predicate | `Allow(op)` (restores empty-list, not `NotFound`, cross-org) |
| `SubmitRunnerEvents` (runner-only now) | per-event post-load `Allow(..., task.ScopeAttr()...)` | single `Allow(OpTaskWrite)` op-level check |

The behavior drift the issue notes (cross-org `ListLinks`/`ListLogs`/`ListEventsByTask` returning `NotFound` instead of empty) reverts to the pre-#911 empty-list behavior, because there's no longer a per-instance auth reason to load the row.

`model.Task.ScopeAttr()`, `authscope.WithTaskArchived` / `AttrTaskArchived`, and `agentauth.Scopes`'s `task.archived:"false"` stamping were introduced **solely** for the agent-on-general-handlers path (`eliminate-runner-socket-proxy` §3/§5). With the agent gone they have no remaining consumer and can be removed. The generic `authscope` engine (`Scope`, `Scopes`, `Allow`/`AllowOp`, the op taxonomy) **stays** — it still enforces the user-facing surface and is the substrate for the unfinished Phases 3/4 of `scope-based-permissions` (narrow `xat_` keys, role-derived sessions). This proposal removes the agent's *use* of per-instance task scopes, not the scope machinery.

### 4. MCP layer cuts over

`internal/agentmcp/xmcp.go` is the only client of these RPCs. Each tool re-points from a general RPC to the agent RPC, dropping the `s.task.ID` it threads into every request today:

| MCP tool | Today | After |
|---|---|---|
| `get_my_task` | `GetTaskDetails{Id: task.ID}` | `GetMyTask{}` |
| `update_my_task` | `UpdateTask{Id: task.ID, ...}` | `UpdateMyTask{...}` |
| `report` | `UploadLogs{TaskId: task.ID, ...}` | `Report{...}` |
| `create_link` | `CreateLink{TaskId: task.ID, ...}` | `CreateMyLink{...}` |
| `get_github_token` | `CreateGitHubToken{}` | `GetMyGitHubToken{}` |

The driver (`internal/agent/driver.go`) swaps its `SubmitRunnerEvents` call for `ReportMyTaskEvent`. The injected `XAGENT_TOKEN` is unchanged in transport — still a Bearer app JWT — only its claim contents differ.

## Migration / cutover

The constraint mirrors `eliminate-runner-socket-proxy`: in-flight containers hold already-minted scope-based tokens hitting the general handlers, and a container is wired once at creation. The general handlers must keep their per-instance checks until no scope-based agent token is in flight.

- **Phase 0 — additive, inert.** Add `agent.proto` + `AgentService` handlers, the `RequireAgentInterceptor`, the `TaskID`/`Capabilities` claim fields, and `ReportMyTaskEvent`. Nothing mints or calls them yet; the scope-based path stays live.
- **Phase 1 — cut clients over.** `CreateTaskToken` mints identity tokens (`TaskID` + capabilities, no task scopes); `agentmcp` and the driver call `AgentService`. New containers use the new surface; in-flight containers keep their scope tokens on the general handlers. Roll out to one runner, verify, fan out.
- **Phase 2 — strip the general handlers.** Once drained (all tasks created before Phase 1 are terminal/restarted — same drain signal as `eliminate-runner-socket-proxy` Open Question #4), remove the per-instance branching from the general task handlers, drop the runner-event per-event auth loop, and delete `ScopeAttr` / `WithTaskArchived` / the minter's archived stamping.

Rollback before Phase 2 is a runner-config flip back to minting scope tokens; the Phase 0 additions are inert without an identity token in play.

## Trade-offs

- **A second RPC surface to maintain.** `CreateMyLink` duplicates logic that also exists generically (`CreateLink`). This is the deliberate cost: the duplication buys handlers with no per-instance auth, and the shared *store* methods (`store.CreateLink`, `store.GetTask`) are still reused — only the thin RPC handler is duplicated. The general handlers get simpler in exchange.
- **Reintroducing a `TaskID` claim** that #907 removed. It returns for a different reason: the agent now has its *own* surface that wants identity, not a shared one that needed predicates. It rides the existing `AppClaims` / `appKey` / verification path, so it's a field, not a new credential type.
- **Capabilities leave the scope engine.** `github_token.create` becomes a token capability flag rather than a scope. Acceptable — it was always a workspace-capability concept (`agentauth.CapabilityGitHubToken`), and the agent surface is the only consumer.
- **The scope engine still earns its keep** on the user-facing surface, so this is not an argument to remove `authscope` — only to stop forcing the *agent* through per-instance task scopes.

## Open Questions

1. **Separate service vs. dedicated methods.** A separate `AgentService` (recommended) gives a clean interceptor boundary and audit split, at the cost of a second service registration and generated client. Dedicated `My*` methods on `XAgentService` avoid that but blur the boundary the interceptor guards. Recommend separate.
2. **Claim shape.** Extend `AppClaims` with `TaskID`/`Capabilities` (recommended — one verification path, minimal change), or introduce a dedicated `AgentClaims` type for a cleaner separation at the cost of a second claims struct and a verification branch?
3. **Do agent tokens keep *any* scope?** The recommendation drops task scopes entirely and authorizes by identity + capability. An alternative keeps a single marker scope for uniform interceptor logic. Recommend none — the `TaskID` claim presence *is* the marker.
4. **`ReportMyTaskEvent` validation.** The driver sends `"started"/"stopped"/"failed"` with version-bypass. Should the agent surface constrain which transitions a driver may report (e.g. reject a driver-sent `started` for a task already `completed`), or keep the state machine's existing idempotent guards as the sole gate (as `SubmitRunnerEvents` does today)? Recommend the latter — the guards already make duplicates safe.
