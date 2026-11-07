import {navigate} from '@/app/nav'
import {openArtifact} from '@/features/artifacts/store'
import {openRunLog} from '@/features/log/store'
import {setPaused} from '@/features/settings/actions'
import {openAddTask, openTaskEditor} from '@/features/task-editor/store'
import {del, errorMessage, get, post, put} from '@/lib/api'
import {invalidate, keys, queryClient} from '@/lib/queries'
import type {AssistantAction, AssistantCLIResult, AssistantTaskDraft, QueueTask, TaskInput} from '@/lib/types'
import {setCard, type Card} from './store'

const NAVIGATION = new Set(['open_view', 'open_run', 'open_artifact', 'open_task'])

export const isNavigation = (a: AssistantAction) => NAVIGATION.has(a.type)

const taskPath = (id: string) => `/api/tasks/${encodeURIComponent(id)}`

// freshTask reads the task as the daemon has it now, not as the list last
// polled it: a proposal may be confirmed minutes after it was made.
async function freshTask(id: string): Promise<QueueTask> {
  const tasks = await get<QueueTask[]>('/api/tasks')
  queryClient.setQueryData(keys.tasks, tasks)
  const t = tasks.find((x) => x.id === id)
  if (!t) throw new Error('The task is no longer in the queue.')
  return t
}

// runNavigation shows what an answer pointed at. It only opens things.
export async function runNavigation(a: AssistantAction) {
  const id = a.id ?? ''
  switch (a.type) {
    case 'open_view':
      if (a.view) navigate(a.view)
      return
    case 'open_run':
      navigate('log')
      openRunLog(id)
      return
    case 'open_artifact':
      navigate('artifacts')
      openArtifact(id)
      return
    case 'open_task':
      navigate('queue')
      try {
        openTaskEditor({mode: 'edit', task: await freshTask(id)})
      } catch {
        // Gone since the answer was written: the queue it opened says enough.
      }
  }
}

// draftInput turns the assistant's draft into the body the task form would
// send. Only what the draft sets is included; a switch set to false is set.
function draftInput(d: AssistantTaskDraft): Partial<TaskInput> {
  const out: Partial<TaskInput> = {}
  for (const [k, v] of Object.entries(d))
    if (v !== undefined && v !== null && v !== '' && !(Array.isArray(v) && v.length === 0))
      (out as Record<string, unknown>)[k] = v
  return out
}

// merged is a task with an update_task draft applied, as PUT expects it whole.
// It follows `dispatchq edit`: a task runs on a provider or a pool, so naming
// one leaves the other, and turning a job into a script drops what only a
// model has.
function merged(t: QueueTask, d: AssistantTaskDraft): QueueTask {
  const next = {...t, ...draftInput(d)} as QueueTask
  if (d.provider) delete next.pool
  if (d.pool) delete next.provider
  if (d.kind === 'script') {
    delete next.provider
    delete next.pool
    delete next.model
    delete next.reasoning_effort
    next.permissions = 'default'
    next.backfill = false
  }
  return next
}

// confirmCard carries out a proposal the operator confirmed.
export async function confirmCard(entryId: number, index: number, card: Card) {
  setCard(entryId, index, {state: 'busy', error: undefined})
  try {
    const output = await apply(card.action)
    setCard(entryId, index, {state: 'done', output})
  } catch (e) {
    setCard(entryId, index, {state: 'failed', error: errorMessage(e)})
  }
}

// apply carries out one proposal; a run_cli returns what the command printed.
async function apply(a: AssistantAction): Promise<string | undefined> {
  const id = a.id ?? ''
  switch (a.type) {
    case 'add_task': {
      await post(
        '/api/tasks',
        draftInput({permissions: 'default', ...a.task} as AssistantTaskDraft) as TaskInput,
      )
      void invalidate(keys.tasks, keys.groups)
      return
    }
    case 'update_task': {
      const t = await freshTask(id)
      await put(taskPath(id), merged(t, a.task ?? {}))
      void invalidate(keys.tasks, keys.groups)
      return
    }
    case 'run_now':
      await post(`${taskPath(id)}/run-now`)
      void invalidate(keys.tasks, keys.runs)
      return
    case 'enable_task':
    case 'disable_task':
      await post(`${taskPath(id)}/${a.type === 'enable_task' ? 'enable' : 'disable'}`)
      void invalidate(keys.tasks)
      return
    case 'delete_task':
      await del(taskPath(id))
      void invalidate(keys.tasks, keys.groups)
      return
    case 'cancel_run':
      await post(`/api/runs/${encodeURIComponent(id)}/cancel`)
      void invalidate(keys.runs, keys.tasks)
      return
    case 'pause_all':
    case 'resume_all':
      await setPaused(a.type === 'pause_all')
      return
    case 'run_cli': {
      // The daemon checks the command against the CLI reference again and
      // runs it as a program, never through a shell.
      const r = await post<AssistantCLIResult>('/api/assistant/cli', {argv: a.argv ?? []})
      // A command can change anything the app shows.
      void queryClient.invalidateQueries()
      return r.output
    }
  }
}

// canEdit says whether a proposal can be opened in the task editor instead of
// being confirmed as it stands.
export const canEdit = (a: AssistantAction) => a.type === 'add_task' || a.type === 'update_task'

// editCard opens a task proposal in the task editor, where the operator can
// change it and the prompt review sees it before it is saved.
export async function editCard(entryId: number, index: number, card: Card) {
  const a = card.action
  try {
    if (a.type === 'add_task') openAddTask(draftInput(a.task ?? {}))
    else if (a.type === 'update_task' && a.id)
      openTaskEditor({mode: 'edit', task: merged(await freshTask(a.id), a.task ?? {})})
    // What happens next is the editor's business; the card has done its part.
    setCard(entryId, index, {state: 'dismissed'})
  } catch (e) {
    setCard(entryId, index, {state: 'failed', error: errorMessage(e)})
  }
}
