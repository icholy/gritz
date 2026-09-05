# Fly.io Sprites Backend for the Runner

Issue: https://github.com/icholy/xagent/issues/1521

## Problem

The runner's sandbox runtime is abstracted behind `backend.Backend`
(proposals/implemented/runner-backend-interface.md), and several implementations
exist or are proposed: Docker (single host), Nomad (a cluster the operator owns),
Firecracker (per-task KVM, single host), and the AWS-managed options — Lambda
MicroVMs and AgentCore. Each managed option pays a price:

- **Lambda MicroVMs** (proposals/implemented/lambda-microvm-backend.md) and
  **AgentCore** (proposals/draft/agent-core-backend.md) are AWS-only and require
  a **purpose-built image build pipeline** (zip → S3 → `create-microvm-image`, or
  a Bedrock runtime resource), plus IAM, plus staging. A *suspended* Lambda VM
  still consumes the account memory quota, so the idle ceiling scales with every
  non-archived task.
- **Firecracker** (proposals/draft/firecracker-backend.md) and **Docker** run on
  compute the runner host owns.

There is no fully-managed backend that (1) runs on **generic, persistent Linux
VMs** with no per-workspace image build pipeline, (2) costs **≈ nothing while
idle** — no compute *and* no standing quota — so a completed event-driven task is
cheap to keep around indefinitely, and (3) reaches the guest through a
**first-class control API** (exec / filesystem) instead of a bespoke in-guest
shim + auth-token proxy.

