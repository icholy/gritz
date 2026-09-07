# Task page tabs: keep `?tab=`, not separate routes

Issue: https://github.com/icholy/gritz/issues/1573

## Problem

The task detail page (`webui/src/routes/tasks.$id.tsx`) is one route that picks
its main panel from a `?tab=` search param:

```tsx
validateSearch: (search: Record<string, unknown>): { tab?: Exclude<TaskTab, 'timeline'> } => {
  const tab = toTaskTab(search.tab)
  return tab === 'timeline' ? {} : { tab }
},
```

and then renders the panel from three conditionals in `TaskDetail`:

```tsx
{tab === 'timeline' && <TaskTimelineChat … />}
{tab === 'logs' && <TaskLogs taskId={taskId} live={!isTerminalTask(task)} />}
{tab === 'shell' && <TaskShellPanel taskId={taskId} orgId={orgId} canOpen={canOpenShell(task)} />}
```

Should each panel instead be its own route — `/tasks/$id`, `/tasks/$id/logs`,
`/tasks/$id/shell` — with `tasks.$id.tsx` demoted to a layout route holding the
sidebar and the shared page chrome?

## Recommendation

**Keep the query param.** Split-routing this page buys nothing the page doesn't
already have, costs a permanent redirect shim and a second tab convention, and
introduces two concrete regressions (org switching, unknown-segment handling)
that have to be actively defended against.

The three things people actually want from the routes proposal — clickable tab
hrefs, sane back/forward, and getting xterm.js out of the entry bundle — are all
reachable without touching the routing model. This proposal recommends doing
those instead; they are the Implementation Plan below.

## Design

### What the page looks like today

`TaskDetail` mounts, unconditionally:

- `useQuery(getTask)` and `useQuery(listLinks)`, both on a 60s `refetchInterval`
- `useTaskTimeline(taskId)` (`webui/src/hooks/use-task-timeline.ts`) — the
  bidirectional infinite query over `ListEventsByTask`, whose `timeline.length`
  feeds the sidebar's Timeline badge and whose `follow()` is called by every
  mutation's `refetchAll`
- `useShellState(String(taskId))` — a `useSyncExternalStore` read of the
  `ShellSessions` singleton, so the sidebar can show the Shell activity dot
- the six mutations (`updateTask` ×2, `archiveTask`, `unarchiveTask`,
  `cancelTask`, `restartTask`) and the instruction composer's local state

Conditionally, per tab, it mounts exactly one of `TaskTimelineChat`,
`TaskLogs`, `TaskShellPanel`.

So the page already has the layout/child split that routing would formalise —
it is just expressed as "hooks at the top, three conditionals at the bottom"
rather than as a parent route with an `<Outlet />`.

### Axis 1 — deep linking

