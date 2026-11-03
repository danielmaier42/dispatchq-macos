import {useState, type KeyboardEvent} from 'react'
import {cn} from '@/lib/utils'
import {axisLabels, bucketTitle, narrowSkips, type ChartBucket, type ChartUnit} from './chart'
import {METRICS, niceTicks, type MetricDef} from './usage'

const PLOT_H = 168

// UsageChart is one column per day, or per week once the range is long, for
// the chosen measure. Hovering or arrowing onto a column shows all three
// measures for it, so switching the measure is only needed to compare the
// shape. An empty column has no bar but keeps its place and its tooltip.
export function UsageChart({
  buckets,
  unit,
  metric,
}: {
  buckets: ChartBucket[]
  unit: ChartUnit
  metric: MetricDef
}) {
  const [active, setActive] = useState<number | null>(null)
  const values = buckets.map(metric.value)
  const ticks = niceTicks(Math.max(0, ...values), metric.integer)
  const top = ticks[ticks.length - 1]
  const labels = axisLabels(buckets, unit)
  const count = buckets.length
  // A hovered column from a longer range must not point past the new one.
  const at = active !== null && active < count ? active : null

  const onKey = (e: KeyboardEvent) => {
    const last = count - 1
    const from = at ?? last
    const to =
      e.key === 'ArrowLeft'
        ? from - 1
        : e.key === 'ArrowRight'
          ? from + 1
          : e.key === 'Home'
            ? 0
            : e.key === 'End'
              ? last
              : null
    if (to === null) return
    e.preventDefault()
    setActive(Math.max(0, Math.min(last, to)))
  }

  const narrowHidden = narrowSkips(labels)

  return (
    <div className="@container/chart flex gap-3">
      <div
        className="relative w-12 shrink-0 text-right text-[11px] text-subtle-foreground tabular-nums"
        style={{height: PLOT_H}}
      >
        {ticks.map((t) => (
          <span key={t} className="absolute right-0 translate-y-1/2" style={{bottom: `${(t / top) * 100}%`}}>
            {metric.fmt(t)}
          </span>
        ))}
      </div>
      <div className="min-w-0 flex-1">
        <div
          role="group"
          aria-label={`${metric.label} per ${unit}. Use the arrow keys to read each ${unit}.`}
          tabIndex={0}
          onKeyDown={onKey}
          onFocus={() => setActive((a) => a ?? count - 1)}
          onBlur={() => setActive(null)}
          onPointerLeave={() => setActive(null)}
          className="relative rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
          style={{height: PLOT_H}}
        >
          {ticks.map((t) => (
            <div
              key={t}
              className="absolute inset-x-0 h-px bg-border"
              style={{bottom: `${(t / top) * 100}%`}}
            />
          ))}
          <div className="absolute inset-0 flex">
            {buckets.map((b, i) => {
              const v = values[i]
              return (
                <div
                  key={b.date}
                  onPointerEnter={() => setActive(i)}
                  className={cn(
                    'relative flex h-full flex-1 items-end justify-center rounded-sm',
                    at === i && 'bg-surface-3/50',
                  )}
                >
                  {v > 0 && (
                    <div
                      className={cn(
                        'w-[58%] max-w-6 rounded-t-[4px] bg-accent transition-[height,filter] duration-300',
                        at === i && 'brightness-110',
                      )}
                      style={{height: `max(2px, ${(v / top) * 100}%)`}}
                    />
                  )}
                </div>
              )
            })}
          </div>
          {at !== null && (
            <BucketTooltip bucket={buckets[at]} unit={unit} index={at} count={count} metric={metric} />
          )}
        </div>
        <div className="relative mt-2 h-4" aria-hidden>
          {labels.map(
            (l, i) =>
              l && (
                <span
                  key={buckets[i].date}
                  className={cn(
                    'absolute top-0 text-[10.5px] whitespace-nowrap text-subtle-foreground',
                    at === i && 'font-medium text-foreground',
                    narrowHidden[i] && '@max-2xl/chart:invisible',
                  )}
                  style={{
                    left: `${((i + 0.5) / count) * 100}%`,
                    transform: `translateX(${anchor(i, count)})`,
                  }}
                >
                  {l}
                </span>
              ),
          )}
        </div>
      </div>
    </div>
  )
}

// anchor keeps a box centred on its column, but pinned to its side near the
// plot's edges so it never leaves the plot.
function anchor(index: number, count: number): string {
  const center = (index + 0.5) / count
  if (count > 2 && center < 0.12) return '0%'
  if (count > 2 && center > 0.88) return '-100%'
  return '-50%'
}

function BucketTooltip({
  bucket,
  unit,
  index,
  count,
  metric,
}: {
  bucket: ChartBucket
  unit: ChartUnit
  index: number
  count: number
  metric: MetricDef
}) {
  const rows = [metric, ...METRICS.filter((m) => m.id !== metric.id)]
  return (
    <div
      role="status"
      className="pointer-events-none absolute top-0 z-10 min-w-36 animate-in rounded-md border border-border bg-surface px-2.5 py-2 text-[12px] whitespace-nowrap shadow-md"
      style={{
        left: `${((index + 0.5) / count) * 100}%`,
        transform: `translate(${anchor(index, count)}, -8px)`,
      }}
    >
      <div className="mb-1 font-medium text-muted-foreground">{bucketTitle(bucket, unit)}</div>
      {rows.map((m, i) => (
        <div key={m.id} className="flex items-baseline justify-between gap-4">
          <span className="text-muted-foreground">{m.label}</span>
          <span
            className={cn('tabular-nums', i === 0 ? 'font-semibold text-foreground' : 'text-foreground/85')}
          >
            {m.fmt(m.value(bucket))}
          </span>
        </div>
      ))}
    </div>
  )
}
