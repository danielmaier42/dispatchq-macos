import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, fireEvent, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {LimitsEntry, Run} from '@/lib/types'
import {OverviewCards} from './OverviewCards'

const runs = vi.hoisted(() => ({data: [] as Run[]}))
const limits = vi.hoisted(() => ({data: [] as LimitsEntry[]}))

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useRuns: () => ({isPending: false, data: runs.data}),
  useTasks: () => ({isPending: false, data: []}),
  useSettings: () => ({data: undefined}),
  useHealth: () => ({data: {limited_until: '2099-01-01T00:00:00Z'}}),
  useLimits: () => ({data: limits.data}),
  useStats: () => ({isPending: true, data: undefined}),
}))

const run = (n: number, status: Run['status'] = 'running'): Run =>
  ({
    run_id: `r${n}`,
    task_id: `t${n}`,
    task_name: `Job ${n}`,
    status,
    started_at: '2026-11-03T15:00:00Z',
    resume_pending: status === 'rate_limited_waiting' ? true : undefined,
  }) as Run

const show = (list: Run[], blocked: LimitsEntry[] = []) => {
  runs.data = list
  limits.data = blocked
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <OverviewCards />
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

describe('Running now', () => {
  afterEach(cleanup)

  it('lists five runs, and +N more unfolds the rest in place', () => {
    show([1, 2, 3, 4, 5, 6, 7].map((n) => run(n)))
    expect(screen.getByText('Job 5')).toBeTruthy()
    expect(screen.queryByText('Job 6')).toBeNull()
    fireEvent.click(screen.getByText('+2 more'))
    expect(screen.getByText('Job 7')).toBeTruthy()
    expect(screen.queryByText(/more$/)).toBeNull()
  })

  it('counts the runs a rate limit paused apart from the running ones', () => {
    show([run(1), run(2), run(3), run(4), run(5, 'rate_limited_waiting')])
    expect(screen.getByText('4')).toBeTruthy()
    expect(screen.getByText('1 waiting')).toBeTruthy()
    expect(screen.getByText('Job 5')).toBeTruthy()
  })

  it('says only "No credit left" for a run whose provider has no credit, with no retry time', () => {
    const paused = {...run(1, 'rate_limited_waiting'), provider: {id: 'api', kind: 'claude-code'}} as Run
    const api = {id: 'api', name: 'API', blocked: {until: '2099-01-01T00:00:00Z', reason: 'credit_exhausted'}}
    show([paused, run(2, 'rate_limited_waiting')], [api as LimitsEntry])
    const rows = screen.getAllByText(/No credit left|Rate limited/)
    expect(rows.map((r) => r.textContent)).toEqual([
      'No credit left',
      expect.stringMatching(/^Rate limited, resumes /),
    ])
  })
})