Already solved. `?tab=logs` and `?tab=shell` are real, shareable, refresh-stable
URLs; that is exactly what `e3679790` ("deep-link task page panels via ?tab=
search param", #1210) was for. `/tasks/1557/logs` is prettier than
`/tasks/1557?tab=logs&org=1`, and that is the entire delta. It is not nothing,
but it is aesthetics, and it is the only unambiguous win on the routes side.

### Axis 2 — back / forward

This axis is **independent of the routing model**, which is the main reason it
shouldn't drive the decision. Today `setTab` passes `replace: true`:

```tsx
navigate({ to: '/tasks/$id', params: { id }, search: (prev) => ({ …prev, tab: … }), replace: true })
```

so switching tabs does not push a history entry: from `/tasks` → task → Logs,
Back returns to `/tasks`, not to the timeline. A route split would, by default,
change this — `<Link>` pushes unless told otherwise — but that is `<Link>`'s
default, not routing's semantics. `<Link to="/tasks/$id" search={{ tab: 'logs' }}>`
pushes too.

Recommendation: **drop `replace: true`**. A tab is a distinct, linkable view of
the task, and Back undoing a tab switch is what users expect from one. Getting
this costs one deleted line.

### Axis 3 — per-tab loaders and code splitting

**Loaders**: the webui has zero route loaders. Every route fetches in the
component via connect-query (`useQuery(getTask, …)`, `useInfiniteQuery(
listEventsByTask, …)`); the router's only data hook is `__root.tsx`'s
`beforeLoad`, which does org adoption, not fetching. Introducing loaders on this
one page would mean either duplicating the connect-query fetch in a loader or
hand-rolling `queryClient.ensureQueryData` with `createConnectQueryKey` — a
second data-fetching convention for one page. Both timeline and logs are also
tail-first infinite queries that must be *live* (`follow()` on the SSE
`task_events` signal, `useVisibilityInterval` backstops), which is component
state a one-shot loader can't own. Loaders are a non-benefit here.

**Code splitting**: `vite.config.ts` says it plainly —

```
// The app ships as a single bundle (no route/vendor code-splitting yet),
```

`@xterm/xterm` + `@xterm/addon-fit` are the heaviest thing on this page and are
in the entry bundle for every user, including the ones who never open a shell.
That is worth fixing — but the fix is `React.lazy` around `TaskShellPanel`, four
lines in `tasks.$id.tsx`, and it works identically under either routing model.
Route-level splitting (`TanStackRouterVite({ autoCodeSplitting: true })`) is an
app-wide change that is worth doing on its own merits and does not require this
page to be split into routes first.

### Axis 4 — type-safe params and file layout

`TaskTab` is already a closed union (`webui/src/lib/task.ts`) validated at the
route boundary, and `toTaskTab` is unit-tested (`src/lib/task.test.ts`). Path
params would be type-safe too — this is a wash, with one asymmetry that favours
the query param:

```ts
export function toTaskTab(value: unknown): TaskTab {
  if (value === 'logs' || value === 'shell') return value
  return 'timeline'
}
```

An unknown value degrades to the timeline. That is load-bearing: `?tab=links`
was a real tab until links moved into the sidebar, and those links still resolve
to a usable page. Under path routing, `/tasks/1557/links` matches no route, and
the app configures no `notFoundComponent` anywhere — the user gets TanStack's
bare default instead of the task. Preserving today's behaviour would mean adding
a `$` splat child or a page-level `notFoundComponent` that redirects to the
index; more machinery for strictly worse graceful degradation.

### Axis 5 — pathless layout route and the shared chrome

This is where the real cost is. A split needs four files instead of one
(`tasks.$id.tsx` as the layout, plus `tasks.$id.index.tsx`, `tasks.$id.logs.tsx`,
`tasks.$id.shell.tsx` — flat-dotted, matching `schedules.$id.edit.tsx`), and the
layout has to hand three things down to children:

1. **`followTimeline`.** Every mutation's `onSuccess: refetchAll` calls
   `refetch()` *and* `followTimeline()`. `submitInstruction` — which lives with
   the composer, i.e. in the timeline child — needs the layout's `updateMutation`,
   which needs the layout's `follow`.
2. **`timelineCount`.** The sidebar's Timeline badge is `timeline.length`, so
   `useTaskTimeline` cannot simply move down into the timeline child.
3. **`task`.** `TaskLogs` needs `live={!isTerminalTask(task)}` and
   `TaskShellPanel` needs `canOpen={canOpenShell(task)}`.

TanStack Router has no `useOutletContext`. Route context is built in
`beforeLoad`, before the component tree exists, so it cannot carry a hook
result. The options are a page-local React context provider (the webui only uses
context for app singletons — `ServicesProvider` in `src/lib/services.tsx`), or
re-calling `useTaskTimeline(taskId)` in the child. The re-call is *safe* —
connect-query dedupes on the input, and `TimelineFollowers.register` keeps a
`Set` precisely so "more than one mount of the same task (a StrictMode
double-mount, or a route transition)" works — but it doubles the
`useVisibilityInterval` backstop poll and gives the page two subscriptions to
the same stream. Either way: new structure, no new user-visible behaviour.

### Axis 6 — backwards compatibility

First, a correction to the premise. Nothing on the server emits a `?tab=` task
URL. `model.TaskURL` (`internal/model/url.go`) is the single generator, and it
produces `%s/ui/tasks/%d?org=%d` — no tab. The MCP tools, the notification
payloads, and the agent prompt all go through it. A repo-wide grep for `tab=`
finds only `tasks.$id.tsx`, `lib/task.ts`, the CHANGELOG entry for #1210, and
`github.setup.tsx`'s `window.location.href = '/ui/settings?tab=organisation'`
(the settings page, not this one).

So the exposure is narrower than assumed: `?tab=` task links exist only where a
human or an agent copied one out of the address bar — Slack, PR comments, issue
threads, bookmarks. Narrower, but permanent, and exactly the places you can't
rewrite.

Handling it under a split is not hard, but it is forever:

```tsx
// tasks.$id.tsx (layout) — keep `tab` in validateSearch solely to redirect it away
beforeLoad: ({ params, search }) => {
  const tab = toTaskTab(search.tab)
  if (search.tab === undefined) return
  throw redirect({
    to: tab === 'timeline' ? '/tasks/$id' : `/tasks/$id/${tab}`,
    params, replace: true,
    search: ({ tab: _drop, ...rest }) => rest, // must drop `tab` or it loops
  })
}
```

That shim can never be deleted, so the split doesn't actually retire the query
param — it adds a path form on top of it.

### Axis 7 — the shell session (and the logs tail)

This is the loudest objection to a split, and it turns out to be a non-issue.
Worth stating precisely, because the reason is not obvious.

