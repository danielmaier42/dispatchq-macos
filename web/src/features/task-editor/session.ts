import {get} from '@/lib/api'
import {errorMessage} from '@/lib/api'
import {keys, queryClient} from '@/lib/queries'
import type {QueueTask, Run, Settings} from '@/lib/types'
import {toast} from '@/components/ui/toaster'
import {carriedFields, formFromTask, type TaskForm} from './form'
import type {TaskEditorRequest} from './store'

// EditorSession is one opening of the editor, resolved from its request: what
// the form starts with, and what the editor says about it.
export interface EditorSession {
  mode: TaskEditorRequest['mode']
  title: string
  description: string
  submitLabel: string
  initial: TaskForm
  carried: ReturnType<typeof carriedFields>
  // The edited task's group as it was; see buildPayload.
  keptGroup?: string
  // Task id for PUT (edit only).
  editId?: string
  // The prompt is new to this Mac and deserves a real review on open; without
  // it the editor only shows what is already known about that text.
  fresh: boolean
  dirHint?: string
  providerHint?: string
}

// resolveSession fetches what a request needs before the form can show it.
// null means the request cannot be opened; the reason was toasted.
export async function resolveSession(req: TaskEditorRequest): Promise<EditorSession | null> {
  switch (req.mode) {
    case 'add': {
      let dir = ''
      try {
        dir = (
          await queryClient.ensureQueryData<Settings>({
            queryKey: keys.settings,
            queryFn: () => get('/api/settings'),
          })
        ).default_working_dir
      } catch {
        /* no default folder: the field stays empty */
      }
      return {
        mode: 'add',
        title: 'New task',
        description: 'Queue work for Claude or a script.',
        submitLabel: 'Add task',
        initial: formFromTask({working_dir: dir, ...req.prefill}),
        carried: carriedFields(req.prefill),
        fresh: false,
      }
    }
    case 'edit': {
      // Re-read the saved task: the queue's copy can lag a save by one poll, and
      // prefilling it would show an old folder although the save succeeded.
      let t: QueueTask = req.task
      try {
        t = (await get<QueueTask[]>('/api/tasks')).find((x) => x.id === t.id) ?? t
      } catch {
        /* keep the queue's copy */
      }
      return {
        mode: 'edit',
        title: 'Edit task',
        description: t.name || t.id,
        submitLabel: 'Save changes',
        initial: formFromTask(t),
        carried: carriedFields(t),
        keptGroup: t.group ?? '',
        editId: t.id,
        fresh: false,
      }
    }
    case 'replay': {
      // The run list does not carry prompts, so the one run is fetched for it. An
      // unanswered fetch says so instead of opening an editor that looks
      // complete and has no prompt in it.
      let task = req.run.task
      try {
        task = (await get<Run>('/api/runs/' + encodeURIComponent(req.run.run_id))).task ?? task
      } catch (e) {
        toast.error('Could not read this run', {description: errorMessage(e)})
        return null
      }
      if (!task) {
        toast.error('No saved definition to replay')
        return null
      }
      return {
        mode: 'replay',
        title: 'Replay task',
        description: `A new task from the run of ${req.run.task_name || task.name}.`,
        submitLabel: 'Schedule again',
        // A replay starts outside any group, as it always has: the run's group
        // may be long gone. The Group field puts it into one.
        initial: {...formFromTask(task), group: ''},
        carried: {},
        fresh: false,
      }
    }
    case 'import': {
      const {task, missing_working_dir: missingDir, unresolved_provider: provider} = req.result
      return {
        mode: 'import',
        title: 'Import task',
        description: 'Nothing is queued until you add it. Check the prompt and the paths first.',
        submitLabel: 'Add task',
        // A group from another Mac's queue means nothing here.
        initial: {...formFromTask(task), group: ''},
        carried: {},
        // A prompt written on another Mac is new here even though nobody typed
        // it — reviewed for real once there is a folder to resolve it against.
        fresh: !missingDir,
        dirHint: missingDir
          ? `The file's working directory (${missingDir}) does not exist on this computer. Choose a folder.`
          : undefined,
        // When no single provider here matches the harness the file was written
        // for, the choice is the importer's: DispatchQ will not put someone else's
        // task on an account they did not pick.
        providerHint: provider
          ? `Written for ${provider}, which does not match exactly one provider here. Choose where it should run.`
          : undefined,
      }
    }
  }
}
