import {describeCron} from '@/features/queue/schedule'
import {
  Activity,
  CalendarClock,
  CircleCheck,
  CircleX,
  Clock,
  Hourglass,
  ListPlus,
  OctagonAlert,
} from 'lucide-react'
import {navigate} from '@/app/nav'
import {type ReactNode, useState} from 'react'
import {Badge} from '@/components/ui/badge'
import {Button} from '@/components/ui/button'
import {Tooltip} from '@/components/ui/tooltip'
import {clearLogFilters, setLogFilters} from '@/features/log/filters-store'
import {openRunLog} from '@/features/log/store'
import {openAddTask, openTaskEditor} from '@/features/task-editor/store'
import {compact, fullTime, relTime, resumeAt, resumeText, runDuration, usd} from '@/lib/format'
import {useHealth, useRuns, useSettings, useStats, useTasks} from '@/lib/queries'
import type {QueueTask, Run} from '@/lib/types'
import {NO_CREDIT, useNoCredit} from './use-no-credit'
import {OverviewCard, OverviewEmpty, OverviewRow} from './OverviewCard'
import {cardOrder, type CardId, holdText, recentFailures, upNext, waitingRuns} from './overview'
import {useNow} from '@/hooks/use-now'

const SHOWN = 3
// Running now lists more before folding, and its +N more unfolds in place.
const RUNNING_SHOWN = 5

const cards: Record<CardId, () => ReactNode> = {
  failed: FailuresCard,
  next: UpNextCard,
  running: RunningCard,
  week: WeekCard,
}

// OverviewCards answer what someone opening the app wants first: what runs
// now, what starts next, what failed, and how much work went through lately. Each card is as tall as what it lists; an empty
// one is a single line rather than a box held open by its neighbour.
export function OverviewCards() {
  return (
    // Two columns only where the content area, not the window, has room for
    // them: the sidebar and the assistant panel take their share first.
    <div className="@container mb-7">
      <div className="grid items-start gap-3 @3xl:grid-cols-2">
        {cardOrder.map((id) => {
          const Tile = cards[id]
          return <Tile key={id} />
        })}
      </div>
    </div>
  )
}

// More is the count past what a card lists, as a link to the full list.
function More({n, onClick}: {n: number; onClick: () => void}) {
  if (n <= 0) return null
  return (
    <button
      type="button"
      onClick={onClick}
      className="w-fit rounded-sm px-2 pt-0.5 text-[12px] text-muted-foreground outline-none hover:text-foreground hover:underline focus-visible:ring-2 focus-visible:ring-accent/40"
    >
      +{n} more
    </button>
  )
}

// Failed runs open in the Log with only failures showing, whatever filter
// was left there before.
function showFailedRuns() {
  clearLogFilters()
  setLogFilters({show: 'failed'})
  navigate('log')
}

function RunningCard() {
  const runs = useRuns()
  const paused = !!useSettings().data?.paused
  const limitedUntil = useHealth().data?.limited_until || undefined
  const noCredit = useNoCredit()
  const now = useNow(15_000)
  const running = (runs.data ?? []).filter((r) => r.status === 'running')
  const waiting = waitingRuns(runs.data ?? [])
  const items: Run[] = [...running, ...waiting]
  const [all, setAll] = useState(false)
  const shown = all ? items : items.slice(0, RUNNING_SHOWN)
  return (
    <OverviewCard
      icon={<Activity />}
      title="Running now"
      count={
        <>
          {running.length > 0 && (
            <Badge tone="accent" dot pulse>
              {running.length}
            </Badge>
          )}
          {waiting.length > 0 && <Badge tone="warning">{waiting.length} waiting</Badge>}
          {paused && <Badge tone="warning">Paused</Badge>}
        </>
      }
      link="Log"
      onLink={() => navigate('log')}
      loading={runs.isPending}
    >
      {items.length ? (
        <>
          {shown.map((r) =>
            r.status === 'running' ? (
              <OverviewRow
                key={r.run_id}
                lead={<span className="size-2 animate-pulse-dot rounded-full bg-accent" />}
                title={r.task_name}
                meta={r.provider?.name ?? r.provider?.kind}
                trail={runDuration(r, now)}
                onClick={() => openRunLog(r.run_id)}
              />
            ) : (
              <OverviewRow
                key={r.run_id}
                lead={<Hourglass className="size-3.5 text-warning-text" />}
                title={r.task_name}
                meta={
                  <span className="text-warning-text">
                    {r.provider?.id && noCredit.has(r.provider.id) ? (
                      NO_CREDIT
                    ) : (
                      <>Rate limited, {resumeText(resumeAt(r.resume_at, limitedUntil))}</>
                    )}
                  </span>
                }
                onClick={() => openRunLog(r.run_id)}
              />
            ),
          )}
          <More n={items.length - shown.length} onClick={() => setAll(true)} />
        </>
      ) : (
        <OverviewEmpty icon={<Clock />}>
          {paused ? 'The queue is paused, nothing starts until it is resumed.' : 'Nothing is running.'}
        </OverviewEmpty>
      )}
    </OverviewCard>
  )
}

