import type {LimitedProvider, NotifyStatus} from '@/lib/types'

export const SUDOERS_COMMAND =
  'echo "$(whoami) ALL=(root) NOPASSWD: /usr/bin/pmset" | sudo tee /etc/sudoers.d/dispatchq'

export const NOTIFICATION_SETTINGS_URL =
  'x-apple.systempreferences:com.apple.Notifications-Settings.extension'

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

// A used-up credit balance is named as such: it does not reset on its own like
// a limit, it lifts once credit is added.
export function limitHeading(providers: LimitedProvider[]): string {
  if (!providers.length) return 'Rate limit reached: the queue is waiting, not stuck'
  const names = providers.map(
    (p) =>
      p.name +
      (noCredit(p) && !providers.every(noCredit) ? ' (no credit left)' : '') +
      (p.fallback ? ' → ' + p.fallback : ''),
  )
  if (providers.every(noCredit)) return 'No API credit left on ' + names.join(', ')
  return 'Rate limit reached, waiting on ' + names.join(', ')
}

// anyNoCredit says whether the banner must explain that a provider waits for
// credit, not for a reset.
export const anyNoCredit = (providers: LimitedProvider[]) => providers.some(noCredit)
