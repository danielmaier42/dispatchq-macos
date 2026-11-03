import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {Run} from '@/lib/types'
import {RunRow} from './RunRow'

// The theme reads matchMedia when it loads, which jsdom does not have.
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

const run: Run = {
  run_id: 'r1',
  task_id: 't1',
  task_name: 'Feature refinement-review #244810 r10 Single robots meta tag',
  started_at: '2026-11-03T15:00:00Z',
  finished_at: '2026-11-03T15:09:54Z',
  status: 'success',
  exit_code: 0,
  log_path: '',
  input_tokens: 100000,
  output_tokens: 36203,
  duration_ms: 594000,
  unread: false,
}

// The row is its own size container; jsdom cannot evaluate container queries,
// so this pins that the figures have a place on each side of the threshold.
describe('RunRow', () => {
  afterEach(cleanup)

  it('shows the figures beside a wide row and under the title of a narrow one', () => {
    const {container} = render(
      <TooltipProvider>
        <RunRow run={run} showProvider={false} />
      </TooltipProvider>,
    )
    expect((container.firstElementChild as HTMLElement).className).toContain('@container/run')
    const figures = screen.getAllByText(/tok/)
    expect(figures).toHaveLength(2)
    const [under, beside] = figures
    expect(under.className).toContain('@2xl/run:hidden')
    expect(beside.className).toContain('hidden')
    expect(beside.className).toContain('@2xl/run:block')
    expect(under.textContent).toBe(beside.textContent)
  })
})