function UpNextCard() {
  const tasks = useTasks()
  const now = useNow()
  const next = upNext(tasks.data ?? [], now)
  return (
    <OverviewCard
      icon={<CalendarClock />}
      title="Up next"
      count={next.length > 0 && <Badge>{next.length}</Badge>}
      link="Queue"
      onLink={() => navigate('queue')}
      loading={tasks.isPending}
    >
      {next.length ? (
        <>
          {next.slice(0, SHOWN).map(({task, at}) => (
            <OverviewRow
              key={task.id}
              title={task.name}
              meta={<NextMeta task={task} />}
              trail={
                at ? (
                  <Tooltip content={fullTime(at)}>
                    <span>{relTime(at, now)}</span>
                  </Tooltip>
                ) : (
                  'Due now'
                )
              }
              onClick={() => openTaskEditor({mode: 'edit', task})}
            />
          ))}
          <More n={next.length - SHOWN} onClick={() => navigate('queue')} />
        </>
      ) : (
        <OverviewEmpty
          icon={<ListPlus />}
          action={
            <Button size="sm" onClick={() => openAddTask()}>
              New task
            </Button>
          }
        >
          Nothing is scheduled.
        </OverviewEmpty>
      )}
    </OverviewCard>
  )
}

const triggerText: Record<QueueTask['trigger'], string> = {
  asap: 'As soon as possible',
  fixed: 'Once',
  cron: 'Repeats',
}

function NextMeta({task}: {task: QueueTask}) {
  const hold = holdText(task)
  if (hold) return <span className={task.blocked_reason ? 'text-danger' : 'text-warning-text'}>{hold}</span>
  const when =
    task.trigger === 'cron' && task.cron ? (describeCron(task.cron) ?? task.cron) : triggerText[task.trigger]
  const parts = [when, task.pool || task.provider, task.backfill && 'backfill'].filter(Boolean)
  return <>{parts.join(' · ')}</>
}

function FailuresCard() {
  const runs = useRuns()
  const now = useNow()
  const failed = recentFailures(runs.data ?? [], now)
  return (
    <OverviewCard
      icon={<OctagonAlert />}
      title="Failed in the last 24 hours"
      count={failed.length > 0 && <Badge tone="danger">{failed.length}</Badge>}
      link="Log"
      onLink={showFailedRuns}
      loading={runs.isPending}
    >
      {failed.length ? (
        <>
          {failed.slice(0, SHOWN).map((r) => (
            <OverviewRow
              key={r.run_id}
              lead={<CircleX className="size-3.5 text-danger" />}
              title={r.task_name}
              meta={r.error || (r.status === 'auth_error' ? 'Not logged in' : 'Failed')}
              trail={relTime(r.finished_at ?? r.started_at, now)}
              onClick={() => openRunLog(r.run_id)}
            />
          ))}
          <More n={failed.length - SHOWN} onClick={showFailedRuns} />
        </>
      ) : (
        <OverviewEmpty icon={<CircleCheck className="text-success" />}>
          No failures in the last 24 hours.
        </OverviewEmpty>
      )}
    </OverviewCard>
  )
}

function WeekCard() {
  const stats = useStats()
  const w = stats.data?.last_7d
  const done = w ? w.success + w.failed : 0
  const rate = done ? Math.round((w!.success / done) * 100) : null
  const days = stats.data?.per_day.slice(-7) ?? []
  const max = Math.max(1, ...days.map((d) => d.runs))
  return (
    <OverviewCard
      icon={<Activity />}
      title="Last 7 days"
      link="Usage"
      onLink={() => navigate('usage')}
      loading={stats.isPending}
    >
      {w && w.runs ? (
        <div className="flex flex-col gap-3 px-2 pt-1">
          <div className="grid grid-cols-3 gap-3">
            <Figure label="runs" value={String(w.runs)} />
            <Figure label="succeeded" value={rate === null ? '–' : `${rate}%`} />
            <Figure label="tokens" value={compact(w.input_tokens + w.output_tokens)} sub={usd(w.cost_usd)} />
          </div>
          <div className="flex h-8 items-end gap-1" aria-hidden>
            {days.map((d) => (
              <Tooltip key={d.date} content={`${d.date}: ${d.runs} run${d.runs === 1 ? '' : 's'}`}>
                <div className="flex h-full flex-1 items-end">
                  <div
                    className="w-full rounded-t-[3px] bg-accent/70"
                    style={{
                      height: d.runs ? `${Math.max(8, (d.runs / max) * 100)}%` : '2px',
                      opacity: d.runs ? 1 : 0.35,
                    }}
                  />
                </div>
              </Tooltip>
            ))}
          </div>
        </div>
      ) : (
        <OverviewEmpty>No runs in the last 7 days.</OverviewEmpty>
      )}
    </OverviewCard>
  )
}

function Figure({label, value, sub}: {label: string; value: string; sub?: string}) {
  return (
    <div className="min-w-0">
      <div className="text-[20px] leading-7 font-semibold tracking-[-0.01em] tabular-nums">{value}</div>
      <div className="truncate text-[12px] text-muted-foreground">
        {label}
        {sub && <span className="text-subtle-foreground"> · {sub}</span>}
      </div>
    </div>
  )
}