[Fly.io Sprites](https://fly.io/sprites/) fit that gap. A Sprite is a persistent,
hardware-isolated Linux VM ("a computer for agents") that:

- **creates in ~1–2s** and boots from a **standard, pre-cached base image** (the
  platform keeps a warm pool), so there is *no image build step*;
- keeps a **durable 100GB root filesystem** — files, packages, repos, and on-disk
  state survive sleep (RAM does not);
- **auto-sleeps when idle at practically zero cost** and **auto-wakes on the next
  API call**;
- exposes a **REST API + SDKs** (`api.sprites.dev/v1`, `github.com/superfly/sprites-go`)
  for `exec` (streaming, with exit codes), a filesystem API, checkpoints, and
  DNS-based egress policy.

This maps cleanly onto how the runner already reuses an exited-but-preserved
sandbox on the next routed event: a Sprite that finished its work sleeps (disk
preserved), and the next `Launch(reuse)` wakes it and re-runs the driver against
the same 100GB filesystem — no re-clone, no re-setup, and *no standing quota*
while it waits.

This proposal adds a `fly` backend implementing `backend.Backend`: fully managed,
no host compute, no per-workspace image pipeline, near-zero idle cost.

## Background: the Sprites contract

Several facts about Sprites drive the design.

1. **A Sprite is a persistent Linux VM, created by name.** `POST /sprites {"name":
   "..."}` provisions one from the platform's standard base image and returns in
   ~1–2s. There is no per-task or per-workspace image to build — the decisive
   difference from Lambda MicroVMs and AgentCore. The flip side (see trade-offs):
   you **cannot boot an arbitrary OCI image**, so a workspace's toolchain must be
   *provisioned into* the Sprite rather than baked into an image.

2. **The disk persists across sleep; RAM does not.** Every Sprite has a durable
   100GB root filesystem backed by object storage. Files, installed packages, and
   on-disk state survive sleep/wake; running processes and in-memory state do not.
   This is exactly the Docker exited-container model — the writable filesystem is
   preserved, the process is not — so it slots into the runner's existing
   reuse-exited-sandbox path unchanged.

3. **Sprites auto-sleep when idle and auto-wake on demand.** An idle Sprite puts
   itself to sleep and "costs practically nothing while asleep"; the next API call
   (exec, fs, get) wakes it. Unlike a suspended Lambda MicroVM, a sleeping Sprite
   does **not** hold a standing memory quota — the idle cost is genuinely ≈ 0. This
   is what makes "keep a completed-but-subscribed task's sandbox around
   indefinitely" cheap.

4. **Commands run over a first-class exec API.** `WSS /sprites/{name}/exec` (and a
   `POST` form) runs a command with streaming stdio and returns an **exit code**;
   `GET /sprites/{name}/exec` lists active sessions and `WSS
   /sprites/{name}/exec/{session-id}` **re-attaches** to a running one. The exec
   process runs server-side in the Sprite, so dropping the WebSocket does not kill
   it (like closing `docker logs`) — the runner re-attaches by session id. This is
   the transport for launching, supervising, and re-adopting the driver, and it
   needs **no in-guest shim and no auth-token proxy** (unlike Lambda).

5. **Files are read/written over a filesystem API.** `POST /sprites/{name}/fs/{path}`
   writes a file, `POST .../fs/chmod` sets its mode. This is the analog of the
   Docker backend's `CopyToContainer` tar — the channel for provisioning the
   driver binary and the agent config.

6. **Egress is default-on and DNS-policy controllable; no ingress is needed.**
   The driver only needs **outbound** access to reach the server and GitHub — it
   connects *out*, exactly as under Docker, using its task JWT
   (post socket-proxy elimination). `POST /sprites/{name}/policies/network` sets
   DNS-based egress allow rules. The runner reaches the Sprite through the Sprites
   **control API** (exec/fs), not through a network path into the guest, so —
   unlike Lambda's managed proxy — there is no ingress, no auth-token minting, and
   no SSE lifecycle stream to maintain.

7. **Checkpoints are a fast, first-class feature.** `POST
   /sprites/{name}/checkpoints` snapshots system state in ~1s (metadata-only), and
   `.../checkpoints/{id}/restore` rolls back. Not load-bearing for the core design,
   but a natural future optimization for warm per-workspace templates (open
   question).

The structural fit with `backend.Backend`: a Sprite is a runner-independent
sandbox we *launch and observe* over an API (like a container or a microVM), whose
disk is preserved when the work stops (like an exited container) at ≈ zero idle
cost (better than a suspended microVM). It honors both restart-survival and
graceful-stop, without any control-plane credential or shim inside the guest.

## Design

### Overview

A new package `internal/runner/backend/fly` implements `backend.Backend`. It
reaches the service through `internal/x/sprites`, a small REST client (modelled on
`api.sprites.dev/v1`, wrapping `github.com/superfly/sprites-go` for `exec`/`fs`
where it is mature and issuing `create`/`checkpoint`/`policy` calls directly).
Selection follows the existing seam in `internal/command/runner.go`:

```
xagent runner --backend fly
```

Per task, the backend maps the task to **one Sprite** named `xagent-<task-id>`,
and:

1. **Creates** the Sprite (`POST /sprites`), if it doesn't already exist.
2. **Provisions** it once (gated by an on-disk marker): detect the Sprite's arch,
   write the matching prebuilt driver binary to `backend.BinaryPath` and
   `spec.Files` (the agent config) over the **fs API**, `chmod 0755` the binary,
   apply the workspace's egress policy, and run the workspace's `fly.setup`
   provisioning commands over **exec**.
3. **Runs the driver** as a background **exec session** (`spec.Cmd` + `spec.Env`),
   recording the session id in the handle.

The driver connects to the server with its task token exactly as under Docker.
The orchestrator (`runner.Runner`), the driver, the server API, the database, and
the task state machine are **untouched** — the driver already connects by URL +
token and neither knows nor cares what launched it (the point of the
socket-proxy-elimination and driver-owned-events prerequisites).

When the driver exits, the exec session ends and the Sprite goes idle and
**auto-sleeps** — disk preserved, cost ≈ 0. This is the exited-but-preserved
husk (`StateExited`). On the next run, `Launch(reuse)` wakes the Sprite and
re-execs the driver against the same filesystem — the resume path, symmetric with
reusing an exited Docker container.

### Lifecycle: sleep on exit, wake + re-exec on the next run

| | Docker | **Fly Sprites** |
|---|---|---|
| driver exits | container exits (state preserved, no cost) | exec session ends → Sprite **auto-sleeps** (100GB disk preserved, cost ≈ 0, **no standing quota**) |
| next run / restart | restart exited container | any API call **auto-wakes** the Sprite; re-exec the driver against the preserved disk |
| task archived/deleted | remove the container | `DELETE /sprites/{name}` |
| graceful stop | SIGTERM → 30s → SIGKILL | send SIGTERM to the driver session → 30s → SIGKILL |

The payoff mirrors Lambda's suspend/resume but is cheaper at rest: an event-driven
task (subscribed to a PR) completes, the Sprite sleeps at ≈ zero cost, and on the
next routed event the runner's existing `Start → Probe StateExited →
Launch(reuse)` path wakes it and re-runs the driver with its
`SetupCommandsCompleted` / `Started` / `NextEventToken` markers already on disk —
no re-clone, no re-provision. Because a sleeping Sprite holds no standing quota
(unlike a suspended Lambda VM, which counts against the account memory quota),
the "terminate only on archive" ceiling does not scale with the count of idle
tasks.

**RAM is not preserved across sleep** — but neither is it for an exited Docker
container, so this is *equivalent* to Docker, not a regression versus it. (Lambda
preserves memory; Sprites and Docker preserve only the filesystem. The driver is
designed for exactly this: it persists its resumable state to the agent config
file on disk, not in memory.)

### Workspace config

Per the backend-interface pattern, each backend gets its own sibling config
section that it alone validates and consumes (Docker reads `container:`, Lambda
reads `lambda_microvm:`). `workspace.Workspace` gains a `fly:` section:

```yaml
workspaces:
  pets-workshop:
    fly:
      region: iad                 # optional; Sprite placement region
      setup:                      # provisioning commands, run ONCE on a fresh
        - apt-get update          # Sprite by the backend before the driver.
        - apt-get install -y git nodejs npm
        - npm install -g @anthropic-ai/claude-code
      egress_allow:               # optional DNS-based egress allowlist
        - api.anthropic.com
        - github.com
        - "*.githubusercontent.com"
      cpu: 2                      # optional resource policy
      memory_mb: 4096             # optional resource policy
      working_dir: /root
      environment:
        CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    agent:
      type: claude
      cwd: /root/pets-workshop
      ...
```

```go
type Fly struct {
	// Region is the Sprite placement region (optional; platform default).
	Region string `yaml:"region"`
	// Setup are commands the backend runs ONCE on a fresh Sprite (after
	// provisioning the driver binary + agent config, before the driver) to
	// install the workspace toolchain. Gated by an on-disk marker, so a
	// resumed Sprite skips them. Distinct from the agent-level `commands:`,
	// which the driver runs and tracks via SetupCommandsCompleted.
	Setup []string `yaml:"setup"`
	// EgressAllow is a DNS-based egress allowlist applied as a network policy.
	// Empty leaves the Sprite's default (egress-on) policy in place.
	EgressAllow []string `yaml:"egress_allow"`
	// CPU / MemoryMB are optional resource-policy constraints.
	CPU        int `yaml:"cpu"`
	MemoryMB   int `yaml:"memory_mb"`
	WorkingDir string `yaml:"working_dir"`
	User       string `yaml:"user"`
	Environment map[string]string `yaml:"environment"`
}
```

The critical difference from `container:` is that there is **no `image:` field** —
a Sprite boots the platform's standard base image, and the workspace's toolchain
is installed by `setup:` (see the trade-off below). A workspace may set both
`container:` and `fly:` so one `workspaces.yaml` serves runners with different
backends; `ValidateWorkspace` checks only the `fly:` section, and
`RegisterWorkspaces` skips (with a warning) workspaces this backend can't run.
The Sprites API token is not in `workspaces.yaml` — it resolves from the standard
`SPRITES_API_TOKEN` env var on the runner, so the config stays expansion-free and
safe to share across a fleet.

### Provisioning files and the toolchain

The Docker backend copies the driver binary and agent config into the container
with `CopyToContainer` (a tar stream) after create. Sprites have a **filesystem
API** instead, so the backend writes each file directly:

- **Agent config** (`spec.Files`, small JSON) → `POST /sprites/{name}/fs/{path}`
  per file, at the declared absolute path.
- **Driver binary** (`backend.BinaryPath`, ~tens of MB) → `POST
  /sprites/{name}/fs/usr/local/bin/xagent`, then `POST .../fs/chmod {mode: 0755}`.
  The backend picks the architecture-matching binary from `prebuilt.ReadBinary`;
  the Sprite's arch is detected once via an exec probe (`uname -m`) or the
  Sprite's metadata. `prebuilt` is reused as-is.

The toolchain that the Docker workspace bakes into its OCI image (git, node, the
agent CLI) is installed by the `fly.setup` commands, run **once** on a fresh
Sprite. All provisioning (files + toolchain) is gated by an on-disk marker
(`/var/lib/xagent/.provisioned`), so a *resumed* Sprite — woken from sleep with
its disk intact — **skips** provisioning entirely, reproducing the Docker
backend's provision-at-create-only semantics and never clobbering the driver's
on-disk markers.

### The Handle

The `Handle` the runner persists is `{Type: "fly", ID: "xagent-<task-id>", Data:
…}`. `ID` is the Sprite name — unique, stable for the task's life, and enough to
address every API call (`exec`, `fs`, `get`, `delete`) and to re-adopt the Sprite
after a runner restart. `Data` carries what the backend needs to *re-attach to the
running driver* but not for identity — most importantly the current exec
**session id**:

```go
// stored opaque in Handle.Data (taskstate.Record.Data), never decoded by the store
type handleData struct {
	SessionID string `json:"session_id"` // active driver exec session, for Wait re-attach
}
```

Because the Sprite name *is* the handle id, a restarted runner re-adopts a task's
Sprite from the `taskstate` store (and, as a backstop, by listing Sprites filtered
to the `xagent-` name prefix / a runner tag). This is the analog of the Docker
backend re-adopting containers by id, but it holds only a handle — no rootfs, no
networking, no local process.

### Backend method mapping

| Method | Implementation |
|---|---|
| `ValidateWorkspace` | Validate the `fly:` section: `setup` entries well-formed, `cpu`/`memory_mb` non-negative, `egress_allow` DNS patterns parseable. There is no required `image`. |
| `Launch` (fresh, `reuse == nil`) | `POST /sprites {name: "xagent-<task-id>"}` (idempotent: adopt an existing same-named Sprite). Provision once (arch-detect → write driver binary + `spec.Files` over the fs API, `chmod 0755` the binary, apply the egress policy, run `fly.setup`), marker-gated. Start the driver as a background exec session (`spec.Cmd` + `spec.Env`); return `Handle{ID: name, Data: {session_id}}`. |
| `Launch` (reuse) | Address the Sprite by `reuse.ID`. If it's gone (`GET` 404) → `backend.ErrGone`. Otherwise the API call **auto-wakes** it; **skip** provisioning (marker present, disk intact) and re-exec the driver, returning the same handle id with the new session id. This is the resume path — the analog of restarting an exited Docker container, against the preserved 100GB filesystem. |
| `Probe` | `GET /sprites/{name}`: absent → `StateGone`. Present with the driver's exec session active (`GET .../exec`) → `StateRunning`. Present with no active driver session (idle/asleep) → `StateExited` (husk preserved: disk kept, re-runnable). A sleeping-after-completion Sprite must look like an exited-but-preserved container so `Start → Probe StateExited → Launch(reuse)` and `Prune → Destroy` work unchanged. |
| `Signal` | Graceful stop: send **SIGTERM** to the driver's exec session (a WS control frame, or an exec `kill -TERM <pid>`), wait a 30s grace, then SIGKILL — the in-Sprite mirror of the Docker backend's SIGTERM→SIGKILL. Returns `signalled=true` if a running session was reached; the driver catches SIGTERM and owns its terminal report. |
| `Destroy` | `DELETE /sprites/{name}`. Idempotent: a 404 (already gone) is not an error. Reached via `Prune` on task archive/delete. |
| `Wait` | Attach to the driver's exec session (`WSS .../exec/{session_id}` from `Handle.Data`, or the live session from `Launch`) and block until it returns an **exit code**. Safe to call after a runner restart: the driver runs server-side, so re-attach by session id re-adopts it. Return shapes per the contract: clean driver exit → `(code, nil)`; session gone with no recoverable code (Sprite reaped, or exited-during-downtime and slept) → `(ExitLost, nil)`; runner shutdown → `(_, ctx.Err())`, leaving the Sprite alive for next-boot rehydration. Transient WS drops are swallowed by reconnect (the exec process is unaffected by a dropped attach). |
| `Close` | Close the REST client and any open exec attaches; leaves Sprites running/sleeping — they outlive the runner exactly as containers do today. |

**Exit-code fidelity** follows the backend contract. The exec API returns the
driver's **true process exit code**, so the clean, code-bearing path is the common
one. The driver-owned-events invariant still governs correctness: the driver
reports its terminal status directly to the server, and the runner's reconcile
treats an exited sandbox whose task is still `RUNNING` as a lost report
(`failed`). When the session is gone and no code is recoverable, the backend
reports `ExitLost` (-1) and lets the server's terminal-state guard in
`internal/model/task.go` reconcile — the same fallback the other backends use.

### Package layout

```
internal/runner/
├── runner.go                 unchanged orchestrator (owns the taskstate store)
├── taskstate/                shared store; the runner is the only writer
├── backend/
│   ├── backend.go            Launch/Probe/Signal/Destroy/Wait over opaque Handles
│   ├── docker/               unchanged
│   ├── lambdamicrovm/        unchanged
│   └── fly/
│       └── fly.go            Fly Sprites implementation
└── workspace/                +Fly config section
internal/x/sprites/           general-purpose Sprites REST client (create, exec,
                              fs, checkpoint, network policy); wraps sprites-go
internal/command/             runner.go backend switch gains a `fly` case
```

Unlike Lambda/AgentCore, there is **no in-guest shim** (`microvm-shim`,
`agentcore-shim`) and **no new hidden subcommand** — the driver is `exec`'d
directly over the Sprites API, and the runner reaches the Sprite through that same
API rather than a network path into the guest. This is the design's main
simplification versus the managed AWS backends.

### CLI

```
xagent runner --backend fly \
  [--fly-region iad]
```

`--fly-region` has an `XAGENT_FLY_REGION` env source (a default overridable per
workspace via `fly.region`). The Sprites API token resolves from
`SPRITES_API_TOKEN` (standard SDK env var); no token flag is introduced.
`internal/command/runner.go`'s backend switch gains a `fly` case constructing
`fly.New(...)` with the runner id, region, and a logger. The state directory is
the shared `taskstate` store's, not a backend-private one. `xagent download` is
not extended — there is no host kernel or hypervisor binary to fetch; the REST
client is compiled in.

### Testing

- Unit tests (no Sprites account): `ValidateWorkspace`; handle construction (id =
  Sprite name, session id in `Data`); provisioning-plan assembly (fs writes for
  the driver binary + files, chmod, egress policy, setup commands) with the REST
  client behind a small interface so calls are mocked (matching the `dockerx` moq
  pattern); `Probe` state mapping (absent → Gone, session active → Running, idle →
  Exited); exit-code extraction from an exec result; the marker-gated
  provision-once logic (fresh runs setup, reuse skips it).
- `Wait` is tested against an httptest WebSocket server standing in for the exec
  endpoint: clean exit → `(code, nil)`; attach dropped mid-run then reconnected →
  no spurious exit; session gone → `(ExitLost, nil)`; ctx cancel → `ctx.Err()`.
- Integration tests in `backend/fly`, skipped unless a `SPRITES_API_TOKEN` is set
  — mirroring how the Docker e2e tests require a daemon and the Lambda tests
  require AWS creds. They cover create → provision → run, sleep-on-exit →
  `Probe StateExited`, wake + re-exec on `Launch(reuse)` against the preserved
  disk, provision-once-across-reuse, graceful stop via `Signal`, `Destroy`
  idempotency, and re-adoption after a simulated runner restart.
- The orchestrator needs no new tests: it already runs against `BackendMock`.

### What doesn't change

The orchestrator (`runner.go`), the durable event outbox, proto definitions, the
database schema, the driver, and the task state machine are untouched. The Docker
and Lambda backends are unaffected beyond the shared `ValidateWorkspace` pattern.
`prebuilt` is reused as-is to source the arch-matching driver binary, now written
over the fs API instead of tar-copied into a container.

## Implementation Plan

1. **Sprites REST client** — Delivers: `internal/x/sprites` with `create`, `get`,
   `delete`, `exec` (start + attach + exit code), `fs write/chmod`, and
   `network policy` calls, plus a mockable interface. Depends on: nothing.
   Verifiable by: unit tests against a mock HTTP/WS server; a smoke test gated on
   `SPRITES_API_TOKEN`.
2. **Workspace `fly:` config** — Delivers: the `Fly` struct on
   `workspace.Workspace`, YAML parsing, and field docs. Depends on: nothing.
   Verifiable by: config round-trip unit tests; a workspace with both `container:`
   and `fly:` loads.
3. **`fly` backend** — Delivers: `internal/runner/backend/fly` implementing
   `backend.Backend` (Launch/Probe/Signal/Destroy/Wait/ValidateWorkspace/Close)
   over the client from (1), marker-gated provisioning, arch detection, and the
   handle shape. Depends on: (1), (2). Verifiable by: unit tests with the mocked
   client; `BackendMock`-level parity with the interface contract.
4. **Runner wire-up** — Delivers: the `fly` case in
   `internal/command/runner.go`'s backend switch and the `--fly-region` flag.
   Depends on: (3). Verifiable by: `xagent runner --backend fly` starting and
   registering `fly:` workspaces end to end against a real Sprites account.
5. **Integration tests** — Delivers: the account-gated e2e suite in `backend/fly`.
   Depends on: (3), (4). Verifiable by: the suite passing with a
   `SPRITES_API_TOKEN` set; skipped otherwise.

## Trade-offs

**Generic base image + provisioning vs. an unmodified OCI image.** This is the
central divergence from Docker/Nomad/Firecracker, which boot the workspace's OCI
image directly. A Sprite always boots the platform's *standard* base image — you
cannot supply `ghcr.io/icholy/xagent-workspace-debian:latest` — so the workspace
toolchain must be installed by `fly.setup` on a fresh Sprite. The mitigations:
provisioning runs **once** (marker-gated) and the result lives on the durable
100GB disk, so a reused/resumed Sprite pays nothing; and the first-run cost is
bounded by an `apt-get`/`npm -g` install, not a full image build. It is still a
real regression in `workspaces.yaml` portability and first-launch latency versus
the image-based backends, and it shifts the toolchain definition from a Dockerfile
into `setup:` commands. Warm per-workspace **checkpoints** (below) are the path to
erasing the first-run cost.

**RAM not preserved across sleep.** A sleeping Sprite loses in-memory state, so a
resumed driver starts as a fresh process. But this is **identical to Docker's
exited-container reuse** (which also loses process memory and preserves only the
filesystem), and the driver already persists its resumable state to the agent
config file on disk. So relative to the backend we most want parity with (Docker),
this is not a regression; relative to Lambda (which preserves memory on suspend),
it is the price of the much cheaper idle state.

**Auto-sleep timing vs. a long-running driver.** A Sprite sleeps when *idle*, and
RAM (including a running process) does not survive sleep — so if the platform
judged an actively-working agent "idle" and slept it, the driver would be killed
mid-task. An agent doing real work generates CPU/network activity and an attached
exec session, which is precisely the "not idle" signal; sleep is intended to fire
only after the work stops and the session ends. Still, the exact idle heuristic is
the platform's, so the backend should keep the exec attach open for the driver's
lifetime (activity + a supervision channel) and, if the platform exposes an idle
policy, disable *automatic* sleep while a driver session is active and let the
backend drive the "work is done → let it sleep" transition explicitly — the same
"own the timing" stance the Lambda backend takes toward auto-suspend. Confirming
the idle contract is an open question below.

**Reaching the guest via the control API vs. a network path.** Lambda needs a
managed proxy, a minted auth token, an in-guest shim, and an SSE lifecycle stream
to be *reached* and to learn when the driver exits. Sprites collapse all of that:
the runner reaches the Sprite over the exec/fs API, the exec call *returns the
exit code directly*, and there is no ingress to the guest at all. Fewer moving
parts and no in-guest credential — the main reason to prefer this backend where
Fly is an option. The cost is a dependency on the Sprites control plane being
reachable from the runner (it always is — that is the only way to drive a Sprite).

**REST client vs. the young Go SDK.** `github.com/superfly/sprites-go` today gives
a solid `exec` surface (an `exec.Cmd`-like `Cmd` with `Start`/`Wait`/`Output` and
an `ExitError.ExitCode()`), but its README marks *sprite creation* as "future
functionality." So the backend uses the SDK for exec/fs where it is mature and
issues `create`/`checkpoint`/`policy` calls against the documented REST API
(`api.sprites.dev/v1`) directly, behind one `internal/x/sprites` client. As the
SDK fills in, the client can delegate more to it without touching the backend.

## Comparison with the sibling backends

| | Docker | Firecracker | Lambda MicroVMs | **Fly Sprites** |
|---|---|---|---|---|
| Host owns hypervisor | n/a | yes | no | **no** |
| Runner-owned compute | yes | yes | no | **no** |
| Image | unmodified OCI | unmodified OCI | purpose-built (S3 build) | **generic base + `setup`** |
| Work survives runner restart | yes | yes | yes | **yes (re-adopt by name)** |
| On driver exit | container exits (state kept, no cost) | VM stays up | `suspend-microvm` (snapshot storage, quota held) | **auto-sleep (disk kept, cost ≈ 0, no standing quota)** |
| Reuse on next run | restart exited container | restart VM | `resume-microvm` (memory + disk) | **auto-wake + re-exec (disk preserved)** |
| Memory preserved across idle | no | yes (VM up) | yes | **no (like Docker)** |
| Graceful stop (SIGTERM) | yes | yes | yes (`/xagent/stop` proxy) | **yes (SIGTERM to exec session)** |
| Exit notification | docker wait | poll | SSE `driver-exited{code}` + control plane | **exec returns exit code** |
| Guest reachability | host (no socket in guest) | host | managed proxy + token + shim | **control API (exec/fs), no ingress, no shim** |
| File injection | tar copy | config disk | S3 bundle + presigned URL | **fs API** |

Fly Sprites is the managed option with the **fewest moving parts** (no image
pipeline, no in-guest shim, no proxy/token dance) and the **cheapest idle state**
(auto-sleep at ≈ zero cost with no standing quota), at the cost of a generic base
image that must be provisioned rather than an unmodified OCI image.

## Open Questions

1. **Idle/sleep contract.** What exactly triggers auto-sleep, and does an open
   exec attach with a running process reliably count as "active"? Can automatic
   sleep be disabled per-Sprite (so the backend owns the "work done → sleep"
   transition, as the Lambda backend owns suspend), or must we rely on the
   platform heuristic? This governs whether a long, CPU-light agent step can be
   slept out from under the driver.
2. **Toolchain provisioning vs. warm checkpoints.** Is per-workspace `setup:`
   (run once, cached on disk) sufficient, or should the backend maintain a
   **template Sprite** per workspace, checkpoint it after `setup`, and seed each
   task's Sprite from that checkpoint for near-zero first-run latency? This depends
   on whether a checkpoint can seed a *different* Sprite (the restore API is
   per-Sprite in the docs) or whether Fly exposes a "create from checkpoint/image"
   primitive.
3. **Arch selection.** Is a Sprite's CPU architecture knowable before first exec
   (from create options or Sprite metadata), or must the backend probe with
   `uname -m` before writing the prebuilt binary? Can the runner pin arch at
   create time so `prebuilt` selection is deterministic?
4. **Graceful-stop signal delivery.** Does the exec API forward SIGTERM to the
   remote process (like SSH), or must the backend resolve the driver's pid and
   `exec kill -TERM`? The `Signal` implementation and its 30s→SIGKILL grace depend
   on which is available.
5. **Concurrency and quotas.** Sprites bill per active VM and (cheaply) per
   sleeping disk. Should the orchestrator's `safesem.Semaphore` count *awake*
   Sprites only (idle ones cost ≈ 0), unlike the Lambda backend which must count
   suspended VMs against the memory quota? What account-level limits (max Sprites,
   region capacity) should the backend surface as backpressure rather than a hard
   `Launch` failure?
6. **Egress policy defaults.** Should the backend apply a default-deny egress
   policy with an allowlist derived from the workspace (server host, agent API,
   GitHub), or leave the Sprite's default egress-on posture and let `egress_allow`
   opt into tightening? Default-deny is safer but risks breaking agents that reach
   unlisted hosts.
