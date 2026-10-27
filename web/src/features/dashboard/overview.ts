import type {QueueTask, Run} from '@/lib/types'

export interface NextTask {
  task: QueueTask
  // null: due now (asap, or a fixed time already passed).
  at: Date | null
}

// nextAt is when the scheduler will next consider a task: an asap task and a
// fixed task whose time has come are due now, a cron task at its next run.
export function nextAt(t: QueueTask, now = Date.now()): Date | null | undefined {
  if (t.trigger === 'asap') return null
  if (t.trigger === 'fixed') {
    if (!t.fixed_at) return null
    const at = new Date(t.fixed_at)
    return at.getTime() <= now ? null : at
  }
  return t.next_run ? new Date(t.next_run) : undefined
}

// upNext is what the queue will start next: enabled tasks that are not
// running, due ones first in queue order, then by start time. Quiet-history
// tasks are left out: a frequent watcher would otherwise always fill the card.
export function upNext(tasks: QueueTask[], now = Date.now()): NextTask[] {
  const out: (NextTask & {i: number})[] = []
  tasks.forEach((task, i) => {
    if (!task.enabled || task.running || task.quiet_history) return
    const at = nextAt(task, now)
    if (at === undefined) return
    out.push({task, at, i})
  })
  out.sort((a, b) => {
    const ta = a.at ? a.at.getTime() : -Infinity
    const tb = b.at ? b.at.getTime() : -Infinity
    return ta === tb ? a.i - b.i : ta - tb
  })
  return out.map(({task, at}) => ({task, at}))
}

export const FAILED: Run['status'][] = ['failed', 'auth_error']

// recentFailures are the failed runs of the last day, newest first: what needs
// a look after a night of unattended work.
export function recentFailures(runs: Run[], now = Date.now(), windowMs = 24 * 3600_000): Run[] {
  return runs
    .filter(
      (r) => FAILED.includes(r.status) && now - new Date(r.finished_at ?? r.started_at).getTime() <= windowMs,
    )
    .sort((a, b) => new Date(b.started_at).getTime() - new Date(a.started_at).getTime())
}

// waitingRuns are runs a rate limit paused whose session will still resume.
export function waitingRuns(runs: Run[]): Run[] {
  return runs.filter((r) => r.status === 'rate_limited_waiting' && r.resume_pending)
}

// holdText is the one-line reason a due task is not starting, if any.
export function holdText(t: QueueTask): string {
  if (t.blocked_reason) return t.blocked_reason
  if (t.waiting_for_limit) return 'Rescheduled: waits for the rate limit to reopen'
  if (t.hold) return t.hold.reason
  if (t.waiting_for?.length) return `Waits for ${t.waiting_for.join(', ')}`
  return ''
}

export type CardId = 'failed' | 'next' | 'running' | 'week'

// cardOrder puts what needs action first: failures lead while there are any,
// then what starts next, what runs, and the week's tally. Without failures
// their card says so last, where a quiet "all clear" belongs.
export function cardOrder(failures: number): CardId[] {
  return failures > 0 ? ['failed', 'next', 'running', 'week'] : ['next', 'running', 'week', 'failed']
}
