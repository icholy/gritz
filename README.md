# GRITZ

Runs coding agents (Claude Code, Codex, Cursor, GitHub Copilot) inside remote sandboxes.

## Features

- **Self-hosted runners** - Run agents on your own infrastructure inside Docker containers
- **Third-party integrations** - Interact with agents via GitHub and Jira events
- **MCP server** - Create and manage tasks from Claude Code, Cursor, or any MCP client

## Web UI

![Web UI](images/webui.png)

## Quick Start

Install `gritz` cli:

```bash
mise run install
```

Download the pre-built binaries (if needed):

```bash
gritz download
```

Create an API key in the Web UI (https://gritz.dev/ui/keys/new) and copy the returned `xat_…` value into `~/.config/gritz/config.yaml`:

```yaml
token: xat_...
```

Create a `workspaces.yaml` file (see examples below):

```bash
vim ~/.config/gritz/workspaces.yaml
```

Start the local runner:

```bash
gritz runner
```

Create and monitor tasks via the Web UI.

Open: https://gritz.dev/

## Workspace Examples

See [examples/workspaces/](examples/workspaces/) for workspace configuration examples:

- [claude.yml](examples/workspaces/claude.yml) - Claude Code
- [codex.yml](examples/workspaces/codex.yml) - OpenAI Codex
- [cursor.yml](examples/workspaces/cursor.yml) - Cursor Agent
- [copilot.yml](examples/workspaces/copilot.yml) - GitHub Copilot
- [mcp-server.yml](examples/workspaces/mcp-server.yml) - MCP server configuration
- [private-repo.yml](examples/workspaces/private-repo.yml) - Cloning private repositories
- [dummy.yml](examples/workspaces/dummy.yml) - Dummy agent for testing

## Docker Compose Runner

See [examples/runner/](examples/runner/) for running the runner as a Docker Compose service with a pull-through registry cache.

## Debugging

View container logs:

```bash
gritz logs -f <taskid>
```

Set `verbose: true` on a workspace's agent to bypass the CLI output parser and
log every raw line. Useful when the parser is hiding details (errors,
intermediate output, tool-use payloads) you want to inspect via `gritz logs`.

```yaml
agent:
  type: claude
  verbose: true
```

Get a shell to a task container:

```bash
gritz shell <taskid>
```

List task containers:

```bash
gritz containers
```

## Local Development

```bash
# Start server and postgres locally
docker compose up -d

# Run the FE
cd webui
pnpm install
pnpm run dev
```

The local server runs with `--no-auth`, but the runner still requires an API key.
Create one in the local Web UI at http://localhost:5173/ui/keys/new, then start the runner:

```bash
gritz runner --server http://localhost:6464 -key <api-key>
```


### Build

```bash
mise run build      # Build main + prebuilt binaries (linux amd64/arm64)
mise run generate   # Generate protobuf code
go build            # Build main binary only
```

## Events

Agents can attach **links** to their tasks for external resources they create (PRs, Jira issues, etc.). Links created with `subscribe=true` act as subscriptions. When a new event occurs on the resource, the agent is automatically restarted to respond.

![Events](images/events.svg)

## Architecture

![Architecture](images/architecture.svg)


## Schema

![Schema](images/schema.svg)
