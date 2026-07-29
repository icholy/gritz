---
name: gritz-task
description: Create gritz tasks using the MCP tools. Use when the user wants to create a task for the gritz system.
---

# Creating gritz Tasks

Use the `mcp__gritz__create_task` and `mcp__gritz__list_workspaces` MCP tools to create tasks.

## Workflow

1. If the user hasn't specified a workspace, use `mcp__gritz__list_workspaces` to find the right one.
2. Create the task with `mcp__gritz__create_task`, always including the `runner` parameter.
3. Show the user the task URL from the response.

## Guidelines

- **Always set `runner`**: Use the runner_id from `list_workspaces` (don't omit it).
- **Name**: Keep task names short and descriptive (under 60 chars).
- **Instruction**: Write clear, actionable instructions. Include context you **already have** from the current conversation (code you've read, file paths discussed, architecture context). Do **not** go researching or exploring the codebase to gather more context before creating the task — the agent can do that itself. The whole point of delegating to gritz is to offload work, not to front-load it.

## Task Types

- **Investigation tasks**: When the task is investigative (research, analysis, auditing), the deliverable should be a GitHub issue.
- **Implementation tasks**: When the task involves writing code, the deliverable is a PR.

## Container Environment

The agent runs inside a Docker container with a limited toolset. The available tools are defined in `mise.toml` at the repo root. Do **not** assume any CLI tools are available beyond what mise installs and standard Linux utilities.
