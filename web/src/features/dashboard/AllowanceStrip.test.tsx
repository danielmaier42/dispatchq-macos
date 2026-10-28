import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {LimitsEntry, LimitWindow} from '@/lib/types'
import {AllowanceStrip} from './AllowanceStrip'

const now = Date.parse('2026-10-28T10:00:00Z')
const limits = vi.hoisted(() => ({data: [] as LimitsEntry[]}))

vi.mock('@/lib/queries', () => ({
  useLimits: () => ({isPending: false, data: limits.data}),
  useProviders: () => ({data: []}),
  useSettings: () => ({data: undefined}),
}))

const win = (id: string, label: string, used: number): LimitWindow => ({id, label, used_percent: used})

const entry = (name: string, windows: LimitWindow[]): LimitsEntry => ({
  id: name,
  name,
  type_name: 'Claude Code',
  default: false,
  limits: {state: 'ok', windows, checked_at: '2026-10-28T09:58:00Z'},
  backfill: false,
})

const cellOf = (label: string) => screen.getByText(label).parentElement as HTMLElement

// Below 700px each window gets its own line under the provider's name; the
// card's width decides, through container queries that jsdom cannot evaluate,
// so these pin the placement each width relies on.
describe('AllowanceStrip', () => {
  afterEach(cleanup)

  const show = (entries: LimitsEntry[]) => {
    limits.data = entries
    render(
      <TooltipProvider>
        <AllowanceStrip now={now} reading={false} onRefresh={() => {}} />
      </TooltipProvider>,
    )
  }

  it('stacks both windows under the name on a narrow card and keeps their wide columns', () => {
    show([entry('claude-1', [win('five_hour', '5 hours', 7), win('week', 'Week', 30)])])
    const five = cellOf('5 hours').className
    const week = cellOf('Week').className
    expect(five).toContain('row-start-2')
    expect(five).toContain('@min-[700px]:col-start-2')
    expect(week).toContain('row-start-3')
    expect(week).toContain('@min-[700px]:col-start-3')
  })

  it('shows a lone weekly window right under the name, in the week column when wide', () => {
    show([entry('codex', [win('week', 'Week', 10)])])
    const week = cellOf('Week').className
    expect(week).toContain('row-start-2')
    expect(week).not.toContain('row-start-3')
    expect(week).toContain('@min-[700px]:col-start-3')
  })
})
