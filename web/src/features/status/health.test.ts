import {describe, expect, it} from 'vitest'
import {
  activeLimit,
  limitCoverage,
  limitHeading,
  notificationsBlocked,
  splitLimits,
  wakeNeedsSudoers,
} from './health'

describe('wakeNeedsSudoers', () => {
  it('recognises permission failures', () => {
    expect(wakeNeedsSudoers('sudo: a password is required')).toBe(true)
    expect(wakeNeedsSudoers('Operation not permitted')).toBe(true)
    expect(wakeNeedsSudoers('pmset: exit status 1')).toBe(false)
  })
})

describe('notificationsBlocked', () => {
  it('flags denied and undecided only', () => {
    expect(notificationsBlocked('denied')).toBe(true)
    expect(notificationsBlocked('not_determined')).toBe(true)
    expect(notificationsBlocked('authorized')).toBe(false)
    expect(notificationsBlocked('')).toBe(false)
  })
})

describe('activeLimit', () => {
  const now = Date.parse('2026-10-19T12:00:00Z')
  it('is null when open, past or invalid', () => {
    expect(activeLimit('', now)).toBeNull()
    expect(activeLimit('2026-10-19T11:59:00Z', now)).toBeNull()
    expect(activeLimit('nonsense', now)).toBeNull()
  })
  it('returns the reopen time while it is ahead', () => {
    expect(activeLimit('2026-10-19T13:00:00Z', now)?.toISOString()).toBe('2026-10-19T13:00:00.000Z')
  })
})

describe('limitCoverage and heading', () => {
  const a = {name: 'Claude', until: ''}
  const b = {name: 'Codex', until: '', fallback: 'Claude'}
  it('tells none, all and some apart', () => {
    expect(limitCoverage([])).toBe('none')
    expect(limitCoverage([a])).toBe('none')
    expect(limitCoverage([b])).toBe('all')
    expect(limitCoverage([a, b])).toBe('some')
  })
  it('names providers and their fallbacks', () => {
    expect(limitHeading([a, b])).toBe('Rate limit reached, waiting on Claude, Codex → Claude')
    expect(limitHeading([])).toMatch(/waiting, not stuck/)
  })
  it('keeps a used-up credit balance out of the rate-limit banner', () => {
    const key = {...b, name: 'API', until: '2026-10-19T12:05:00Z', reason: 'credit_exhausted' as const}
    const one = {...a, until: '2026-10-19T13:00:00Z'}
    const two = {...b, until: '2026-10-19T14:00:00Z'}
    expect(splitLimits([key], key.until)).toEqual({
      creditUntil: '2026-10-19T12:05:00.000Z',
      rateLimited: [],
      until: '',
    })
    // The rate limit keeps its own, earliest reopen time and outlives the credit retry.
    expect(splitLimits([two, key, one], key.until)).toEqual({
      creditUntil: '2026-10-19T12:05:00.000Z',
      rateLimited: [two, one],
      until: '2026-10-19T13:00:00.000Z',
    })
    expect(splitLimits([a, key], key.until).until).toBe(key.until)
    expect(splitLimits([one, b], 'x')).toEqual({creditUntil: '', rateLimited: [one, b], until: 'x'})
  })
})
