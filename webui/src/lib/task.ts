import { timestampDate } from '@bufbuild/protobuf/wkt'
import { TaskStatus, type Task } from '@/gen/gritz/v1/gritz_pb'
import { durationToMillis } from '@/lib/duration'

type TaskLike = Pick<Task, 'status' | 'actions' | 'archived'>
type AutoArchiveTask = TaskLike & Pick<Task, 'autoArchive' | 'updatedAt'>

export function canArchiveTask(task: TaskLike): boolean {
  return task.actions?.archive ?? false
}

export function canUnarchiveTask(task: TaskLike): boolean {
  return task.actions?.unarchive ?? false
}

export function canCancelTask(task: TaskLike): boolean {
  return task.actions?.cancel ?? false
}

export function canRestartTask(task: TaskLike): boolean {
  return task.actions?.restart ?? false
}

export function isArchivedTask(task: TaskLike): boolean {
  return task.archived
}

// canOpenShell reports whether an in-browser debug shell can be opened for the
// task. OpenShell relaunches the sandbox against the task's preserved disk, so it
// is only valid for a finished (terminal) task — mirroring the server's
// FailedPrecondition guard, which requires a terminal status.
export function canOpenShell(task: TaskLike): boolean {
  switch (task.status) {
    case TaskStatus.COMPLETED:
    case TaskStatus.FAILED:
    case TaskStatus.CANCELLED:
      return true
    default:
      return false
  }
}

// taskLabel is a task's display name. A task exists before the agent gets
// around to naming it, so unnamed tasks fall back to their id rather than
// rendering as an empty row.
export function taskLabel(task: Pick<Task, 'id' | 'name'>): string {
  return task.name || `Unnamed - ${task.id}`
}

// taskSearchValue is what the quick-switch palette fuzzy-matches a task
// against. cmdk requires item values to be unique and appends the item's
// keywords to this string before scoring, so appending the id both
// disambiguates same-named tasks and makes typing a bare task number find it.
export function taskSearchValue(task: Pick<Task, 'id' | 'name'>): string {
  return `${taskLabel(task)} ${task.id}`
}

// TaskTab identifies which view of the task detail page is shown. It is mirrored
// in the URL's ?tab= search param so views can be deep-linked and shared. Links
// are not a view — they live in the task sidebar.
export type TaskTab = 'timeline' | 'shell'

// toTaskTab normalizes an untrusted value (e.g. a URL search param) into a valid
// tab, falling back to the default "timeline" view. This also swallows stale
// ?tab=links deep links from when links were a tab.
export function toTaskTab(value: unknown): TaskTab {
  if (value === 'shell') return value
  return 'timeline'
}

// autoArchiveDeadline returns the time at which the task is scheduled to be
// auto-archived, or null when it isn't. The timer only runs once a task is in a
// terminal state, so this requires the task to be archivable (terminal and not
// yet archived), auto_archive to be positive, and updated_at (the terminal
// timestamp) to be set.
export function autoArchiveDeadline(task: AutoArchiveTask): Date | null {
  if (!canArchiveTask(task) || !task.autoArchive || !task.updatedAt) {
    return null
  }
  const afterMs = durationToMillis(task.autoArchive)
  if (afterMs <= 0) {
    return null
  }
  return new Date(timestampDate(task.updatedAt).getTime() + afterMs)
}
