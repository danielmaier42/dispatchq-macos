import {describe, expect, it} from 'vitest'
import type {LimitsEntry, ProviderLimits} from '@/lib/types'
import {
  blockText,
  byName,
  creditGone,
  creditText,
  leftLevel,
  limitStatusText,
  newestReading,
  noLimits,
  readFailed,
  readText,
  rowAge,
  windowSlots,
} from './limits'

const entry = (limits: ProviderLimits): LimitsEntry => ({
  id: 'p',
  name: 'p',
  type_name: 'Claude',
  default: false,
  limits,
  backfill: false,
})

describe('limits', () => {
  it('colours a window before it runs dry', () => {
    expect(leftLevel(60)).toBe('ok')
    expect(leftLevel(25)).toBe('warning')
    expect(leftLevel(10)).toBe('danger')
  })
  it('keeps the week in the second column without a five-hour window', () => {
    const week = {id: 'week', label: 'Week', used_percent: 10}
    expect(windowSlots([week])).toEqual([null, week])
  })
  it('a failed read names its reason and the age of what is shown', () => {
    const t = Date.parse('2026-10-19T12:00:00Z')
    const l = {
      state: 'unavailable' as const,
      reason: 'Token expired.',
      updated_at: '2026-10-19T11:50:00Z',
      checked_at: '',
    }
    expect(limitStatusText(l, t)).toBe('Token expired. Showing the reading from 10 min ago.')
    expect(readFailed(l)).toBe(true)
  })
  it('an API-key account has an unknown credit balance, which is no failure', () => {
    const l: ProviderLimits = {
      state: 'unsupported',
      reason: "Credit balance unknown: an API key's balance cannot be read",
      api_key: true,
      checked_at: '',
    }
    expect(noLimits(l)).toBe(true)
    expect(readFailed(l)).toBe(false)
    expect(limitStatusText(l)).toBe("Credit balance unknown: an API key's balance cannot be read")
    expect(creditText(entry(l))).toBe("Credit balance unknown: an API key's balance cannot be read")
    expect(creditGone(entry(l))).toBe(false)
    expect(blockText(entry(l))).toBe('')
  })
  it('a refused run proves the credit balance used up', () => {
    const t = Date.parse('2026-11-05T22:00:00Z')
    const p = {
      ...entry({state: 'unsupported', api_key: true, checked_at: ''}),
      blocked: {until: '2026-11-05T23:00:00Z', reason: 'credit_exhausted' as const},
    }
    expect(creditGone(p)).toBe(true)
    expect(blockText(p, t)).toBe('No credit left')
  })
  it('a bare billing refusal does not claim the credit is gone', () => {
    const t = Date.parse('2026-11-05T22:00:00Z')
    const p = {
      ...entry({state: 'unsupported', api_key: true, checked_at: ''}),
      blocked: {until: '2026-11-05T23:00:00Z', reason: 'billing_refused' as const},
    }
    expect(creditGone(p)).toBe(false)
    expect(blockText(p, t)).toMatch(/^Billing refused: the credit is used up or a spend limit was reached/)
    expect(blockText(p, t)).toMatch(/press Read now\.$/)
  })
  it('names the latest credit proof of an API-key account', () => {
    const t = Date.parse('2026-11-05T22:00:00Z')
    const base = entry({state: 'unsupported', api_key: true, checked_at: ''})
    expect(
      creditText({...base, credit: {status: 'ok', checked_at: '2026-11-05T19:00:00Z', source: 'ping'}}, t),
    ).toBe('Credit OK · the ping answered 3 h ago.')
    expect(
      creditText(
        {...base, credit: {status: 'credit_exhausted', checked_at: '2026-11-05T21:50:00Z', source: 'run'}},
        t,
      ),
    ).toBe('No credit left · a run was refused 10 min ago.')
    expect(
      creditText(
        {
          ...base,
          credit: {
            status: 'ok',
            checked_at: '2026-11-04T22:00:00Z',
            source: 'ping',
            ping_failed: 'no network',
          },
        },
        t,
      ),
    ).toMatch(/Last ping failed: no network$/)
    expect(creditText(entry({state: 'ok', checked_at: ''}), t)).toBe('')
  })
  it('an ordinary limit is no account block', () => {
    const p = {...entry({state: 'ok', checked_at: ''}), blocked: {until: '2026-11-05T23:00:00Z'}}
    expect(blockText(p)).toBe('')
    expect(creditGone(p)).toBe(false)
  })
  it('heads the strip with the newest reading and lets only lagging rows name their own', () => {
    const t = Date.parse('2026-10-19T12:00:00Z')
    const fresh = {state: 'ok' as const, updated_at: '2026-10-19T11:57:00Z', checked_at: ''}
    const old = {state: 'unavailable' as const, updated_at: '2026-10-19T09:00:00Z', checked_at: ''}
    const newest = newestReading([entry(old), entry(fresh), entry({state: 'unsupported', checked_at: ''})])
    expect(newest).toBe(fresh.updated_at)
    expect(readText(newest, t)).toBe('Read 3 min ago · refreshes every 15 min')
    expect(readText(undefined, t)).toBe('Refreshes every 15 min')
    expect(readText('2026-10-19T12:00:20Z', t)).toBe('Read just now · refreshes every 15 min')
    expect(rowAge(fresh, newest)).toBeUndefined()
    expect(rowAge(old, newest)).toBe(old.updated_at)
  })
})

describe('byName', () => {
  const p = (id: string, name: string) => ({id, name})

  it('ignores case and accents and reads digits as numbers', () => {
    const sorted = byName([p('1', 'codex'), p('2', 'Claude 10'), p('3', 'claude 2'), p('4', 'Ärger')])
    expect(sorted.map((e) => e.name)).toEqual(['Ärger', 'claude 2', 'Claude 10', 'codex'])
  })

  it('breaks a tie by id so equal names never swap, and falls back to the id without a name', () => {
    expect(byName([p('b', 'Same'), p('a', 'same'), p('zed', '')]).map((e) => e.id)).toEqual(['a', 'b', 'zed'])
  })

  it('leaves the input untouched', () => {
    const input = [p('b', 'B'), p('a', 'A')]
    byName(input)
    expect(input.map((e) => e.id)).toEqual(['b', 'a'])
  })
})
