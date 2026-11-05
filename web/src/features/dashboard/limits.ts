import {relTime} from '@/lib/format'
import type {LimitWindow, LimitsEntry, ProviderLimits} from '@/lib/types'

// A window shows what is left of it, so the bar empties as it is used. It is
// coloured before it runs dry: at 0% left the provider's tasks already wait,
// and the point is to see it coming.
export function windowLeft(w: LimitWindow): number {
  return Math.min(100, Math.max(0, 100 - w.used_percent))
}

export type Level = 'ok' | 'warning' | 'danger'

export function leftLevel(left: number): Level {
  return left <= 10 ? 'danger' : left <= 25 ? 'warning' : 'ok'
}

export function resetText(w: LimitWindow, now = Date.now()): string {
  if (!w.resets_at) return ''
  const at = new Date(w.resets_at)
  if (at.getTime() <= now) return 'Resets now'
  const day = at.toLocaleDateString(undefined, {weekday: 'short'})
  const time = at.toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'})
  return `Resets ${relTime(at, now)} · ${day} ${time}`
}

// The week sits in the same column on every provider, also where an account has
// no five-hour window, so the rows can be read down a column.
export function windowSlots(windows: LimitWindow[] = []): (LimitWindow | null)[] {
  const five = windows.find((w) => w.id === 'five_hour') ?? null
  const week = windows.find((w) => w.id === 'week') ?? null
  const rest = windows.filter((w) => w !== five && w !== week)
  if (!five && !week) return rest
  return [five, week, ...rest]
}

// noLimits is an instance with no allowance to show: a harness without one, or
// an account on an API key rather than a plan. A plain fact, not a warning.
export const noLimits = (l: ProviderLimits) => l.state === 'unsupported'

// readFailed is a reading that should have worked and did not.
export const readFailed = (l: ProviderLimits) => l.state === 'unavailable'

// creditGone is a provider whose API credit balance is proven used up: a run
// was refused with "Credit balance is too low" and its gate is still closed.
// That is the only way the balance becomes known (see creditUnknown).
export const creditGone = (p: LimitsEntry) => p.blocked?.reason === 'credit_exhausted'

// creditUnknown is an API-key account whose balance nobody has proven empty:
// the balance cannot be read, so it may be ample or nearly gone.
export const creditUnknown = (p: LimitsEntry) => !!p.limits.api_key && !creditGone(p)

export const creditUnknownHint =
  'An API key spends prepaid credit, and Anthropic offers no way to read that balance without an admin key. ' +
  'DispatchQ only learns it is used up when a run is refused with "Credit balance is too low"; then this line says so.'

// blockText says what an account block means and when the provider is tried
// again; empty for a provider that is not blocked by one.
export function blockText(p: LimitsEntry, now = Date.now()): string {
  const b = p.blocked
  if (!b?.reason) return ''
  const retry = Date.parse(b.until) > now ? `tried again ${relTime(b.until, now)}` : 'tried again now'
  if (b.reason === 'credit_exhausted') {
    return `No credit left: a run was refused with "Credit balance is too low". Add credit in the Anthropic Console; ${retry}.`
  }
  return `Claude subscription access is disabled by the organisation; ${retry}.`
}

// limitStatusText says when the figures are from, or why there are none.
export function limitStatusText(l: ProviderLimits, now = Date.now()): string {
  switch (l.state) {
    case 'ok':
      return l.updated_at ? `Updated ${relTime(l.updated_at, now)}` : 'Updated'
    case 'unsupported':
      return l.reason || 'No limit'
    case 'disabled':
      return 'Switched off'
  }
  const when = l.updated_at ? ` Showing the reading from ${relTime(l.updated_at, now)}.` : ''
  return (l.reason || 'Could not be read.') + when
}

// The daemon reads every provider at once, so one age heads the strip; the
// newest reading is the one the last read produced.
export function newestReading(entries: LimitsEntry[]): string | undefined {
  let best: string | undefined
  for (const e of entries) {
    const at = e.limits.updated_at
    if (at && (!best || Date.parse(at) > Date.parse(best))) best = at
  }
  return best
}

export function readText(at: string | undefined, now = Date.now()): string {
  if (!at) return 'Refreshes every 15 min'
  // The daemon's clock may run a little ahead; a reading is never from the future.
  return `Read ${relTime(new Date(Math.min(Date.parse(at), now)), now)} · refreshes every 15 min`
}

// A row names its own age only when it lags the strip's: a reading that failed
// since, or one the last read could not renew.
export function rowAge(l: ProviderLimits, newest: string | undefined): string | undefined {
  if (!l.updated_at || !newest) return undefined
  return Date.parse(newest) - Date.parse(l.updated_at) > 120_000 ? l.updated_at : undefined
}

export const fmtWeight = (w: number) => `${Math.round(w * 100) / 100}×`
