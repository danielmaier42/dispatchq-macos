import type {LimitedProvider, NotifyStatus} from '@/lib/types'

export const SUDOERS_COMMAND =
  'echo "$(whoami) ALL=(root) NOPASSWD: /usr/bin/pmset" | sudo tee /etc/sudoers.d/dispatchq'

// A wake error about passwords or permissions means pmset needs a sudoers
// entry; anything else is a plain failure to report as it is.
export function wakeNeedsSudoers(err: string): boolean {
  return /password|sudo|not permitted|permission/i.test(err)
}

// macOS can accept every notification DispatchQ posts and still show none of
// them: an app whose authorization is denied (or never granted) posts into the
// void. That is invisible from the app's side, so the banner says it out loud.
export function notificationsBlocked(
  status: NotifyStatus | undefined,
): status is 'denied' | 'not_determined' {
  return status === 'denied' || status === 'not_determined'
}

// The gate is open once its reopen time has passed, even before the next
// health poll says so.
export function activeLimit(until: string | undefined, now: number): Date | null {
  if (!until) return null
  const d = new Date(until)
  return Number.isNaN(d.getTime()) || d.getTime() <= now ? null : d
}

export type LimitCoverage = 'none' | 'all' | 'some'

// A provider with a fallback is not holding its tasks up: they run on the
// substitute right now. So the banner says what is true for this set of
// blocked providers: all of them covered, none, or some.
export function limitCoverage(providers: LimitedProvider[]): LimitCoverage {
  const covered = providers.filter((p) => p.fallback).length
  if (covered === 0) return 'none'
  return covered === providers.length ? 'all' : 'some'
}

const noCredit = (p: LimitedProvider) => p.reason === 'credit_exhausted'

export interface LimitSplit {
  // When the last used-up credit balance is tried again; '' when no provider
  // is paused for credit.
  creditUntil: string
  // The providers that wait for an ordinary limit to reset.
  rateLimited: LimitedProvider[]
  // When the queue reopens for the rate-limited ones: the earliest of their
  // own times, as the daemon's gate does; '' when none waits for a limit.
  until: string
}

const times = (providers: LimitedProvider[]) =>
  providers.map((p) => Date.parse(p.until)).filter((t) => !Number.isNaN(t))

// splitLimits keeps a used-up credit balance out of the rate-limit banner: its
// retries are not news, so neither its provider nor its retry time feed the
// headline or the reopen time. A provider without a time of its own falls back
// to the daemon's gate time.
export function splitLimits(providers: LimitedProvider[], until: string): LimitSplit {
  const credit = providers.filter(noCredit)
  if (!credit.length) return {creditUntil: '', rateLimited: providers, until}
  const rateLimited = providers.filter((p) => !noCredit(p))
  const c = times(credit)
  const r = times(rateLimited)
  return {
    creditUntil: c.length ? new Date(Math.max(...c)).toISOString() : until,
    rateLimited,
    until: r.length ? new Date(Math.min(...r)).toISOString() : rateLimited.length ? until : '',
  }
}

export function limitHeading(providers: LimitedProvider[]): string {
  if (!providers.length) return 'Rate limit reached: the queue is waiting, not stuck'
  const names = providers.map((p) => p.name + (p.fallback ? ' → ' + p.fallback : ''))
  return 'Rate limit reached, waiting on ' + names.join(', ')
}
