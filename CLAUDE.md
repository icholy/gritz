# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build Commands

```bash
mise run build          # Build main binary + prebuilt binaries for linux amd64/arm64
mise run generate       # Generate protobuf code (go tool buf generate)
go build -o gritz ./cmd/gritz  # Build main binary only
```

## Running Tests

Tests require a running PostgreSQL instance. Start it with:

```bash
mise run build    # The prebuild binaries & webui must exist for the tests to pass
mise run up:test  # Start test dependencies
mise run test     # Run all tests
```

Pass extra flags to `go test` with `--`: `mise run test -- -run=TestFoo -v`

## Architecture

GRITZ is an async agent orchestrator using a central control plane architecture to run multiple Claude Code instances in parallel inside Docker containers.

### Core Components

- **Server** (`internal/server/`) - Connect RPC API + Web UI, stores tasks and logs in PostgreSQL
- **Runner** (`internal/runner/`) - Polls for pending tasks, manages Docker container lifecycle, creates Unix socket proxy for container-to-server communication
- **Agent** (`internal/agent/`) - Runs inside containers, executes Claude Code CLI (`claude --print`)
- **Store** (`internal/store/`) - PostgreSQL persistence layer using pgx driver

### Key Concepts

- **Tasks** are the unit of work - contain workspace reference and prompts to execute
- **Agents** run one-to-one with tasks inside containers named `gritz-{task-id}`
- **Workspaces** define container config (image, volumes, env vars) and MCP servers in `workspaces.yaml`
- Communication happens via Unix socket proxy at `/gritz/socket` inside containers
- Runner auto-injects an `gritz` MCP server (see below)

### MCP Server Tools

The runner injects an `gritz` MCP server into each agent, providing these tools:

- `get_my_task` - Get current task instructions, links, and events
- `update_my_task` - Update the current task's name
- `create_link` - Associate external resources (PRs, Jira tickets) with the task
- `report` - Log messages visible in the Web UI

### Event System

Tasks can be notified about external events through the event system:

- **Events** represent external triggers (GitHub PR comments, Jira issue updates, etc.)
- **Links** created with `subscribe=true` route events to tasks when the event URL matches the link URL
- When an event is processed, all tasks with matching subscribed links receive the event
- Events appear in `get_my_task` output and provide additional context to agents
- External pollers (GitHub, Jira) create events and process them to notify linked tasks

Use `create_link` with `subscribe=true` for resources that may need follow-up (PRs awaiting review, issues awaiting response, etc.)

### CLI Subcommands

```
gritz server     # Start server
gritz runner     # Start container orchestrator
gritz driver     # Run agent (inside container, started by runner)
gritz mcp        # Local user-facing stdio MCP server that proxies to the server
gritz notify     # Subscribe to server notifications and emit system notifications
gritz tool agent-mcp # In-container MCP server providing gritz tools to the agent
gritz task       # Task CRUD (list, create, update, delete)
gritz containers # List gritz containers
gritz jira       # Poll Jira for issue comments
gritz github     # GitHub integration
```

### Protobuf

Service definitions in `proto/gritz/v1/gritz.proto`; generated code is written to `internal/proto/` (checked in) by `mise run generate`.

## Conventional Commits & Releases

Commit messages and PR titles must follow the [Conventional Commits](https://www.conventionalcommits.org/) spec. The format is enforced by `conform` (see `.conform.yaml`):

```
<type>(<optional scope>): <subject>
```

Allowed types: `feat`, `fix`, `chore`, `docs`, `refactor`, `test`, `ci`, `perf`, `build`, `revert`, `proposal`. Breaking changes use `!` after the type (e.g. `feat!: ...`) or a `BREAKING CHANGE:` footer.

Validation runs in CI on every PR via `siderolabs/conform`. To validate locally on each commit, install the opt-in hook once: `mise run install:hooks`.

Releases are driven by [release-please](https://github.com/googleapis/release-please) — `.github/workflows/release-please.yml` continuously opens a "Release PR" against master based on the conventional commits since the last release. Merging that PR creates the version tag, which triggers `release.yml` to build binaries, publish images to GHCR, and deploy to Fly.

- `feat`, `fix`, `perf`, `revert` → visible in the generated CHANGELOG.md
- All other types → hidden from the changelog
- `feat:` → minor bump; `fix:` → patch bump; `feat!:` or `BREAKING CHANGE:` → major bump

## Web UI

The web interface is a React-based UI in `webui/` using TanStack Router, TanStack Query, and shadcn/ui components.

**Always run `pnpm lint` in `webui/` before finishing any frontend change** — CI runs ESLint on every webui PR and will fail otherwise.

For detailed development guidelines, see the `webui` skill in `.claude/skills/webui/`.