`ShellSessions` (`webui/src/lib/shell-sessions.ts`) lives outside React, next to
`AuthTransport` and `NotificationSSE`, constructed once in `main.tsx` and
injected through `ServicesProvider`. Two properties make it immune to
mount/unmount churn:

- The socket is **only ever created in `open()` / `connect()`** — a user click —
  never in an effect. Mounting or unmounting the shell page cannot open or close
  a socket.
- `detach()` is a deliberate no-op:

  ```ts
  // detach is intentionally a no-op: a shell session persists across navigation.
  detach(key: string): void { void key }
  ```

  (Note: `TaskShell`'s comment still says "detach is grace-delayed in the
  singleton". That is stale — the grace delay was replaced by the no-op. Fixing
  the comment is slice 4 below.)

A session ends on the shell process exiting, an explicit `close()`, or the
browser unloading the tab. `TaskShell`'s `subscribeOutput` replays up to
`MAX_SCROLLBACK` (512 kB) into a freshly created xterm instance, so a remount
re-renders what the session already produced.

A route transition from `/tasks/$id/shell` to `/tasks/$id` unmounts exactly the
same subtree that `{tab === 'shell' && …}` unmounts today. **Same unmount, same
outcome: the session survives.** The parent layout stays mounted across a
sibling-child transition, so the sidebar's `useShellState` keeps reading the
singleton and the activity dot keeps working. This architecture was built for
StrictMode's double-invocation, which is strictly harsher than a route
transition — it already handles this.

The logs tail is the same story, and also already lossy in a way routing won't
change. `useTaskLogs` is called *inside* `TaskLogs`, so switching away from Logs
today already unmounts the hook: the 5s `FOLLOW_POLL_MS` tail poll stops and
`Scrollback`'s scroll position is lost (it re-opens pinned to the bottom on
mount). Coming back re-mounts against a still-warm react-query cache. A route
split reproduces this exactly. If we want the logs tail to keep polling or the
scroll offset to survive a tab switch, that is a separate change — hoist the
hook, or keep the panel mounted and hidden — and it is equally available under
either model.

**Net: no routing-side risk to the shell, and no routing-side improvement to
logs.** The one thing that *would* break a live shell is a full page navigation
(the socket dies with the document), and neither model does that.

### Axis 8 — consistency with the rest of the webui

Three pages in `webui/src/routes/` have tabs. All three use `?tab=`:

| Page | Route | Mechanism |
| --- | --- | --- |
| Settings | `settings.tsx` | `validateSearch: … ({ tab: toSettingsTab(search.tab) })`, shadcn `<Tabs>` |
| Events | `events.index.tsx` | `validateSearch: … ({ tab: toEventsTab(search.tab) })`, shadcn `<Tabs>` |
| Task detail | `tasks.$id.tsx` | `validateSearch` + `toTaskTab`, sidebar switcher |

The task page's tab param was explicitly modelled on settings — from
`e3679790`: *"following the same validateSearch + navigate pattern used by the
settings page"*. Nothing else in the app nests a route purely to select a panel;
`schedules.$id.edit.tsx` and `keys.new.tsx` are separate routes because they are
separate *pages* with separate chrome.

Splitting one of the three creates a second convention. Splitting all three is a
much larger change than the issue asks for, and settings/events would gain even
less than the task page does.

### The two regressions a split has to actively defend against

Both are easy to miss in review, which is itself an argument.

**1. Org switching silently breaks.** `__root.tsx`:

```tsx
const route = useMatches().at(-1)
const redirect = route?.staticData.orgSwitchRedirect
```

It reads `staticData` off the **leaf** match. `staticData` is per-route and is
not inherited from parents, so `staticData: { orgSwitchRedirect: '/tasks' }` on
the `tasks.$id.tsx` layout would not be visible from `/tasks/$id/logs`. The
fallback branch runs instead — `navigate({ to: '.', search: { org } })` — which
keeps the user on task 1557 in an org that doesn't have a task 1557. Fixing it
means repeating the `staticData` on all three children, or teaching `__root` to
walk the match chain for the nearest `orgSwitchRedirect` (the better fix, but
now the split is touching the root route).

**2. Unknown segments 404.** Covered in Axis 4: `?tab=links` degrades to the
timeline; `/tasks/1557/links` does not.

## Implementation Plan

The recommendation is "keep the query param", so the plan is the set of changes
that deliver the routes proposal's real motivations without the split. Each
slice is independently mergeable and independently revertable; none depends on
another.

