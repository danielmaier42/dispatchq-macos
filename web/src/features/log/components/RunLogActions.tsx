import {Pencil, RotateCcw, SquareTerminal, TimerOff, X} from 'lucide-react'
import {useState} from 'react'
import {Button} from '@/components/ui/button'
import {SheetClose} from '@/components/ui/sheet'
import {Tooltip} from '@/components/ui/tooltip'
import {useTasks} from '@/lib/queries'
import type {QueueTask, Run} from '@/lib/types'
import {openTaskEditor} from '@/features/task-editor/store'
import {cancelResume, cancelRunning, continueInTerminal, replayRun} from '../actions'
import {canContinue, canTakeOver, cancelMode} from '../runs'
import {usePlatform} from '@/lib/platform'

// The sheet's footer: call the run off, edit or run its task again, or pick its
// session up in Terminal. Continue follows the run: offered as a take-over while it
// runs, as a plain resume once it has finished for good, and hidden while it
// waits to be resumed by the queue.
export function RunLogActions({run}: {run: Run}) {
  const {terminal} = usePlatform()
  const [busy, setBusy] = useState<'' | 'cancel' | 'continue'>('')
  // Once the user has canceled, a poll answered before the cancel landed must
  // not offer the action again.
  const [canceled, setCanceled] = useState(false)
  const mode = canceled ? '' : cancelMode(run)
  const takeOver = canTakeOver(run)
  const resume = canContinue(run)
  // Edit opens the task as it is in the queue now, not the run's snapshot; a
  // deleted task has nothing left to edit (Replay still recreates it).
  const tasks = useTasks().data
  // The queue hides a one-shot task while it runs; it is still in the
  // configuration, so the run's own snapshot (with its prompt) stands in.
  const snapshot =
    run.status === 'running' && run.task?.prompt ? ({...run.task, running: true} as QueueTask) : undefined
  const task = tasks?.find((t) => t.id === run.task_id) ?? snapshot

  const cancel = async () => {
    setBusy('cancel')
    const ok = mode === 'resume' ? await cancelResume(run) : await cancelRunning(run)
    if (ok) setCanceled(true)
    setBusy('')
  }
  const cont = async () => {
    if (busy) return
    setBusy('continue')
    await continueInTerminal(run, takeOver)
    setBusy('')
  }

  return (
    <>
      {mode && (
        <Tooltip
          content={
            mode === 'resume'
              ? 'Drop the scheduled resume so this task does not start again'
              : 'Stop the running process'
          }
        >
          <Button variant="danger-ghost" onClick={cancel} loading={busy === 'cancel'} className="mr-auto">
            {mode === 'resume' ? <TimerOff /> : <X />}
            {mode === 'resume' ? 'Cancel resume' : 'Cancel task'}
          </Button>
        </Tooltip>
      )}
      <Tooltip
        content={
          task
            ? 'Open this task in the editor'
            : tasks
              ? 'This task no longer exists in the queue'
              : 'Loading the queue'
        }
      >
        {/* A disabled button gets no hover, so the span carries the tooltip. */}
        <span
          tabIndex={task ? -1 : 0}
          className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
        >
          <Button disabled={!task} onClick={() => task && openTaskEditor({mode: 'edit', task})}>
            <Pencil />
            Edit task
          </Button>
        </span>
      </Tooltip>
      {run.task && (
        <Tooltip content="Run this task again: opens a new task prefilled from this run">
          <Button onClick={() => void replayRun(run)}>
            <RotateCcw />
            Replay
          </Button>
        </Tooltip>
      )}
      {(takeOver || resume) && (
        <Tooltip
          content={
            takeOver
              ? `Stops the unattended run and reopens its chat in ${terminal}, in the task's folder, with the full conversation context`
              : `Opens ${terminal} in the task's folder and resumes this chat interactively, with the full conversation context`
          }
        >
          <Button variant="primary" onClick={cont} loading={busy === 'continue'}>
            <SquareTerminal />
            {takeOver ? `Take over in ${terminal}` : `Continue in ${terminal}`}
          </Button>
        </Tooltip>
      )}
      <SheetClose asChild>
        <Button variant={takeOver || resume ? 'secondary' : 'primary'}>Done</Button>
      </SheetClose>
    </>
  )
}
