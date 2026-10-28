import {AlertTriangle, RefreshCw} from 'lucide-react'
import {openSettings} from '@/app/nav'
import {ProviderLogo} from '@/components/domain/provider-logo'
import {Button} from '@/components/ui/button'
import {Card} from '@/components/ui/card'
import {Meter} from '@/components/ui/progress'
import {Skeleton} from '@/components/ui/skeleton'
import {Tooltip} from '@/components/ui/tooltip'
import {backfillThreshold} from '@/features/settings/providers/backfill'
import {UrgencyGauge} from '@/features/settings/providers/UrgencyGauge'
import {fullTime, relTime} from '@/lib/format'
import {useLimits, useProviders, useSettings} from '@/lib/queries'
import type {LimitWindow, LimitsEntry} from '@/lib/types'
import {cn} from '@/lib/utils'
import {
  leftLevel,
  limitStatusText,
  newestReading,
  readFailed,
  readText,
  resetText,
  rowAge,
  windowLeft,
  windowSlots,
} from './limits'

const valueTone = {ok: 'text-foreground', warning: 'text-warning-text', danger: 'text-danger'}
const meterTone = {ok: 'accent', warning: 'warning', danger: 'danger'} as const
// One template for every row, so the windows line up down a column. The card
// is the size container: below 700px the four columns no longer fit, so the
// windows leave the provider's line and each takes a line of its own under it,
// instead of meter, share and reset running into each other.
const row =
  'grid grid-cols-[minmax(6rem,1fr)_minmax(0,auto)] items-center gap-x-5 gap-y-1.5 @min-[700px]:grid-cols-[minmax(140px,1fr)_minmax(0,1.5fr)_minmax(0,1.5fr)_180px]'
const head = 'col-start-1 row-start-1'
const gauge = 'col-start-2 row-start-1 @min-[700px]:col-start-4'
// slot places a window by its column on a wide card and by its order among the
// windows shown on a narrow one, so a provider with only a weekly window shows
// it first below its name instead of after an empty line.
const slot = {
  first: {
    2: 'col-span-full row-start-2 @min-[700px]:col-span-1 @min-[700px]:col-start-2 @min-[700px]:row-start-1',
    3: 'col-span-full row-start-2 @min-[700px]:col-span-1 @min-[700px]:col-start-3 @min-[700px]:row-start-1',
  },
  second: {
    3: 'col-span-full row-start-3 @min-[700px]:col-span-1 @min-[700px]:col-start-3 @min-[700px]:row-start-1',
  },
}
const bothWindows =
  'col-span-full row-start-2 @min-[700px]:col-span-2 @min-[700px]:col-start-2 @min-[700px]:row-start-1'

// AllowanceStrip is how much of each enabled provider's allowance is left,
// one line per provider, at the top of the Dashboard because it decides what
// can run, with each provider's urgency against the backfill threshold. The
// daemon reads it without spending any; the threshold is set in Settings →
// Providers.
export function AllowanceStrip({
  now,
  reading,
  onRefresh,
}: {
  now: number
  reading: boolean
  onRefresh: () => void
}) {
  const limits = useLimits()
  const providers = useProviders().data ?? []
  const kindOf = (id: string) => providers.find((p) => p.id === id)?.kind ?? ''
  const shown = (limits.data ?? []).filter((p) => p.limits.state !== 'disabled')
  const newest = newestReading(shown)
  const threshold = backfillThreshold(useSettings().data)

  return (
    <Card className="@container mb-4 overflow-hidden">
      <div className="flex items-center gap-2 border-b border-border py-1.5 pr-2 pl-4">
        <h2 className="text-[13px] font-semibold">Allowance</h2>
        <Tooltip
          content={newest ? `Read ${fullTime(newest)}, without spending any` : 'Read without spending any'}
        >
          <span className="truncate text-[12px] text-muted-foreground">{readText(newest, now)}</span>
        </Tooltip>
        <Tooltip content="Ask every provider for its allowance again. Readings younger than half a minute are reused.">
          <Button variant="ghost" size="sm" className="ml-auto" onClick={onRefresh} disabled={reading}>
            <RefreshCw className={cn(reading && 'animate-spin')} />
            {reading ? 'Reading…' : 'Read now'}
          </Button>
        </Tooltip>
      </div>
      {limits.isPending ? (
        <div className="divide-y divide-border">
          {[0, 1].map((i) => (
            <div key={i} className={cn(row, 'px-4 py-2.5')}>
              <Skeleton className={cn(head, 'h-4 w-32')} />
              <Skeleton className={cn(slot.first[2], 'h-3')} />
              <Skeleton className={cn(slot.second[3], 'h-3')} />
            </div>
          ))}
        </div>
      ) : !shown.length ? (
        <div className="flex items-center gap-2 px-4 py-2.5 text-[12.5px] text-muted-foreground">
          No provider is enabled.
          <Button variant="link" size="sm" onClick={() => openSettings('providers')}>
            Open Settings → Providers
          </Button>
        </div>
      ) : (
        <div className="divide-y divide-border">
          {shown.map((p) => (
            <ProviderLine
              key={p.id}
              p={p}
              kind={kindOf(p.id)}
              now={now}
              newest={newest}
              threshold={threshold}
            />
          ))}
        </div>
      )}
    </Card>
  )
}