1. **Sidebar view switcher becomes `<Link>`** — Delivers: `ViewItem` in
   `webui/src/components/task-sidebar.tsx` renders a `<Link to="/tasks/$id"
   params={{ id }} search={(prev) => ({ …prev, tab })} replace>` instead of a
   `<button onClick>`, so the three tabs have real `href`s: hover preview,
   middle-click and ⌘-click open a tab in a new browser tab, "Copy link address"
   works. Keeps `replace` so this slice is a pure refactor with no behavioural
   change. `TaskSidebar` drops the `onTabChange` prop and `TaskDetail` drops
   `setTab`. Depends on: nothing. Verifiable by: `pnpm lint` clean; each tab has
   the expected `href`; ⌘-clicking Logs opens `?tab=logs` in a new browser tab;
   clicking still switches in place without a history entry.

2. **Tab switches push instead of replace** — Delivers: drop `replace` from the
   sidebar `<Link>`s, so Back/Forward walk the tab history. Depends on: (1).
   Verifiable by: task → Logs → Shell → Back lands on Logs, Back again on the
   timeline, Forward retraces. One-line revert if it turns out to be annoying —
   which is the point of keeping it off slice 1.

3. **Lazy-load the shell panel** — Delivers: `TaskShellPanel` behind
   `React.lazy` + `<Suspense>` in `tasks.$id.tsx`, moving `@xterm/xterm` and
   `@xterm/addon-fit` out of the entry bundle into a chunk fetched on first
   Shell open. Depends on: nothing. Verifiable by: `pnpm build` shows a separate
   xterm chunk and a smaller entry chunk (record both numbers in the PR); the
   Shell tab still opens, attaches, and — critically — a session opened, left,
   and returned to is still `connected` with its scrollback intact.

4. **Fix the stale `detach` comment** — Delivers: `TaskShell`'s "detach is
   grace-delayed in the singleton, so StrictMode's mount→cleanup→mount nets one
   live socket" is corrected to match `ShellSessions.detach`, which is now an
   intentional no-op; add a line to `TaskTab`'s docstring in `src/lib/task.ts`
   recording that the query param is the deliberate choice and pointing at this
   proposal. Depends on: nothing. Verifiable by: reading it. `docs:` commit.

If the split is later adopted anyway, the ordered stack would be: (a) `__root`
walks the match chain for `orgSwitchRedirect`, merged and verified on its own;
(b) `tasks.$id.tsx` becomes a layout with `<Outlet />` and a page-local context
for `task` / `follow` / `timelineCount`, with the index child still rendering
the timeline so `model.TaskURL` needs no change; (c) the `logs` and `shell`
children; (d) the `?tab=` `beforeLoad` redirect shim plus a `notFoundComponent`
for unknown segments; (e) settings and events converted for consistency. Note
that (a) and (d) are pure tax — no user-visible change — which is roughly the
shape of the argument against.

## Trade-offs

**Chosen: keep `?tab=`.** Zero migration cost, zero back-compat surface, one
file, one convention shared with settings and events, `toTaskTab`'s graceful
degradation preserved, and the shell/logs mount boundaries left exactly where
they are today. Costs: URLs stay `?tab=logs&org=1` rather than `/logs`, and the
page keeps its three conditionals rather than an `<Outlet />`.

**Rejected: nested routes** (`/tasks/$id/{,logs,shell}`). Gains a prettier URL
and a structure that some reviewers find more idiomatic. Costs: a permanent
redirect shim (so the query param never actually goes away), four files, a
page-local context or a duplicated timeline subscription, two latent regressions
(org switch, 404 on unknown segments), and a second tab convention in an app
that has exactly one. The gains it is usually credited with — code splitting and
back/forward — are available without it, which is what slices 2 and 3 do.

**Rejected: convert all three tabbed pages to routes.** Consistent, but a much
larger change than the issue scopes, and settings and events benefit even less
than the task page — their tabs are content sections, not distinct app views.

**Rejected: hybrid** (routes for the task page, `?tab=` elsewhere). Worst of
both: the migration cost of the split plus the inconsistency of two conventions.

## Open Questions

1. **Is slice 2 (push instead of replace) actually wanted?** Pushing per tab
   switch means someone who opens a task from a notification, clicks through
   Logs and Shell, then hits Back three times to get out. The alternative is
   keeping `replace` — Back always exits the task, tab state never enters
   history. This proposal recommends push; it is deliberately its own slice so
   the answer can be "no" without blocking anything else.

2. **Should the logs tail keep polling while another tab is shown?** Today it
   stops, because `useTaskLogs` is scoped to `TaskLogs`. Hoisting it to
   `TaskDetail` (like `useTaskTimeline`) would keep the 5s tail poll running and
   let a returning user land on fresher output, at the cost of a background poll
   for every open task page. Out of scope here — this proposal only establishes
   that routing does not change the answer either way.

3. **Is app-wide route code-splitting worth enabling?**
   (`TanStackRouterVite({ autoCodeSplitting: true })`, plus removing the
   `chunkSizeWarningLimit: 1500` workaround.) Independent of this decision, but
   slice 3 is a good moment to measure whether the entry bundle warrants it.
