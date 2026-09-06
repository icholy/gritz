import { useTaskLogs } from '@/hooks/use-task-logs'
import { Scrollback } from '@/components/scrollback'
import { Loader2 } from 'lucide-react'

// TaskLogs renders the task's shipped driver log — the server-side mirror of
// the sandbox's /gritz/log — as a monospace scrollback. It opens at the tail
// (a post-mortem reads the end first), loads older pages on scroll-up, and
// polls the tail. `live` only picks the empty-state wording; it does not gate
// polling, since a task's last chunks land after it reports terminal. The bytes are a raw terminal
// transcript, not structured events, so they render as plain preformatted text.
export function TaskLogs({ taskId, live }: { taskId: bigint; live: boolean }) {
  const { text, isLoading, hasOlder, loadOlder, isLoadingOlder } = useTaskLogs(taskId)

  if (isLoading) {
    return (
      <div className="flex flex-1 items-center justify-center text-muted-foreground">
        <Loader2 className="mr-2 h-4 w-4 animate-spin" />
        Loading logs…
      </div>
    )
  }

  if (!text) {
    return (
      <div className="flex flex-1 items-center justify-center text-muted-foreground">
        {live ? 'No logs yet.' : 'No logs.'}
      </div>
    )
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Scrollback
        contentLength={text.length}
        hasOlder={hasOlder}
        loadOlder={loadOlder}
        isLoadingOlder={isLoadingOlder}
        className="p-4"
      >
        <pre className="whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">
          {text}
        </pre>
      </Scrollback>
    </div>
  )
}