function ProviderLine({
  p,
  kind,
  now,
  newest,
  threshold,
}: {
  p: LimitsEntry
  kind: string
  now: number
  newest?: string
  threshold: number
}) {
  const l = p.limits
  const failed = readFailed(l)
  const [five, week] = windowSlots(l.windows)
  const hasWindows = !!(five || week)
  const weekSlot = five ? slot.second[3] : slot.first[3]
  const age = rowAge(l, newest)
  return (
    <div className={cn(row, 'min-h-10 px-4 py-2')}>
      <div className={cn(head, 'flex min-w-0 items-center gap-2')}>
        <ProviderLogo kind={kind} className="size-4" />
        <Tooltip
          content={[p.type_name, l.plan, p.default && 'default'].filter(Boolean).join(' · ') || p.name}
        >
          <span className="truncate text-[13px] font-medium" tabIndex={0}>
            {p.name}
          </span>
        </Tooltip>
        <div className="shrink-0 text-[11.5px] text-subtle-foreground tabular-nums @min-[700px]:ml-auto">
          {failed && hasWindows ? (
            <Tooltip content={limitStatusText(l, now)}>
              <span className="inline-flex items-center gap-1 text-warning-text" tabIndex={0}>
                <AlertTriangle className="size-3.5" />
                {age ? relTime(age, now) : 'Read failed'}
              </span>
            </Tooltip>
          ) : (
            age && (
              <Tooltip content={`Read ${fullTime(age)}`}>
                <span tabIndex={0}>{relTime(age, now)}</span>
              </Tooltip>
            )
          )}
        </div>
      </div>
      {hasWindows ? (
        <>
          <WindowCell w={five} now={now} dim={failed} className={slot.first[2]} />
          <WindowCell w={week} now={now} dim={failed} className={weekSlot} />
        </>
      ) : (
        // Without windows the reason takes their place: muted where there is
        // simply nothing to show, in warning colour only for a failed read.
        <p
          className={cn(
            bothWindows,
            'truncate text-[12.5px]',
            failed ? 'text-warning-text' : 'text-muted-foreground',
          )}
        >
          {limitStatusText(l, now)}
        </p>
      )}
      <div className={cn(gauge, 'flex min-w-0 justify-end')}>
        {p.urgency && hasWindows && (
          <UrgencyGauge
            u={p.urgency}
            threshold={threshold}
            spare={p.backfill}
            barClassName="hidden w-12 @min-[250px]:block @min-[700px]:w-20"
          />
        )}
      </div>
    </div>
  )
}

// WindowCell is one allowance window on a single line: its name, a meter of
// what is left, the share left and when it resets.
function WindowCell({
  w,
  now,
  dim,
  className,
}: {
  w: LimitWindow | null
  now: number
  dim: boolean
  className: string
}) {
  if (!w) return null
  const left = windowLeft(w)
  const level = leftLevel(left)
  const reset = !w.resets_at
    ? ''
    : Date.parse(w.resets_at) <= now
      ? 'resets now'
      : `resets ${relTime(w.resets_at, now)}`
  return (
    <Tooltip
      content={[`${w.label}: ${Math.round(left)}% left`, resetText(w, now)].filter(Boolean).join(' · ')}
    >
      <div
        tabIndex={0}
        className={cn(
          className,
          'flex min-w-0 items-center gap-2 rounded-sm text-[12px] outline-none focus-visible:ring-2 focus-visible:ring-accent/40',
          dim && 'opacity-60',
        )}
      >
        <span className="w-12 shrink-0 truncate text-muted-foreground">{w.label}</span>
        <Meter value={left} tone={meterTone[level]} className="min-w-8 flex-1" />
        <span className={cn('shrink-0 font-semibold tabular-nums', valueTone[level])}>
          {Math.round(left)}%<span className="font-normal text-muted-foreground"> left</span>
        </span>
        {reset && (
          <span className="min-w-0 truncate text-[11.5px] text-subtle-foreground tabular-nums">{reset}</span>
        )}
      </div>
    </Tooltip>
  )
}
