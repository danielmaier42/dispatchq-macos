import {CornerDownRight, FileText, RotateCcw, TimerOff} from 'lucide-react'
import {useState, type MouseEvent, type ReactNode} from 'react'
import {Button} from '@/components/ui/button'
import {Tooltip} from '@/components/ui/tooltip'
import {relTime, resumeAt, resumeText} from '@/lib/format'
import type {Run} from '@/lib/types'
import {cn} from '@/lib/utils'
import {cancelResume, replayRun} from '../actions'
import {useNow} from '@/hooks/use-now'
import {runMetrics, runTimeTip} from '../metrics'
import {errorLine, runFolder} from '../runs'
import {openRunLog} from '../store'
import {StatusPill} from './StatusPill'

// RunRow is one run in the Log. A click anywhere opens its log (which marks it
// read); the title is the keyboard target. The figures on the right give way
// to the row's actions on hover, so a row stays calm until it is pointed at;
// in a narrow row they move under the title instead.
export function RunRow({
  run,
  showProvider,
  limitedUntil,
  depth = 0,
}: {
  run: Run
  // Which harness a run used is only worth saying once there is more than one
  // it could have been; a script run always says so.
  showProvider: boolean
  limitedUntil?: string
  depth?: number
}) {
  const running = run.status === 'running'
  const now = useNow(1000, running)
  const metrics = runMetrics(run, now)
  const folder = runFolder(run)
  const p = run.provider
  const provider = p?.name && (showProvider || p.kind === 'script') ? p : undefined
  const error = errorLine(run)

  return (
    <div
      onClick={() => openRunLog(run.run_id)}
      className="group/row @container/run relative flex min-h-12 cursor-pointer items-center gap-3 px-3 py-2 transition-colors hover:bg-surface-2/70"
    >
      {depth > 0 && (
        <CornerDownRight
          className="-mr-1 size-3.5 shrink-0 text-subtle-foreground"
          style={{marginLeft: (depth - 1) * 16}}
        />
      )}
      <div className="min-w-0 flex-1">
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation()
            openRunLog(run.run_id)
          }}
          className={cn(
            'block max-w-full truncate rounded-sm text-left text-[13px] outline-none focus-visible:ring-2 focus-visible:ring-accent/40',
            'font-medium',
          )}
        >
          {run.task_name}
        </button>
        <div className="flex min-w-0 items-center gap-1.5 truncate text-[12px] text-muted-foreground">
          <Tooltip content={[runTimeTip(run, now), ...metrics.detail].join('\n')}>
            <span className="shrink-0 tabular-nums">{relTime(run.started_at, now)}</span>
          </Tooltip>
          {error ? (
            <Meta>
              <Tooltip content={run.error}>
                <span className="text-[color-mix(in_oklab,var(--danger)_80%,var(--foreground))]">
                  {error}
                </span>
              </Tooltip>
            </Meta>
          ) : (
            <>
              {folder && (
                <Meta>
                  <span className="font-mono text-[11.5px]">{folder}</span>
                </Meta>
              )}
              {provider && (
                <Meta>
                  {provider.name}
                  {provider.model && <span className="ml-1 font-mono text-[11.5px]">{provider.model}</span>}
                  {provider.pool && (
                    <span className="ml-1 text-subtle-foreground">via pool {provider.pool}</span>
                  )}
                </Meta>
              )}
              {run.resume_pending && (
                <Meta>
                  <span className="font-medium text-[color-mix(in_oklab,var(--warning)_80%,var(--foreground))]">
                    {resumeText(resumeAt(run.resume_at, limitedUntil))}
                  </span>
                </Meta>
              )}
            </>
          )}
        </div>
        {/* A narrow row keeps its title and gives the figures a line of their own. */}
        {metrics.short.length > 0 && (
          <div className="truncate text-[12px] text-subtle-foreground tabular-nums @2xl/run:hidden">
            {metrics.short.join(' · ')}
          </div>
        )}
      </div>

      <div className="grid shrink-0 items-center justify-items-end">
        <span className="col-start-1 row-start-1 hidden text-[12px] text-subtle-foreground tabular-nums transition-opacity group-focus-within/row:opacity-0 group-hover/row:opacity-0 @2xl/run:block">
          {metrics.short.join(' · ')}
        </span>
        <RowActions run={run} />
      </div>
      <StatusPill run={run} className="w-[104px] justify-center" />
    </div>
  )
}

function Meta({children}: {children: ReactNode}) {
  return (
    <>
      <span className="text-subtle-foreground" aria-hidden>
        ·
      </span>
      <span className="min-w-0 truncate">{children}</span>
    </>
  )
}

function RowActions({run}: {run: Run}) {
  const [canceling, setCanceling] = useState(false)
  const stop = (fn: () => void) => (e: MouseEvent) => {
    e.stopPropagation()
    fn()
  }
  return (
    <div className="col-start-1 row-start-1 flex items-center gap-0.5 opacity-0 transition-opacity group-focus-within/row:opacity-100 group-hover/row:opacity-100">
      {run.resume_pending && (
        <Tooltip content="Drop the scheduled resume so this task does not start again">
          <Button
            variant="danger-ghost"
            size="sm"
            loading={canceling}
            onClick={stop(async () => {
              setCanceling(true)
              await cancelResume(run)
              setCanceling(false)
            })}
          >
            <TimerOff />
            Cancel resume
          </Button>
        </Tooltip>
      )}
      {run.task && (
        <Tooltip content="Run this task again: opens a new task prefilled from this run">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Replay"
            onClick={stop(() => void replayRun(run))}
          >
            <RotateCcw />
          </Button>
        </Tooltip>
      )}
      <Tooltip content="Open the log">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label="Open the log"
          onClick={stop(() => openRunLog(run.run_id))}
        >
          <FileText />
        </Button>
      </Tooltip>
    </div>
  )
}
