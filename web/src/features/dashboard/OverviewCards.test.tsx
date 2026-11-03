import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, fireEvent, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {Run} from '@/lib/types'
import {OverviewCards} from './OverviewCards'

const runs = vi.hoisted(() => ({data: [] as Run[]}))

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useRuns: () => ({isPending: false, data: runs.data}),
  useTasks: () => ({isPending: false, data: []}),
  useSettings: () => ({data: undefined}),
  useHealth: () => ({data: undefined}),
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

const show = (list: Run[]) => {
  runs.data = list
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
})
