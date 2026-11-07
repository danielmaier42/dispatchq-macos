import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it} from 'vitest'
import type {PoolRanking} from '@/lib/types'
import {UrgencyGauge} from './UrgencyGauge'

const note = (tier: 1 | 2 | 3, text: string) =>
  ({
    provider_id: 'p',
    name: 'p',
    tier,
    note: text,
    urgency: 0,
    score: 0,
    weight: 1,
    running: 0,
  }) as unknown as PoolRanking

describe('UrgencyGauge', () => {
  afterEach(cleanup)

  it('reads a used-up week in red and any other note in grey', () => {
    render(<UrgencyGauge u={note(2, 'week used up')} threshold={1} spare={false} />)
    expect(screen.getByText('week used up').className).toContain('text-danger')
    cleanup()
    render(<UrgencyGauge u={note(2, '95% of 5 hours used')} threshold={1} spare={false} />)
    expect(screen.getByText('95% of 5 hours used').className).toContain('text-muted-foreground')
  })
})
