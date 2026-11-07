import {Check, CircleAlert, PenLine} from 'lucide-react'
import {useState} from 'react'
import {Button} from '@/components/ui/button'
import type {AssistantAction, AssistantTaskDraft} from '@/lib/types'
import {cn} from '@/lib/utils'
import {canEdit, confirmCard, editCard} from '../actions'
import {taskDetailRows} from '../details'
import {setCard, type Card} from '../store'

// What each proposal is called on its card and on its confirm button.
function heading(a: AssistantAction): {title: string; confirm: string; danger?: boolean} {
  const name = a.label || a.id || ''
  switch (a.type) {
    case 'add_task':
      return {title: `New task: ${name}`, confirm: 'Add to queue'}
    case 'update_task':
      return {title: `Change “${name}”`, confirm: 'Apply'}
    case 'run_now':
      return {title: `Run “${name}” now`, confirm: 'Run now'}
    case 'enable_task':
      return {title: `Enable “${name}”`, confirm: 'Enable'}
    case 'disable_task':
      return {title: `Pause “${name}”`, confirm: 'Pause'}
    case 'delete_task':
      return {title: `Delete “${name}”`, confirm: 'Delete', danger: true}
    case 'cancel_run':
      return {title: `Cancel the run of ${name}`, confirm: 'Cancel run', danger: true}
    case 'pause_all':
      return {title: 'Pause all runs', confirm: 'Pause all'}
    case 'resume_all':
      return {title: 'Resume all runs', confirm: 'Resume'}
    case 'run_cli':
      return {title: capitalize(a.label || 'Run a dispatchq command'), confirm: 'Run', danger: a.danger}
    default:
      return {title: a.type, confirm: 'Confirm'}
  }
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

const doneText: Partial<Record<AssistantAction['type'], string>> = {
  add_task: 'Added to the queue',
  update_task: 'Changed',
  run_now: 'Started',
  enable_task: 'Enabled',
  disable_task: 'Paused',
  delete_task: 'Deleted',
  cancel_run: 'Canceled',
  pause_all: 'All runs paused',
  resume_all: 'Runs resumed',
  run_cli: 'Done',
}

// ActionCard is a proposal from the assistant. Nothing happens until it is
// confirmed here; a task proposal can also go to the task editor first.
export function ActionCard({entryId, index, card}: {entryId: number; index: number; card: Card}) {
  const a = card.action
  const h = heading(a)
  const open = card.state === 'pending' || card.state === 'busy' || card.state === 'failed'

  return (
    <div
      className={cn(
        'rounded-xl border bg-surface shadow-xs transition-opacity',
        h.danger ? 'border-danger/30' : 'border-border',
        !open && 'opacity-70',
      )}
    >
      <div className="px-3 pt-2.5 pb-2">
        <div className="text-[13px] font-semibold">{h.title}</div>
        {a.task && <TaskDetails draft={a.task} full={a.type === 'add_task'} />}
        {a.command && (
          <code className="mt-1.5 block rounded-md bg-surface-2 px-2 py-1.5 font-mono text-[11.5px] break-all whitespace-pre-wrap text-foreground/85">
            {a.command}
          </code>
        )}
        {a.problem && (
          <p className="mt-1.5 flex items-start gap-1.5 text-[12px] text-warning-text">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0 text-warning" />
            {a.problem}
          </p>
        )}
        {card.state === 'failed' && card.error && (
          <p className="mt-1.5 flex items-start gap-1.5 text-[12px] text-danger">
            <CircleAlert className="mt-0.5 size-3.5 shrink-0" />
            {card.error}
          </p>
        )}
      </div>
      {card.state === 'done' && card.output && (
        <pre className="mx-3 mb-2 max-h-48 overflow-auto rounded-md bg-surface-2 px-2 py-1.5 font-mono text-[11.5px] whitespace-pre-wrap text-foreground/85">
          {card.output}
        </pre>
      )}
      <div className="flex items-center gap-1.5 border-t border-border px-2.5 py-2">
        {open ? (
          <>
            <Button
              size="sm"
              variant={h.danger ? 'danger' : 'primary'}
              disabled={!!a.problem}
              loading={card.state === 'busy'}
              onClick={() => void confirmCard(entryId, index, card)}
            >
              {h.confirm}
            </Button>
            {canEdit(a) && (
              <Button
                size="sm"
                variant="secondary"
                disabled={card.state === 'busy'}
                onClick={() => void editCard(entryId, index, card)}
              >
                <PenLine /> Edit
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="ml-auto"
              disabled={card.state === 'busy'}
              onClick={() => setCard(entryId, index, {state: 'dismissed'})}
            >
              Dismiss
            </Button>
          </>
        ) : (
          <span className="flex items-center gap-1.5 px-1 text-[12px] text-muted-foreground">
            {card.state === 'done' && <Check className="size-3.5 text-success" />}
            {card.state === 'done'
              ? (doneText[a.type] ?? 'Done')
              : card.state === 'replaced'
                ? 'Replaced by a newer proposal'
                : 'Dismissed'}
          </span>
        )}
      </div>
    </div>
  )
}

// TaskDetails shows what a task proposal sets: the whole job for a new one,
// just the changed fields for an update.
function TaskDetails({draft, full}: {draft: AssistantTaskDraft; full: boolean}) {
  const [expanded, setExpanded] = useState(false)
  const rows = taskDetailRows(draft, full)

  return (
    <div className="mt-1.5 space-y-1.5">
      {rows.length > 0 && (
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-[12px]">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-subtle-foreground">{k}</dt>
              <dd className="min-w-0 truncate" title={v}>
                {v}
              </dd>
            </div>
          ))}
        </dl>
      )}
      {draft.prompt && (
        <button
          type="button"
          onClick={() => setExpanded((e) => !e)}
          title={expanded ? 'Show less' : 'Show all'}
          className={cn(
            'block w-full rounded-md bg-surface-2 px-2 py-1.5 text-left text-[12px] whitespace-pre-wrap text-foreground/85',
            !expanded && 'line-clamp-3',
          )}
        >
          {draft.prompt}
        </button>
      )}
    </div>
  )
}
