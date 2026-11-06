import {confirm} from '@/components/ui/confirm'
import {toast} from '@/components/ui/toaster'
import {openTaskEditor} from '@/features/task-editor/store'
import {errorMessage, get, post} from '@/lib/api'
import {invalidate, keys, queryClient} from '@/lib/queries'
import type {Run, RunDetail} from '@/lib/types'
import {currentPlatform} from '@/lib/platform'

// Every action on a run, shared by the Log list and the run log sheet. Each one
// reports whether it went through, so a caller keeps its own state in step
// instead of assuming success.

const runPath = (id: string) => `/api/runs/${encodeURIComponent(id)}`

function patchRun(id: string, patch: Partial<Run>) {
  queryClient.setQueryData<Run[]>(keys.runs, (rs) => rs?.map((r) => (r.run_id === id ? {...r, ...patch} : r)))
  queryClient.setQueryData<RunDetail>(keys.run(id), (r) => (r ? {...r, ...patch} : r))
}

// Marking read is instant: the dot goes at once, the request follows, and a
// failure is put right by the next read of the list.
export async function markRunRead(id: string) {
  patchRun(id, {unread: false})
  try {
    await post(`${runPath(id)}/read`)
  } catch {
    void invalidate(keys.runs)
  }
}

// The prompt is the one thing the run list leaves out (it is the bulk of that
// list and no row shows it), so the single run is asked for when it is needed.
export const fetchRun = (id: string) =>
  queryClient.fetchQuery({
    queryKey: keys.run(id),
    queryFn: ({signal}) => get<RunDetail>(runPath(id), signal),
    staleTime: 2_000,
  })

// Replaying needs the prompt too. An unanswered fetch says so instead of
// opening an editor that looks complete and has no prompt in it.
export async function replayRun(run: Run) {
  try {
    const full = await fetchRun(run.run_id)
    openTaskEditor({mode: 'replay', run: full})
  } catch (e) {
    toast.error('Could not read this run', {description: errorMessage(e)})
  }
}

function resumeQuestion(r: Run): string {
  if (!r.task) return 'The interrupted session is dropped and the run is recorded as canceled.'
  if (r.task.trigger === 'cron')
    return 'The interrupted session is dropped; the task keeps its schedule and runs again at its next occurrence.'
  return 'The interrupted session is dropped and the task leaves the queue. It will not run again.'
}

// Dropping a scheduled resume: the session is nobody's plan any more, so the
// run counts as canceled right away and can be continued by hand.
export async function cancelResume(r: Run): Promise<boolean> {
  const ok = await confirm({
    title: `Cancel the scheduled resume of "${r.task_name}"?`,
    description: resumeQuestion(r),
    confirmLabel: 'Cancel resume',
    cancelLabel: 'Keep it',
    destructive: true,
  })
  if (!ok) return false
  try {
    await post(`${runPath(r.run_id)}/cancel`)
    patchRun(r.run_id, {status: 'canceled', resume_pending: false})
    toast.success('Resume canceled')
    return true
  } catch (e) {
    toast.error('Could not cancel the resume', {description: errorMessage(e)})
    return false
  } finally {
    void invalidate(keys.runs, keys.tasks)
  }
}

export async function cancelRunning(r: Run): Promise<boolean> {
  const ok = await confirm({
    title: `Cancel the running task "${r.task_name}"?`,
    description: 'Its process is terminated. What it did so far stays in the log.',
    confirmLabel: 'Cancel task',
    cancelLabel: 'Keep running',
    destructive: true,
  })
  if (!ok) return false
  try {
    await post(`${runPath(r.run_id)}/cancel`)
    toast.success('Task canceled')
    return true
  } catch (e) {
    // 409: it finished on its own meanwhile; the refresh below shows how.
    toast.error('Could not cancel the task', {description: errorMessage(e)})
    return false
  } finally {
    void invalidate(keys.runs, keys.tasks)
  }
}

// Continue reopens the run's session in Terminal, in its provider and its
// working directory. A running run still owns its session, so it is taken
// over: the daemon stops it first and opens Terminal once it is gone.
export async function continueInTerminal(r: Run, takeOver: boolean): Promise<boolean> {
  const {isMac, terminal} = currentPlatform()
  if (
    takeOver &&
    !(await confirm({
      title: `Take over "${r.task_name}" in ${terminal}?`,
      description: `The unattended run is stopped and its chat reopens in ${terminal}, where you can write to it.`,
      confirmLabel: 'Take over',
      cancelLabel: 'Keep running',
    }))
  )
    return false
  const pending = toast.loading(takeOver ? 'Stopping the run…' : `Opening ${terminal}…`)
  try {
    await post(`${runPath(r.run_id)}/continue`)
    toast.success(isMac ? 'Terminal opened' : 'Terminal window opened', {
      id: pending,
      description: 'The session continues there.',
    })
    return true
  } catch (e) {
    toast.error('Continue failed', {id: pending, description: errorMessage(e)})
    return false
  } finally {
    if (takeOver) void invalidate(keys.runs, keys.tasks, keys.runLog(r.run_id))
  }
}
