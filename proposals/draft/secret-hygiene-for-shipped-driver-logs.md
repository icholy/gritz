# Secret hygiene for shipped driver logs

Issue: https://github.com/icholy/gritz/issues/1563

## Problem

`proposals/implemented/ship-driver-logs-to-server.md` mirrors every byte of the
driver's log sink to the server as `log_chunks`, readable via `gritz logs` and
the Web UI's Logs tab. That proposal explicitly deferred secret hygiene out of
v1: the shipper mirrors the sink verbatim, by design.

The deferral changed a trust boundary. On the sandbox's disk the log was
readable only by someone who could already `gritz shell` in — the same boundary
as running the agent. Shipped, it is persisted in Postgres and readable by
anyone with `OpTaskRead` in the org, indefinitely (there is no retention; see
below). The audience for the log is no longer the audience for the sandbox.

A survey of what actually flows through the sink shows this is not
hypothetical. Credential-shaped material enters the shipped transcript through
several distinct doors:

- **The task JWT, structurally, today.** The runner injects its minted task
  token into the agent MCP config as a `--token` argument
  (`internal/runner/runner.go:479-491`). The Copilot agent then logs that
  entire MCP config JSON into the sink on every run
  (`internal/agent/copilot.go:44`), and the dummy agent logs each MCP server's
  argv (`internal/agent/dummy.go:122`). Task tokens have **no expiry** —
  revocation is the `task.archived` scope predicate
  (`internal/auth/apiauth/jwt.go:52-60`) — so a transcript containing one holds
  a live, task-scoped write credential until the task is archived.
- **Setup commands, verbatim.** The driver logs the full setup-command list at
  startup (`d.Log.Info("loaded config", …, "commands", cfg.Commands, …)`,
  `internal/agent/driver.go:176-182`) and each command again as it runs
  (`driver.go:286`). The documented pattern for private repos puts a live token
  inside that string (`examples/workspaces/private-repo.yml`:
  `git clone https://x-access-token:${sh:gh auth token}@github.com/…` — the
  `${sh:}` expansion has already happened by the time the driver sees it).
- **Setup command output.** Setup stdout/stderr are teed into the sink
  (`driver.go:288-292`). Arbitrary shell: a failed authenticated clone echoes
  the credentialed URL back, `set -x` or `env`-style steps dump the whole
  environment — which carries the workspace `environment:` map
  (`CLAUDE_CODE_OAUTH_TOKEN`, `CODEX_API_KEY`, `NPM_TOKEN`, …), `GRITZ_TOKEN`,
  and whatever the image bakes in.
- **The agent's own activity.** Tool-call summaries flow through
  `toollog.Summarize` into the sink. A `Bash` call's `command` field is
  rendered up to 120 runes — plenty for
  `curl -H "Authorization: Bearer …"` or an `export KEY=…`. Claude logs a
  failed tool result's content unbounded (`internal/agent/claude.go:150`), and
  Claude's stderr is teed raw (`claude.go:69`), where MCP startup failures echo
  the gritz server's spawn line — including `--token`. The `verbose` workspace
  option logs every raw stream-JSON line with no truncation at all
  (`claude.go:94-97`, `codex.go:102-105`, `cursor.go:85-88`).
- **Runtime-minted credentials.** `gritz git-credential` and the
  `get_github_token` MCP tool hand the agent GitHub App installation tokens
  (`internal/gitcredential/gitcredential.go:43-46`,
  `internal/agentmcp/xmcp.go:57-63`) precisely so it can use them in shell
  commands — which are then summarized into the log. The driver never sees
  these values, so no amount of "mask what you were given" covers them.

Nothing filters any of this. The only `Redact` in the tree
(`internal/agent/toollog`) is a bulky-field truncator keyed on field names
(`old_string`, `content`, …) — prior art for *where* a hook can live, not a
detection mechanism.

