import {Server} from 'lucide-react'
import {Section} from '@/components/layout/view-layout'
import {OverviewCard} from '@/features/dashboard/OverviewCard'
import {duration} from '@/lib/format'
import type {StatsBucket, StatsProvider} from '@/lib/types'
import {cn} from '@/lib/utils'
import {Period} from './UsageSummary'
import {bucketValue, metricDef, type Metric} from './usage'

// Several providers mean several allowances, so the split says which account
// the work went to. With one provider in the period the daemon sends none:
// nothing to split. Under each provider its models, since the model decides
// what a run costs. Bar, figure and order follow the measure chosen for the
// chart: a provider's bar is its share of all of it, a model's bar its share of
// that provider's, so a provider's models add up to 100%.
export function ProviderBreakdown({
  providers,
  period,
  metric,
}: {
  providers: StatsProvider[]
  period: string
  metric: Metric
}) {
  const busiest = <T extends StatsBucket>(rows: T[]) =>
    [...rows].sort((a, b) => bucketValue(metric, b) - bucketValue(metric, a))
  return (
    <Section>
      <OverviewCard
        icon={<Server />}
        title="By provider"
        count={<Period label={`${period}, busiest first`} />}
      >
        <div className="divide-y divide-border">
          {busiest(providers).map((p) => (
            <div key={p.id || '_'} className="py-1">
              <Row
                name={p.name || p.id || 'Not recorded'}
                bucket={p}
                metric={metric}
                share={shareOf(metric, p, providers)}
              />
              {busiest(p.models ?? []).map((m) => (
                <Row
                  key={m.model || '_'}
                  name={m.model || 'Harness default'}
                  bucket={m}
                  metric={metric}
                  share={shareOf(metric, m, p.models ?? [])}
                  nested
                />
              ))}
            </div>
          ))}
        </div>
      </OverviewCard>
    </Section>
  )
}

function shareOf(metric: Metric, b: StatsBucket, of: StatsBucket[]) {
  const total = of.reduce((n, x) => n + bucketValue(metric, x), 0)
  return total ? bucketValue(metric, b) / total : 0
}

// What the figure on the right is, said once beside it.
const UNIT: Record<Metric, string> = {tokens: 'tokens', runs: 'runs', cost: 'API-equiv.'}

function Row({
  name,
  bucket: b,
  metric,
  share,
  nested,
}: {
  name: string
  bucket: StatsBucket
  metric: Metric
  share: number
  nested?: boolean
}) {
  return (
    <div className={cn('@container/prow flex items-center gap-4', nested ? 'py-1 pr-2 pl-6' : 'px-2 py-1.5')}>
      <div className="min-w-0 flex-1">
        <div className={nested ? 'truncate font-mono text-[12.5px]' : 'truncate text-[13.5px] font-medium'}>
          {name}
        </div>
        <div className="truncate text-[12px] text-muted-foreground tabular-nums">
          {b.runs} run{b.runs === 1 ? '' : 's'} · {b.success} succeeded · {b.failed} failed ·{' '}
          {duration(b.duration_ms)}
        </div>
      </div>
      <div className="hidden w-24 shrink-0 items-center gap-2 sm:flex @2xl/prow:w-40">
        <div className="h-1.5 flex-1 overflow-hidden rounded-full bg-surface-3">
          <div
            className={nested ? 'h-full rounded-full bg-accent/50' : 'h-full rounded-full bg-accent'}
            style={{width: `${share * 100}%`}}
          />
        </div>
        <span className="w-9 text-right text-[11.5px] text-muted-foreground tabular-nums">
          {Math.round(share * 100)}%
        </span>
      </div>
      <div className="w-28 shrink-0 text-right whitespace-nowrap tabular-nums @2xl/prow:w-36">
        <span className={nested ? 'text-[13px] font-medium' : 'text-[14px] font-semibold'}>
          {metricDef(metric).fmt(bucketValue(metric, b))}
        </span>{' '}
        <span className="text-[12px] text-muted-foreground">{UNIT[metric]}</span>
      </div>
    </div>
  )
}
