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

// Below 860px each window gets its own line under the provider's name; the
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
    expect(five).toContain('@min-[860px]:col-start-2')
    expect(week).toContain('row-start-3')
    expect(week).toContain('@min-[860px]:col-start-3')
  })

  it('shows a lone weekly window right under the name, in the week column when wide', () => {
    show([entry('codex', [win('week', 'Week', 10)])])
    const week = cellOf('Week').className
    expect(week).toContain('row-start-2')
    expect(week).not.toContain('row-start-3')
    expect(week).toContain('@min-[860px]:col-start-3')
  })

  it('lists the providers alphabetically by name, whatever order the configuration has', () => {
    show([
      entry('codex', []),
      entry('Claude 10', []),
      {...entry('claude 2', []), id: 'b'},
      entry('Ärger', []),
      {...entry('Claude 2', []), id: 'a'},
    ])
    const names = ['codex', 'Claude 10', 'claude 2', 'Claude 2', 'Ärger']
    const lines = names.map((n) => screen.getByText(n))
    const order = [...lines].sort((a, b) =>
      a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1,
    )
    expect(order.map((el) => el.textContent)).toEqual(['Ärger', 'Claude 2', 'claude 2', 'Claude 10', 'codex'])
  })
})