Two facts bound the blast radius and shape the fix. First, the driver process
*knows* most of the secrets that matter locally: its own token arrives as
`--token`/`GRITZ_TOKEN`, and the workspace `environment:` map is in its
process environment — so the highest-value masking is deterministic string
replacement, not detection. Second, any org member can already `gritz shell`
into any live task's sandbox and read `/gritz/log` raw (org members hold the
wildcard admin scope, `internal/auth/apiauth/jwt.go:42`), so the *new* exposure
from shipping is specifically the at-rest, outlives-the-sandbox, no-effort
copy — which is what this proposal scrubs.

## Design

**Filter at the tee, inside the driver, on the shipped branch only.** A new
`internal/redact` package provides a rule-driven, line-buffering
`io.Writer` filter; `agent.OpenDriverLog` splices it between the sink and the
shipper. The in-sandbox `/gritz/log` file and the process stderr (docker logs)
stay raw — they are inside the original trust boundary and keep full fidelity
for shell-based post-mortems. Detection is two-tier: **known-value masking**
seeded from what the driver was given (the task token, sensitively-named
environment values), plus a **small curated set of high-precision patterns**
for credential formats that are minted at runtime and therefore unknowable in
advance (GitHub installation tokens, JWTs, bearer headers, credentialed URLs,
PEM blocks). Alongside the filter, the two structural leaks (Copilot's MCP
config dump, dummy's argv line) are simply fixed at the source, and a small
per-task purge tool handles logs already shipped.

### Why the tee, not ingest or read time

- **Only the driver knows the local secrets.** Known-value masking is exact:
  no false positives, no pattern to get wrong. The server never has the
  sandbox's environment, so ingest- or read-time filtering is limited to
  patterns — strictly weaker on exactly the values (workspace env, task token)
  that dominate the real exposure.
- **Chunk boundaries.** The shipper cuts chunks at 32 KiB offsets with no
  regard for lines; a secret can straddle two chunks (and duplicated chunks
  from ambiguous retries can overlap). A server-side scanner would need
  cross-chunk, cross-retry reassembly state per task. Filtering before the
  shipper sees the bytes makes chunking irrelevant.
- **Secrets never at rest.** Read-time filtering leaves credentials in
  Postgres — in backups, in `pg_dump`s, visible to DB access — and every
  future read surface (new RPC, export, retention tooling) must remember to
  filter. Tee filtering keeps them out of the database entirely, which is the
  trust-boundary restoration the issue asks for.
- The known cost: tee filtering cannot help bytes already shipped (handled by
  the purge slice below) and a driver-side bug ships raw bytes silently. The
  second is mitigated by the filter being a tiny pure package with exhaustive
  unit tests, and by retention as the backstop (see Open Questions).

### `internal/redact`

A pure, dependency-free package (the `toollog` shape):

```go
// Rules is the set of things Writer masks. Zero value masks nothing.
type Rules struct{ ... }

// AddLiteral registers a known secret value. Values shorter than 8 bytes are
// ignored (degenerate masks). Long literals also match by 16-byte prefix, so
// a value truncated by toollog's 120-rune cap is still caught.
func (r *Rules) AddLiteral(name, value string)

// AddSensitiveEnv registers the values of environment entries whose names
// match the sensitivity pattern (TOKEN|SECRET|KEY|PASSWORD|PASSWD|CREDENTIAL|AUTH).
func (r *Rules) AddSensitiveEnv(environ []string)

// DefaultPatterns is the curated pattern set (see table).
var DefaultPatterns []Pattern

// NewWriter wraps w in a line-buffering masking filter. Write never returns
// an error and never blocks beyond w's own Write. Flush scans and forwards
// any held partial line.
func NewWriter(w io.Writer, rules *Rules) *Writer
func (w *Writer) Flush()
```

Matches are replaced with `[gritz:masked <rule>]` — e.g.
`[gritz:masked env:CODEX_API_KEY]` or `[gritz:masked github-token]` — in the
style of the shipper's existing `[gritz: dropped N log bytes]` marker. Naming
the rule costs nothing secret and makes an over-masked log debuggable.

**Line buffering.** Writes into the sink are arbitrary byte runs (slog lines,
`io.Copy` splats from setup commands), so a secret can straddle two `Write`
calls. The writer buffers to the newline, scans complete lines, and forwards
them. A held partial line is capped at 64 KiB: past the cap, the writer scans
and forwards all but a 1 KiB tail (larger than any pattern window), so a
pathological no-newline stream still ships with bounded memory and no
straddle gap. `Flush` scans and forwards the remainder.

**Patterns.** Deliberately small and prefix-anchored — the industry moved to
prefixed token formats precisely so they can be recognized with near-zero
false positives. The starting set (formats, not live values):

| rule | shape |
|---|---|
| `github-token` | `gh[pousr]_[A-Za-z0-9]{36,}` and `github_pat_\w{22,}` — covers App installation tokens (`ghs_…`), the runtime-minted case the driver cannot know |
| `jwt` | `eyJ…\.eyJ…\.…` three-part base64url — catches any echoed task token even when the literal rule misses (re-minted, truncated differently) |
| `anthropic-key` | `sk-ant-…` |
| `openai-key` | `sk-[A-Za-z0-9]{20,}` |
| `slack-token` | `xox[abprs]-…` |
| `aws-key-id` | `AKIA[0-9A-Z]{16}` |
| `bearer` | `(?i)bearer\s+[A-Za-z0-9._~+/=-]{20,}` — masks the credential, keeps the word `Bearer` |
| `url-credential` | `://user:pass@` userinfo in URLs — masks the password component; catches the private-repo clone pattern and git's own error echoes |
| `pem-block` | stateful: from `-----BEGIN … PRIVATE KEY-----` to the matching `END` line — the one multi-line rule |

Entropy scanning and large imported rule sets (gitleaks et al.) are explicitly
rejected — see Trade-offs. Over-masking an occasional commit hash or base64
blob is acceptable in a diagnostic log; missing quietly is the failure mode to
avoid, and the two-tier design means patterns only need to carry the
runtime-minted residue, not the whole load.

### Driver wiring

`command/driver.go` builds the rules where the secrets are in hand:

```go
rules := &redact.Rules{}
rules.AddLiteral("token", cmd.String("token")) // the task JWT
rules.AddSensitiveEnv(os.Environ())            // workspace env, image env, GRITZ_TOKEN
rules.AddPatterns(redact.DefaultPatterns...)
```

`agent.OpenDriverLog(logPath, shipper)` grows a rules parameter and splices
the filter into only the shipped branch:

```go
filtered := redact.NewWriter(shipper, rules)      // nil rules → shipper as-is
sink := io.MultiWriter(file, filtered)
```

Every existing tee — slog handler, `Sink()`, `Stdout()`, `Stderr()`,
`StartRun` — is filtered automatically, because they all already write through
the sink; no call site changes. `DriverLog` holds the filter alongside the
shipper for two ordering points:

- `Flush`/`Close` call `filtered.Flush()` before `shipper.Flush(ctx)`, so a
  held partial line ships before the drain.
- `StartRun` calls `filtered.Flush()` before `shipper.SetVersion(version)`,
  so a buffered preamble fragment is not stamped into the new run's chunks.

`os.Environ()` at driver start is the right snapshot: it includes the
workspace `environment:` map, the runner's `GRITZ_TOKEN`, and image-baked
variables, all before any setup command can mutate anything. The name
heuristic (rather than masking every env value) keeps `PATH=/usr/bin` from
poisoning the literal set; a workspace secret under an unmatched name is the
known gap (see Open Questions).

### Fixing the structural leaks at the source

The byte-layer filter is the backstop, not an excuse to keep logging secrets
on purpose:

- `internal/agent/copilot.go:44` — stop logging the MCP config JSON; log the
  server names and count instead (matching the driver's own
  `"mcp_servers", len(cfg.McpServers)` restraint in `driver.go:179`).
- `internal/agent/dummy.go:122` — drop `"args", mcpConfig.Args` from the
  "connecting to MCP server" line; name and command suffice.

These are plain `fix:` changes, independently landable, and worth shipping
first — they remove the two *unconditional* token disclosures regardless of
when the filter lands.

### Already-shipped logs: purge, not rewrite

In-place scrubbing of stored chunks would have to reassemble straddled and
duplicated chunks per task and rewrite rows — machinery out of proportion to
a feature that shipped days ago. Instead:

- **A per-task purge**: store method `DeleteLogChunksByTask(ctx, tx, taskID)`,
  an RPC `DeleteLogChunksByTask` gated like task mutation
  (`OpTaskWrite` on the task's scope attrs), and `gritz logs purge <task-id>`.
  This is also the standing incident-response tool for the day detection
  misses something ("this task's log caught a secret — purge it").
- **A one-time cleanup pass**, operational rather than code: scan existing
  chunks server-side with the same pattern set (a one-off query or script is
  fine — at rest the chunks are small and few), purge the hit tasks' logs,
  and **archive any task whose transcript contained its task JWT** — every
  Copilot task, per the survey — since archiving is what revokes the token.

### What stays raw, deliberately

- `/gritz/log` and stderr/docker-logs: in-sandbox and runner-host surfaces,
  unchanged trust boundary, full fidelity for `gritz shell` post-mortems.
- Shell/PTY bytes: never shipped (`shellwire.Data` frames only), out of scope.
- Event payloads (`task failed` reasons, `report` messages): a different,
  lower-volume surface with its own audience; see Open Questions.

## Implementation Plan

1. **Source fixes** — Delivers: `fix:` for `copilot.go:44` (MCP config dump →
   names/count) and `dummy.go:122` (drop argv from the connect line). Depends
   on: nothing; landable immediately. Verifiable by: agent tests asserting the
   injected `--token` value never appears in the sink for a Copilot/dummy run.
2. **`internal/redact` package** — Delivers: `Rules` (literals with prefix
   matching, `AddSensitiveEnv`, `DefaultPatterns`, the stateful PEM rule) and
   the line-buffering `Writer` with holdback and `Flush`. Depends on: nothing.
   Verifiable by: unit tests covering secrets straddling `Write` boundaries,
   truncated tokens (120-rune prefix still masked), the no-newline holdback
   path, multi-line PEM blocks, and rule-name markers.
3. **Driver wiring** — Delivers: rule construction in `command/driver.go`,
   the `OpenDriverLog` splice, and the `Flush` ordering in
   `DriverLog.Flush`/`Close`/`StartRun`. Depends on: (2). Verifiable by: a
   driver test with a dummy agent and a planted sensitively-named env var
   asserting the value appears in `/gritz/log` but only `[gritz:masked …]` in
   the bytes the fake server received.
4. **Purge tool** — Delivers: `DeleteLogChunksByTask` store method + RPC
   (task-scoped `OpTaskWrite` auth, following the `AppendLogChunk` handler
   shape) and `gritz logs purge <task-id>`. Depends on: nothing (parallel to
   2–3). Verifiable by: store/handler tests (delete scoped to the task and
   org, auth rejection) and an end-to-end purge leaving `gritz logs` empty.
5. **One-time cleanup** — Operational, not a PR: run the pattern scan over
   existing chunks, purge hits via (4), archive tasks whose transcripts held
   their live JWT. Verifiable by: re-running the scan → zero hits.

Slice 1 should land first; 2–3 and 4 are independent stacks.

## Trade-offs

- **Tee filtering vs. ingest vs. read time.** Argued above: only the driver
  knows the local secret values, chunk boundaries make server-side scanning
  stateful and retry-hostile, and read-time filtering leaves credentials at
  rest and must be re-remembered by every future reader. The tee's weakness —
  it cannot fix history — is one purge command, once, while the feature is
  days old. A belt-and-suspenders ingest scan on top of tee filtering was
  considered and dropped: it re-introduces the cross-chunk state machine for
  marginal coverage, and retention (below) is a cheaper backstop.
- **Known values + curated patterns vs. entropy/ruleset scanners.** A
  gitleaks-style ruleset or entropy detector maximizes recall but floods a
  *log* — full of hashes, ids, and base64 — with false positives, and drags
  in a dependency and a config surface. The two-tier split assigns each tier
  what it is good at: literals for everything the driver was handed
  (exhaustive, exact), prefix-anchored patterns for the runtime-minted
  remainder (installation tokens, JWTs), where prefixes exist precisely to
  make this reliable.
- **Narrowing read access instead of scrubbing.** Rejected as the primary
  lever: org members hold the wildcard admin scope
  (`internal/auth/apiauth/jwt.go:42`) — there is no role model to narrow
  *with*, and building one is a far bigger project than a filter. More
  fundamentally, any member can already shell into a live task's sandbox and
  read the raw log, so read-narrowing shipped logs would not change who can
  see secrets — only make the sanctioned path less useful. And it does
  nothing about secrets at rest in Postgres. Scrubbing attacks the actual
  delta that shipping created.
- **Filtering only the shipped branch vs. also `/gritz/log`.** Filtering the
  file too would be one more wrapped writer, but it destroys post-mortem
  fidelity inside a boundary that already holds the secrets in env, config
  files, and `/proc` — masking the log there is theater. The asymmetry is the
  point: raw where the secrets already live, masked where the audience
  widened.
- **Fix the leaky log lines vs. rely on the filter.** Both. Source fixes are
  precise but only cover known lines; the filter catches the long tail
  (stderr echoes, setup output, verbose mode) but is best-effort. Neither
  alone is sufficient; together the filter is a backstop rather than the only
  line of defense.
- **Purge vs. rewrite for shipped history.** Rewriting chunks in place
  preserves diagnostics but requires reassembling straddled/duplicated chunks
  and rewriting rows — for a handful of young tasks. Purging loses those
  transcripts (still on sandbox disk where the sandbox survives) and is one
  small store method that doubles as permanent incident-response tooling.

## Open Questions

- **Retention as the backstop (#1241).** Detection is best-effort by nature;
  a bounded retention window caps the exposure of anything missed. There is
  currently *no* deletion path at all — chunks outlive archiving and die only
  with the task row. Recommendation: resolve #1241's open question with
  age-based GC (delete chunks N days after their task is archived —
  transcripts' diagnostic value decays fast), as a separate small proposal;
  with tee filtering in place the window does not need to be aggressive.
- **Env name heuristic vs. masking every env value.** `AddSensitiveEnv`'s
  name pattern misses a secret under a bland name (`MY_CREDS=`). Masking
  every env value regardless of name would catch it at the cost of masking
  `HOME=/root` everywhere. An explicit opt-in (a `sensitive: [NAMES]` list in
  the workspace `environment:` block, GitHub-Actions-`add-mask` style) is the
  clean fix if the heuristic proves too loose — worth deciding during review.
- **Event payloads.** `task failed` reasons and agent `report` messages can
  carry the same material into the events table (e.g. an error string echoing
  a credentialed URL). Should the same `Rules` wrap the driver's event
  submissions? Cheap to add once the package exists, but it widens scope past
  the log pipeline this issue is about.
- **Trusting the truncation interaction.** `toollog.Summarize`'s 120-rune cap
  can slice a token so that neither the full literal nor a prefix-anchored
  pattern is guaranteed to see a match window (mitigated by literal prefix
  matching and open-ended quantifiers, but not provably closed). If review
  wants this airtight, the alternative is masking *before* summarizing —
  running tool-input strings through `Rules` at the `toollog` call sites —
  at the cost of spreading redaction knowledge into the agents.
