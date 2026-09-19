# DigitalOcean Agent Sandboxes (MicroVMs) Backend for the Runner

Issue: https://github.com/icholy/gritz/issues/1585

## Problem

The runner's sandbox runtime is abstracted behind `backend.Backend`
(`internal/runner/backend/backend.go`,
proposals/implemented/runner-backend-interface.md). Two implementations exist —
Docker (`backend/docker`, single host) and AWS Lambda MicroVMs
(`backend/lambdamicrovm`, proposals/implemented/lambda-microvm-backend.md) — and
three are proposed: Nomad (proposals/draft/nomad-backend.md), Firecracker
(proposals/draft/firecracker-backend.md), AgentCore
(proposals/draft/agent-core-backend.md), plus Fly.io Sprites
([#1522](https://github.com/icholy/gritz/pull/1522)).

Every *managed* option we have costs something structural:

- **Lambda MicroVMs** and **AgentCore** are AWS-only and need a purpose-built
  image pipeline (zip → S3 → `create-microvm-image`), IAM roles, an S3 staging
  bucket, and a hand-rolled SigV4 client (`internal/x/awsmicrovm`) because the
  control plane is in preview with no Go SDK. A *suspended* Lambda VM still holds
  account memory quota.
- **Fly Sprites** removes the image pipeline entirely, but at the cost of *not*
  being able to boot an arbitrary OCI image: the workspace toolchain has to be
  reinstalled into a generic base via a `fly.setup` block.
- **Docker**, **Nomad** and **Firecracker** run on compute the operator owns.

DigitalOcean's agent-sandbox primitive — shipped in godo as **MicroVMs** and in
the Terraform provider and CLI as **MicroDroplets** — sits in a gap none of these
fill: a **fully managed Firecracker microVM that boots an unmodified OCI image
reference**, pauses to a memory+disk checkpoint and resumes from it, driven by a
**first-party, versioned Go SDK** we can depend on directly.

That combination is unique among the managed backends: Lambda gives
memory-preserving suspend but demands a bespoke image build; Fly gives a clean
API but no OCI image; DigitalOcean gives both the OCI image *and* the
memory-preserving pause, with `github.com/digitalocean/godo` instead of a
hand-written client.

It also has the sharpest gaps of any backend we have considered. The API has **no
command override, no file-injection API, no exec API, no wait/exit-code
primitive, and no signal delivery** — five of the things `backend.Backend` needs.
Those are enumerated in [What the product cannot do](#what-the-product-cannot-do)
and they shape the whole design: everything the platform will not do for us has
to be done by an in-sandbox shim reached over the microVM's HTTP endpoint.

This proposal adds a `do-microvm` backend implementing `backend.Backend`.

## Background: what the product actually is, and what was verifiable

Research date: **2026-09-19**. Sources are listed at the bottom of this section.
Anything not verifiable from a first-party source is in
[Open Questions](#open-questions), not asserted here.

### Naming

Three names appear in first-party material and they do not obviously resolve to
one product:

- **"MicroVM Droplets"** — announced at Deploy 2026 in **Private Preview**:
  "Firecracker-based instances that start in roughly 200 milliseconds, ideal for
  agent sandboxes and lightweight, spiky workloads" (DigitalOcean blog,
  *Powering the Inference Era*).
- **"Managed Sandboxes"** — announced in the *same* post as **GA**:
  "E2B-compatible, Firecracker-based, sub-second cold start for safe execution of
  model-generated code."
- **"MicroVMs" / "MicroDroplets"** — the concrete, documented API surface:
  `github.com/digitalocean/godo` ships a `MicroVMsService` over `/v2/microvms`,
  and the Terraform provider ships `digitalocean_microdroplet`,
  `digitalocean_microdroplet_image` and `digitalocean_microdroplet_checkpoints`.
  godo's own `URN()` keeps the collection name `MicroDroplet` "on purpose",
  confirming the two names are the same resource.

I could **not** find any first-party API reference, SDK, or docs page for an
E2B-compatible "Managed Sandboxes" product. The only agent-sandbox surface with a
published contract is `/v2/microvms`, so that is what this proposal designs
against. Whether "Managed Sandboxes" is a separate, higher-level product with its
own (E2B-shaped, exec-and-filesystem-bearing) API is
[Open Question 1](#open-questions) — and if it is, it would remove several of the
constraints below.

### Preview status, and what that implies

Two independently checkable facts say this is pre-GA and moving:

1. **The published OpenAPI spec does not contain it.** DigitalOcean's public v2
   spec (`DigitalOcean-public.v2.yaml`, the artifact the API reference renders)
   has **zero** occurrences of `microvm`, `microdroplet` or `sandbox`, while
   containing recently-added surfaces such as `/v2/gen-ai/simulation_runs` — so
   the spec is current, and microVMs are simply not in it.
2. **The docs sitemap has no product section for it.** `docs.digitalocean.com`'s
   sitemap lists exactly seven microdroplet pages, all under
   `/reference/terraform/`. There is no `/products/...` page, no how-to, and no
   API-reference tag page (godo's source comment points at
   `#tag/MicroVMs`, which the spec above does not define).

godo's changelog dates the surface precisely: initial support **2026-07-22**
(v1.200.0, "MDROP-23: add godo support for microdroplets"), reshaped onto the
"api-v2 contract" **2026-09-15** (v1.207.0, MDROP-336), create-options added
**2026-09-18** (v1.210.0) — the day before this research. The contract is real,
first-party, and actively churning.

### The API surface

From `godo/microvms.go` at v1.210.0 — the endpoints, verbatim from the source:

| Verb | Path |
|---|---|
| list / create | `GET`, `POST /v2/microvms` |
| get | `GET /v2/microvms/{id}` |
| pause / resume | `POST /v2/microvms/{id}/pause`, `.../resume` |
| delete | `DELETE /v2/microvms/{id}` |
| checkpoints | `GET /v2/microvms/checkpoints`, `POST /v2/microvms/{id}/checkpoints`, `GET`/`DELETE /v2/microvms/checkpoints/{id}` |
| create options | `GET /v2/microvms/options` |

The create request is the whole contract for what a sandbox *is*:

```go
type MicroVMCreateRequest struct {
	Name         string
	Region       string
	Size         *MicroVMSizeRequest // CPU, Memory (disk is provisioned with the size)
	Source       *MicroVMSource      // exactly one of OCIRef or CheckpointID
	Networking   MicroVMNetworking   // "public" | "vpc"
	VPCUUID      string
	AutoPause    *AutoPauseConfig    // Enabled + IdleTimeout (Go duration string)
	AutoResume   *bool               // resume when the endpoint receives a request
	HTTPPort     uint32
	HTTPProtocol MicroVMHTTPProtocol // "http" | "http2"
	Ports        []uint32
	Environment  map[string]string
	Tags         []string
}
```

Five properties drive the design.

1. **It boots an OCI image reference directly.** `Source.OCIRef` takes a ref like
   `docker.io/library/nginx:latest` or a DOCR ref. There is *no* build step, *no*
   staging bucket, *no* snapshot ARN — the decisive advantage over Lambda
   MicroVMs and AgentCore, and over Fly Sprites (which cannot boot an OCI image
   at all).

2. **Pause captures memory *and* disk; resume restores it.** Checkpoints are
   "created automatically when a MicroDroplet pauses", "preserv[ing] the memory
   and disk state needed to resume", and each carries `memory_bytes` and
   `disk_bytes`. This is Lambda's suspend/resume semantics — strictly better than
   Docker's and Fly's filesystem-only preservation — and it maps onto the
   runner's existing `Start → Probe StateExited → Launch(reuse)` path unchanged.

3. **The only way into the guest is an HTTP endpoint.** A microVM exposes
   `http_port`/`ports` and reports `urls` (`hostname`, `port`, `default`,
   `status`); Terraform exports a single `endpoint` described as "Public endpoint
   URL". There is no exec API and no filesystem API.

4. **The state machine has no "exited".** States are `creating`, `running`,
   `pausing`, `paused`, `resuming`, `terminating`, `terminated`, `failed`.
   Nothing in the API reports the guest's process exit status, and nothing
   suggests the microVM stops when its process exits.

5. **There are hard account limits, exposed.** `GET /v2/microvms/options` returns
   `MicroVMAccountLimits{MaxConcurrentRunning, MaxTotalCount, MaxMemoryBytes,
   MaxDiskBytes, MaxIdleTimeoutSeconds}` plus per-size `Pricing{PricePerHour,
   PricePerMonth}` and the regions each size is available in — for the calling
   team specifically. A backend can read its own ceiling at startup.

Sources used, all fetched 2026-09-19:

- `https://raw.githubusercontent.com/digitalocean/godo/main/microvms.go` (v1.210.0)
- `https://raw.githubusercontent.com/digitalocean/godo/main/CHANGELOG.md`
- `https://docs.digitalocean.com/reference/terraform/reference/resources/microdroplet/`
- `https://docs.digitalocean.com/reference/terraform/reference/resources/microdroplet_image/`
- `https://docs.digitalocean.com/reference/terraform/reference/data-sources/microdroplet/`
- `https://docs.digitalocean.com/reference/terraform/reference/data-sources/microdroplet_checkpoints/`
- `https://www.digitalocean.com/blog/powering-the-inference-era`
- `https://investors.digitalocean.com/news/news-details/2026/DigitalOcean-Unveils-AI-Native-Cloud-Built-for-the-Inference-Era/default.aspx`
- `https://api-engineering.nyc3.cdn.digitaloceanspaces.com/spec-ci/DigitalOcean-public.v2.yaml` (public v2 OpenAPI spec)
- `https://docs.digitalocean.com/sitemap.xml`

## What the product cannot do

Stated plainly, because these are what the design has to work around. Each is an
absence in the published contract above, not a claim that DigitalOcean will never
add it.

| `backend.Backend` needs | DigitalOcean MicroVMs | Consequence |
|---|---|---|
| Run `Spec.Cmd` | **No command/args/entrypoint field** on create | The image's own default process must be our supervisor |
| Provision `Spec.Files` + the driver binary | **No filesystem API** | Files must be pushed in-band, over the guest's own HTTP surface |
| Probe/inspect inside the guest | **No exec API** | Cannot even run `uname -m` to pick the arch-matching prebuilt driver |
| `Wait` → exit code | **No "exited" state, no wait, no exit status** | Exit must be reported *by* the guest |
| `Signal` → SIGTERM | **No signal API** | Graceful stop must be an in-guest request |
| Per-run env (fresh task JWT) | `Environment` is **create-time only**; a resumed VM keeps the original | The task token cannot ride in `environment` |
| Reverse-index sandboxes | Tags are "not exposed via the shared `/v2/tags` API" and are not returned on read | No tag-based discovery; only `ListByName` |
| Attach storage | **No volumes**; disk is fixed by the size | Persistence is checkpoint-only |
| Authenticated ingress | Endpoint documented only as "Public endpoint URL"; no platform auth documented | The guest surface must authenticate itself |

Four of the first five are the *same* gaps Lambda MicroVMs has, and gritz already
solved them there: the in-sandbox shim (`internal/microvmshim`) is the image's
entrypoint, fetches a bundle, provisions files once, spawns and supervises the
driver, streams `driver-exited{code}` over SSE, and accepts `POST /gritz/stop`.
This backend reuses that machinery. The two genuinely new problems are
**create-time-only environment** (solved by delivering the per-run spec over the
control surface rather than in `environment`) and **an unauthenticated public
endpoint** (solved by a per-task bearer token, with VPC networking as the
stronger option).

## Design

### Overview

A new package `internal/runner/backend/domicrovm` implements `backend.Backend`
against `github.com/digitalocean/godo`'s `MicroVMsService`. Selection follows the
existing seam in `internal/command/runner.go`:

```
DIGITALOCEAN_ACCESS_TOKEN=... gritz runner --backend do-microvm
```

Per task the backend maps the task to **one microVM** named `gritz-<task-id>`:

1. **Create** — `POST /v2/microvms` with `source.oci_ref` = the workspace image,
   `http_port` = the shim's control port, `environment` carrying **only**
   bootstrap values (the shim's bearer token), `auto_pause.enabled = false`.
2. **Wait for `running`** and for the default URL's status to become `ACTIVE`.
3. **Run** — `POST https://<endpoint>/gritz/run` with the bundle (`Spec.Cmd`,
   `Spec.Env`, `Spec.Files`, working dir, user). The shim provisions the files
   once (marker-gated) and spawns the driver.
4. **Wait** — consume `GET /gritz/lifecycle` (SSE) until `driver-exited{code}`,
   then **pause** the microVM. Return the code.

The driver connects out to the server with its task token exactly as under
Docker. The orchestrator (`runner.Runner`), the driver, the server API, the
database and the task state machine are **untouched**.

### Lifecycle

| | Docker | Lambda MicroVM | **DO MicroVM** |
|---|---|---|---|
| driver exits | container exits (state kept, no cost) | `suspend-microvm` | shim publishes `driver-exited{code}`; runner **pauses** the VM — memory **and** disk checkpointed |
| next run / restart | restart the exited container | `resume-microvm` | `POST /{id}/resume`, then `POST /gritz/run` with a **fresh** bundle |
| task archived/deleted | remove the container | `terminate-microvm` | `DELETE /v2/microvms/{id}` |
| graceful stop | SIGTERM → 30s → SIGKILL | `POST /gritz/stop` over the AWS proxy | `POST /gritz/stop` over the VM endpoint |

Pause is the runner's, never the guest's — the same stance the Lambda backend
takes, and for the same reason: lifecycle authority must not live inside an
untrusted sandbox. The guest holds **no** DigitalOcean credential. The platform's
own `auto_pause` is therefore **disabled** by default (see
[Trade-offs](#trade-offs)).

The payoff is the resume path: an event-driven task subscribed to a PR completes,
its microVM pauses to a checkpoint, and the next routed event resumes it with the
working tree, the dependency caches, *and* process memory intact — then re-runs
the driver with a freshly minted token. No re-clone, no re-pull, no re-setup.

### The image contract

Because there is no command override, the workspace's OCI image must start the
shim as its **default process**, and must contain the driver binary (there is no
exec API to detect the architecture with, and no filesystem API to push tens of
megabytes through).

The image should declare the shim as **`CMD`, not `ENTRYPOINT`**:

```dockerfile
FROM ghcr.io/icholy/gritz-workspace-debian:latest
COPY gritz /usr/local/bin/gritz
CMD ["/usr/local/bin/gritz", "tool", "microvm-shim", "--addr", ":8080"]
```

`CMD` (rather than `ENTRYPOINT`) keeps **one image usable by both backends**: the
Docker backend passes `Cmd: spec.Cmd` to `ContainerCreate`, which overrides `CMD`
and runs the driver directly, while the DO backend lets the image's default
process — the shim — run. The same `image:` value therefore works under
`--backend docker` and `--backend do-microvm`. (That DigitalOcean honours the
image's `CMD` when no entrypoint is configured is the natural reading of
"OCI reference", but it is not documented —
[Open Question 4](#open-questions).)

This is a real cost relative to Docker, but a much smaller one than Lambda's:
it is an ordinary `docker build` + `docker push` to any public registry or DOCR,
not a zip → S3 → `create-microvm-image` pipeline with a build IAM role.

### Delivering the per-run spec

`Environment` is fixed at create time, and `Spec.Env` contains a **freshly minted
task JWT** (`runner.spec()` calls `CreateTaskToken` on every launch) plus the
workspace's secret values. Baking those into `environment` would mean a resumed
microVM re-running the driver with a long-dead token, and would persist secrets
in a control-plane object of unknown readability
([Open Question 6](#open-questions)).

So `environment` carries only what must exist before the runner can talk to the
guest:

| Variable | Purpose |
|---|---|
| `GRITZ_SHIM_TOKEN` | per-task bearer the shim requires on `/gritz/*` |
| `GRITZ_SHIM_ADDR` | control port (matches `http_port`) |

Everything else — `Cmd`, `Env` (token + secrets), `Files` — is POSTed to
`/gritz/run` at each launch, over TLS, authenticated by that bearer. This is
strictly better than Lambda's arrangement, where a 16 KB hook payload forced the
bundle through an S3 staging bucket and a presigned URL: here the runner has a
direct HTTP path to the guest, so **no object storage is involved at all**.

### Reaching the guest

The microVM's endpoint is a public HTTPS hostname with no documented platform
authentication (unlike Lambda's managed auth-token proxy). Two layers:

- **Always:** the shim requires `Authorization: Bearer $GRITZ_SHIM_TOKEN` on
  every `/gritz/*` request. The runner generates 32 random bytes per microVM at
  create time.
- **Recommended for production:** `networking: vpc` with the runner inside the
  same VPC, so the surface is not internet-reachable at all. Whether a
  `vpc`-mode microVM's `endpoint` is VPC-internal is
  [Open Question 5](#open-questions); until confirmed, the bearer token is the
  load-bearing control and `public` is the default.

The bearer is stored in `Handle.Data`, i.e. in the `taskstate` store. That is a
persisted secret — acknowledged in [Trade-offs](#trade-offs) — scoped to one
task's sandbox, and removed with the record on `Destroy`.

### Workspace config

Following the backend-interface pattern (Docker reads `container:`, Lambda reads
`lambda_microvm:`), `workspace.Workspace` gains a `do_microvm:` section:

```yaml
workspaces:
  pets-workshop:
    secrets:
      CLAUDE_CODE_OAUTH_TOKEN: ${env:CLAUDE_CODE_OAUTH_TOKEN}
    container:
      image: ghcr.io/icholy/gritz-workspace-debian:latest
      working_dir: /root
    do_microvm:
      # image: defaults to container.image — both are plain OCI refs
      region: nyc3
      cpu: 2
      memory_mb: 4096
      networking: vpc
      vpc_uuid: 1a2b3c4d-...
      environment:
        SOME_TUNABLE: value
    commands:
      - git clone https://github.com/github-samples/pets-workshop
    agent:
      type: claude
      cwd: /root/pets-workshop
```

```go
// DOMicroVM holds the DigitalOcean MicroVMs backend's runtime config. The API
// token is not configured here; it resolves from DIGITALOCEAN_ACCESS_TOKEN on
// the runner, so workspaces.yaml stays expansion-free and shareable.
type DOMicroVM struct {
	// Image is the OCI reference the microVM boots (source.oci_ref). Empty
	// falls back to Container.Image: unlike Lambda (which needs a purpose-built
	// snapshot ARN) and Fly (which has no image at all), both fields are plain
	// OCI refs, so one image can serve both backends.
	Image string `yaml:"image"`
	// Region is the placement region slug. Empty uses the runner's default,
	// itself defaulting to the API's default_region from /v2/microvms/options.
	Region string `yaml:"region"`
	// CPU and MemoryMB select the size. Disk is provisioned with the size and
	// is not requestable. Validated against /v2/microvms/options at startup.
	CPU      int `yaml:"cpu"`
	MemoryMB int `yaml:"memory_mb"`
	// Networking is "public" (default) or "vpc". VPCUUID is required for "vpc".
	Networking string `yaml:"networking"`
	VPCUUID    string `yaml:"vpc_uuid"`
	// AutoPauseIdleTimeout opts into the PLATFORM's idle auto-pause as a
	// backstop. Empty (the default) disables it and leaves pause timing to the
	// runner, which pauses on driver exit. See the trade-off on auto-pause.
	AutoPauseIdleTimeout string `yaml:"auto_pause_idle_timeout"`
	// Environment is merged into the create-time environment map. Per-run
	// values (the task JWT, workspace secrets) do NOT travel here — they are
	// delivered in the run bundle, because create-time env is immutable across
	// resume.
	Environment map[string]string `yaml:"environment"`
}
```

A workspace may set `container:` and `do_microvm:` together so one
`workspaces.yaml` serves runners with different backends; `ValidateWorkspace`
checks only its own section, and `RegisterWorkspaces` skips (with a warning)
workspaces this backend cannot run.

### The Handle

```go
// stored opaque in Handle.Data (taskstate.Record.Data), never decoded by the store
type handleData struct {
	Endpoint  string `json:"endpoint"`   // https://<hostname>:<port>, for /gritz/*
	ShimToken string `json:"shim_token"` // per-VM bearer for the control surface
	Name      string `json:"name"`       // gritz-<task-id>, for the ListByName backstop
}
```

`Handle.ID` is the microVM UUID — the index key the runner persists and
reverse-indexes, stable for the sandbox's life and sufficient for every
control-plane call. Because tags are neither queryable through `/v2/tags` nor
returned on read, the only discovery backstop is `ListByName("gritz-<task-id>")`;
the `taskstate` store remains the authority, exactly as the interface intends.

### Backend method mapping

| Method | Implementation |
|---|---|
| `ValidateWorkspace` | Validate `do_microvm:`: an image ref is resolvable (own field or `container.image`), `networking` is `public`/`vpc` with `vpc_uuid` set for `vpc`, `cpu`/`memory_mb` match an available size for the region from the cached `/v2/microvms/options`, `auto_pause_idle_timeout` parses and is ≤ `MaxIdleTimeoutSeconds`. |
| `Launch` (fresh) | `POST /v2/microvms` — `name: gritz-<task-id>`, `source.oci_ref`, size, region, networking, `http_port`, `environment: {GRITZ_SHIM_TOKEN, GRITZ_SHIM_ADDR}`, `auto_pause.enabled=false`, `auto_resume=false`, tags. Poll `GET /{id}` until `running` and the default URL is `ACTIVE`. `POST /gritz/run` with the bundle. Return `Handle{ID: uuid, Data: {endpoint, shim_token, name}}`. |
| `Launch` (reuse) | `GET /v2/microvms/{reuse.ID}`: 404, `terminated` or `failed` → `backend.ErrGone`. `paused` → `POST /{id}/resume`, poll to `running`. Then `POST /gritz/run` with a **fresh** bundle (new task JWT); the shim skips provisioning (marker present on the restored disk) and re-spawns the driver. Never creates a fresh microVM. |
| `Probe` | `GET /v2/microvms/{id}`. 404 / `terminated` / `failed` → `StateGone`; `paused` → `StateExited` (husk preserved: checkpointed memory + disk); `running` → `StateRunning`; `creating`/`pausing`/`resuming`/`terminating` → `StateUnknown` (the interface's documented "ignore transient" contract). Control-plane only — no call into the guest, so `Probe` stays cheap and works on a paused VM. |
| `Signal` | `POST https://<endpoint>/gritz/stop` (bearer). The shim SIGTERMs the driver, waits the grace, then SIGKILLs — the in-guest mirror of the Docker backend's 30s SIGTERM→SIGKILL. Returns `signalled=true` when the shim reports a live driver; the driver then owns its terminal report. A 404/unreachable endpoint on a non-running VM returns `false`. |
| `Destroy` | `DELETE /v2/microvms/{id}`; a 404 is not an error. Reached via the orchestrator's `Prune` on task archive/delete. Automatic checkpoints are the platform's to reap — whether deleting a microVM deletes its checkpoints is [Open Question 7](#open-questions); if not, `Destroy` also sweeps `GET /v2/microvms/checkpoints?microvm_id=` and `DELETE`s each. |
| `Wait` | Open `GET https://<endpoint>/gritz/lifecycle` (SSE, sticky-replayed) and block. On `driver-exited{code}`: `POST /{id}/pause`, return `(code, nil)`. On stream drop: re-connect; arbitrate with `GET /v2/microvms/{id}` as the liveness authority — `running` → reconnect, anything terminal → `(ExitLost, nil)`. On `ctx` cancellation: `(_, ctx.Err())`, leaving the microVM alive for next-boot rehydration. Safe to call on a sandbox this process did not start: the sticky replay hands a reattaching runner the exit event it missed. |
| `Close` | Close the HTTP client and any open SSE streams. MicroVMs outlive the runner exactly as containers do. |

This is deliberately the same shape as `backend/lambdamicrovm` — the shim
contract, the SSE lifecycle stream, the arbitrate-on-drop loop and the
pause-on-exit rule are all carried over. What drops out is the entire AWS-shaped
scaffolding: no S3 stager, no presigned URLs, no IAM roles, no auth-token
minting, no SigV4 client.

### Package layout

```
internal/runner/
├── runner.go                  unchanged orchestrator (owns the taskstate store)
├── taskstate/                 shared store; the runner is the only writer
├── backend/
│   ├── backend.go             unchanged
│   ├── docker/                unchanged
│   ├── lambdamicrovm/         unchanged
│   └── domicrovm/
│       ├── domicrovm.go       the backend; godo behind a mockable `Cloud` iface
│       ├── shimclient.go      /gritz/run, /gritz/stop, /gritz/lifecycle
│       └── README.md          image recipe + operator notes
└── workspace/                 +DOMicroVM config section
internal/command/              runner.go backend switch gains a `do-microvm` case
```

There is **no** `internal/x/domicrovm`. The Lambda backend needed
`internal/x/awsmicrovm` because its control plane is preview with no Go SDK; here
`github.com/digitalocean/godo` is a first-party, semver'd client that already
covers every endpoint, so the backend depends on it directly and hides it behind
a narrow `Cloud` interface for testing — the same `Cloud`/`moq` pattern
`lambdamicrovm` uses.

### CLI

```
gritz runner --backend do-microvm [--do-region nyc3]
```

`--do-region` (env `GRITZ_DO_REGION`) sets the default region, overridable per
workspace via `do_microvm.region`; unset, the backend uses `default_region` from
`/v2/microvms/options`. The API token resolves from `DIGITALOCEAN_ACCESS_TOKEN`
(godo/doctl/Terraform convention) — no token flag, so it never reaches a process
listing. `internal/command/runner.go`'s backend switch gains a `do-microvm` case;
the `--backend` usage string gains the name. `gritz download` is not extended —
there is no host hypervisor to fetch.

### Concurrency and quotas

At startup the backend calls `GET /v2/microvms/options` once and logs the team's
`MaxConcurrentRunning`, `MaxTotalCount`, `MaxMemoryBytes`, `MaxDiskBytes` and
`MaxIdleTimeoutSeconds`, and validates every registered workspace's size against
the available sizes for its region. `MaxTotalCount` counts *paused* microVMs too,
so — exactly like Lambda's memory quota, and unlike a sleeping Fly Sprite — the
idle ceiling scales with the number of non-archived tasks. The backend surfaces a
quota rejection as a `Launch` error (the task fails and is retried) rather than
blocking; wiring it into the orchestrator's `safesem.Semaphore` as real
backpressure is [Open Question 8](#open-questions).

### Testing

- Unit tests (no DigitalOcean account), mirroring `backend/lambdamicrovm`'s:
  `ValidateWorkspace` (image fallback to `container.image`, vpc requires
  `vpc_uuid`, size validated against a stubbed options response); handle
  construction and `handleData` round-trip; `Probe` state mapping for all nine
  microVM states plus 404; the create-request builder (env carries only the
  bootstrap pair, `auto_pause` disabled, `http_port` matches the shim addr);
  `Destroy` idempotency on 404. `godo` sits behind a `Cloud` interface with a
  generated mock.
- `Wait` against an `httptest` server standing in for the shim: clean
  `driver-exited{code}` → `(code, nil)` **and** a pause call issued; stream
  dropped then reconnected → no spurious exit; stream gone with the VM
  `terminated` → `(ExitLost, nil)`; `ctx` cancel → `ctx.Err()` and **no** pause.
- Shim-client tests for bearer propagation and non-2xx handling.
- Integration tests in `backend/domicrovm`, skipped unless
  `DIGITALOCEAN_ACCESS_TOKEN` is set — the same gating as the Docker e2e tests
  (need a daemon) and the Lambda ones (need AWS creds). They cover create → run →
  driver exit → pause, resume + re-run against the preserved checkpoint,
  provision-once across reuse, `Signal`, `Destroy` idempotency, and re-adoption
  after a simulated runner restart.
- The orchestrator needs no new tests: it already runs against `BackendMock`.

### What doesn't change

`runner.go`, the durable event outbox, the proto definitions, the database
schema, the driver, the log shipper and secret redaction, and the task state
machine are untouched. The Docker and Lambda backends are unaffected. `prebuilt`
is **not** used by this backend — the driver ships in the image, since neither
the architecture nor a file channel is available before boot.

## Implementation Plan

1. **Backend-neutral shim run/resume** — Delivers: a backend-agnostic
   `POST /gritz/run` verb on `internal/microvmshim`'s `ControlHandler()` (bundle
   in the request body; provision-once marker; spawn/supervise; re-spawn on a
   second call), bearer-token auth on `/gritz/*`, and `Bundle` moved out of
   `backend/lambdamicrovm` into a neutral package. Depends on: nothing — this is
   the seam that proposals/draft/generic-shim.md ([#1221](https://github.com/icholy/gritz/issues/1221))
   generalises, and landing that proposal instead subsumes this slice.
   Verifiable by: unit tests driving the shim over HTTP with no AWS hook surface
   registered; the existing Lambda hook path still green.
2. **Workspace `do_microvm:` config** — Delivers: the `DOMicroVM` struct on
   `workspace.Workspace`, YAML parsing, field docs, and the `container.image`
   fallback. Depends on: nothing. Verifiable by: config round-trip unit tests; a
   workspace with both `container:` and `do_microvm:` loads under either backend.
3. **godo dependency + `Cloud` seam** — Delivers: `github.com/digitalocean/godo`
   in `go.mod`, the narrow `Cloud` interface (create/get/list-by-name/pause/
   resume/delete/options/checkpoints) with a `moq` mock, and an options-backed
   size/region validator. Depends on: nothing. Verifiable by: unit tests against
   the mock; an account-gated smoke test listing `/v2/microvms/options`.
4. **`do-microvm` backend** — Delivers:
   `internal/runner/backend/domicrovm` implementing all seven `backend.Backend`
   methods over (1) and (3), plus the shim client. Depends on: (1), (2), (3).
   Verifiable by: unit tests with the mocked `Cloud` and an `httptest` shim;
   `Probe` mapping and `Wait` return-shape coverage against the interface
   contract.
5. **Runner wire-up** — Delivers: the `do-microvm` case in
   `internal/command/runner.go`'s backend switch, `--do-region`, and the
   `--backend` usage string. Depends on: (4). Verifiable by:
   `gritz runner --backend do-microvm` starting and registering `do_microvm:`
   workspaces against a real account.
6. **Image recipe + published base** — Delivers:
   `backend/domicrovm/README.md` with the `CMD`-based Dockerfile, and a published
   `ghcr.io/icholy/gritz-workspace-debian-shim` variant (or a `--shim` build arg
   on the existing image) so operators do not hand-roll one. Depends on: (1).
   Verifiable by: the image runs the shim under DO and the driver under Docker
   from the same ref.
7. **Integration tests** — Delivers: the account-gated e2e suite in
   `backend/domicrovm`. Depends on: (5), (6). Verifiable by: the suite passing
   with `DIGITALOCEAN_ACCESS_TOKEN` set; skipped otherwise.
8. **Checkpoint-seeded warm start** *(optional, gated on
   [Open Question 3](#open-questions))* — Delivers: a per-workspace template
   microVM checkpointed after first boot, with `source.checkpoint_id` seeding each
   task's sandbox to skip cold boot. Depends on: (4). Verifiable by: a benchmark
   comparing `oci_ref` cold create against checkpoint-seeded create.

Layers 1–3 are independently mergeable and useful on their own (1 unblocks every
stock-image backend; 2 is inert config; 3 is a dependency and a validator).
Layer 4 is the only large one, and 5–8 are thin.

## Trade-offs

**A shim in the image vs. a platform exec API.** Fly Sprites' exec/fs API means
no in-guest component at all; DigitalOcean's gives us nothing to reach into the
guest with, so the shim is mandatory, and with it an image build step. The
mitigation is that the build is an ordinary two-line Dockerfile pushed to any
registry — not Lambda's zip → S3 → `create-microvm-image` with a build IAM role —
and that the shim already exists and is already the Lambda backend's supervisor.
The residual cost is real: an operator cannot point `do_microvm.image` at an
arbitrary third-party image the way they can point `container.image` at one.

**Baking the driver into the image vs. provisioning it.** Docker tar-copies the
arch-matching driver from `prebuilt` at create time; here there is no file
channel and no way to detect the guest's architecture before boot, so the driver
ships in the image. That couples driver upgrades to image rebuilds across a
fleet. proposals/draft/generic-shim.md's "download the driver from the server when
it is not already present" is the escape hatch and would restore
upgrade-without-rebuild; this proposal does not depend on it, but benefits from
it.

**OCI image + memory-preserving pause — the reason to build this at all.** No
other managed backend gives both. Lambda preserves memory but needs a bespoke
image; Fly boots fast but loses RAM and cannot take an OCI image; Docker loses
RAM and needs host compute. A DO microVM resumes a completed task with its
working tree, its caches, *and* its process memory — from an image the operator
already builds for Docker. For gritz's event-driven, long-tail-of-idle-tasks
workload that is the single most valuable property.

**Disabling platform auto-pause.** `auto_pause` with an `idle_timeout` is
tempting — it is exactly the "cheap when idle" behaviour we want — but what
counts as "idle" is undocumented, and an agent doing CPU-light work (waiting on a
model response, on CI, on a long `git clone`) is precisely the case a
request-driven idle heuristic gets wrong. Losing a microVM mid-task to a platform
pause would be a silently truncated run. So the backend disables it and owns pause
timing, as the Lambda backend owns suspend; `auto_pause_idle_timeout` is left in
the workspace config as an opt-in **backstop** against a runner that dies holding
a running VM. `auto_resume` is likewise off, so a stray probe cannot wake a
paused sandbox and start billing.

**Idle cost: paused, not free.** A paused microVM holds a checkpoint
(`memory_bytes` + `disk_bytes` of stored state) and counts against
`MaxTotalCount`. That is Lambda's problem, not Fly's: the ceiling on
"keep a completed-but-subscribed task's sandbox around indefinitely" scales with
the number of non-archived tasks. Checkpoint storage should be cheaper than
suspended-VM memory quota, but no first-party price for it was findable
([Open Question 9](#open-questions)), so this is a known unknown rather than a
claimed win.

**A persisted bearer token.** Lambda mints a short-lived, port-scoped proxy token
per stream, so nothing long-lived is stored. Here the endpoint has no documented
platform auth, so the backend generates a per-microVM bearer and stores it in
`Handle.Data` — a secret at rest in the taskstate store for the sandbox's life.
The alternatives are worse or unavailable: rotating it would require mutating
create-time `environment` (impossible), and mTLS would need a cert channel into
the guest (also impossible). `networking: vpc` is the real fix and the
recommended production posture; the token is then defence in depth.

**A preview API under an unstable contract.** The surface was reshaped onto an
"api-v2 contract" four days before this was written, and gained new fields the
day before. The API is also absent from the published OpenAPI spec and from the
product docs. The mitigations are that godo is first-party and semver'd (so
breaks surface as compile errors, not runtime 400s), and that the backend hides
it behind `Cloud`. But this should not be the default backend, and the proposal
should not be accepted on the assumption the contract is settled.

**Betting on the wrong product.** If DigitalOcean's "Managed Sandboxes" (the
E2B-compatible, GA offering) is a distinct product with an exec-and-filesystem
API, a backend built on it would need no shim, no image build, and no in-band
bundle — much closer to the Fly Sprites design. That is
[Open Question 1](#open-questions) and it should be resolved with DigitalOcean
*before* layer 4, since it could invalidate layers 1 and 6.

## Comparison with the sibling backends

| | Docker | Firecracker | Lambda MicroVMs | Fly Sprites | **DO MicroVMs** |
|---|---|---|---|---|---|
| Runner-owned compute | yes | yes | no | no | **no** |
| Image | unmodified OCI | unmodified OCI | purpose-built (zip→S3→build) | generic base + `setup` | **OCI ref + shim as `CMD`** |
| Client | docker SDK | local | hand-rolled SigV4 (`x/awsmicrovm`) | REST + young SDK | **first-party godo** |
| In-guest shim | no | no | yes | no | **yes** |
| Object storage needed | no | no | **yes** (S3 bundle) | no | **no** |
| On driver exit | container exits (state kept) | VM stays up | `suspend-microvm` | auto-sleep | **`pause` → checkpoint** |
| Memory preserved while idle | no | yes (VM up) | yes | no | **yes** |
| Idle cost | ~0 | full VM | snapshot storage + **memory quota** | ≈ 0, no standing quota | **checkpoint storage + `MaxTotalCount`** |
| Reuse on next run | restart container | restart VM | `resume-microvm` | wake + re-exec | **`resume` + `/gritz/run`** |
| Exit notification | `docker wait` | poll | SSE `driver-exited{code}` via AWS proxy | exec returns code | **SSE `driver-exited{code}` via VM endpoint** |
| Graceful stop | SIGTERM→SIGKILL | SIGTERM | `POST /gritz/stop` (proxy) | SIGTERM to session | **`POST /gritz/stop` (endpoint)** |
| File injection | tar copy | config disk | S3 bundle + presigned URL | fs API | **in-band bundle over HTTP** |
| Guest auth | n/a | n/a | managed proxy token | control API | **self-issued bearer (or VPC)** |
| Per-run env refresh | yes | yes | yes (bundle refetch) | yes (new exec) | **yes (run bundle)** |

DO MicroVMs is the only managed backend that boots an **unmodified OCI image
reference** *and* preserves **memory** across idle, with a **first-party Go SDK**
and **no object storage**. It pays for that with a mandatory in-guest shim, a
per-workspace image build, a self-managed guest credential, and a contract that
is still moving.

## Open Questions

1. **Is "Managed Sandboxes" the same product as MicroVMs?** DigitalOcean
   announced an E2B-compatible, GA "Managed Sandboxes" and a separate
   private-preview "MicroVM Droplets" in the same post. Only the latter has a
   published contract. If the former exists as a distinct API with E2B's
   exec/filesystem surface, it would remove the shim, the image build, and the
   bearer token from this design. **This should be resolved before layer 4.**
2. **What happens when the guest's main process exits?** Nothing in the state
   machine (`creating`/`running`/`pausing`/`paused`/`resuming`/`terminating`/
   `terminated`/`failed`) corresponds to "exited", and nothing suggests the
   microVM stops billing when its process does. The design assumes the microVM
   stays `running` — which is why the shim must not exit and why the runner
   pauses explicitly. If the platform instead reaps or `failed`s the VM on
   process exit, `Wait`'s arbitration and the `failed → StateGone` mapping both
   need revisiting.
3. **Can a microVM be created from *another* microVM's checkpoint?**
   `MicroVMSource` accepts `checkpoint_id`, which reads as a general
   create-from-checkpoint primitive and would enable per-workspace warm templates
   (layer 8). But the Terraform docs describe checkpoints as automatic, read-only
   artifacts that "cannot be created or deleted through the customer API", while
   godo exposes `CreateCheckpoint` and `DeleteCheckpoint`. These two first-party
   sources disagree; which is current is unknown.
4. **Does the platform honour the image's `CMD`?** The whole "one image, both
   backends" story rests on the microVM running the image's default process and
   on `CMD` (overridable by Docker) being enough. Neither is documented.
5. **What is the endpoint's reachability and auth?** Terraform calls `endpoint` a
   "Public endpoint URL". Is a `networking: vpc` microVM's endpoint
   VPC-internal? Is there any platform-level authentication, rate limit, or
   TLS-client-cert option? Is the hostname stable across pause/resume — the
   design assumes it is, and caches it in `Handle.Data`.
6. **Is create-time `environment` readable back?** The Terraform data source
   exports `environment`, implying the API returns it; godo's `MicroVM` struct
   does not carry it. If it is readable, anything in it is visible to any holder
   of the team's API token — which is why the design keeps secrets out of it, but
   the answer determines whether even `GRITZ_SHIM_TOKEN` belongs there.
7. **Does `DELETE /v2/microvms/{id}` reap its checkpoints?** If not, `Destroy`
   must sweep them or every archived task leaks checkpoint storage forever.
8. **Quotas as backpressure.** Should `MaxConcurrentRunning` / `MaxTotalCount`
   feed the orchestrator's `safesem.Semaphore` so the runner stops pulling tasks
   it cannot place, rather than failing them at `Launch`? This is a
   backend-interface question (no backend advertises capacity today) and affects
   Lambda equally.
9. **Pricing and preview access.** `/v2/microvms/options` returns per-size
   `price_per_hour`/`price_per_month`, but no public price list, no checkpoint
   storage price, and no preview-enrolment path were findable. Access to the
   private preview is a prerequisite for layers 3, 5 and 7.
10. **Architecture.** `MicroVMSizeRequest` is CPU + memory only, with no arch
    field, and there is no exec API to probe with. Are microVMs x86-64 only,
    arm64 only, or is the arch inferred from the OCI ref's manifest? This decides
    whether the shim image needs to be multi-arch and whether `prebuilt`'s arch
    selection has any role.
11. **Private registry authentication.** `source.oci_ref` accepts DOCR refs and
    `docker.io` refs, but the create request has no credential field. Can a
    private ghcr.io image be used, and if so how are pull credentials supplied?
    gritz's published workspace images are public, so this is not blocking, but
    it constrains operators with private images.
12. **Is the separate image-import step still required?** The Terraform provider
    models `digitalocean_microdroplet_image` (import an OCI ref, get a UUID/URN,
    pass it as `image`), while godo's post-api-v2 create takes `source.oci_ref`
    inline with no image resource at all. If the import step is still mandatory
    server-side, `Launch` needs an import-and-wait phase (and a cache) before
    create.
