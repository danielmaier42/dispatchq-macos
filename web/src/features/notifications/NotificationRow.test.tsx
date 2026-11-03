import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {AppNotification} from '@/lib/types'
import {NotificationRow} from './NotificationRow'

// The theme reads matchMedia when it loads, which jsdom does not have.
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

const n: AppNotification = {
  id: 'n1',
  kind: 'artifact',
  title: 'New artifact',
  message: 'The feature remains in Refinement Review.',
  task_name: 'Feature refinement-review #244810 r10 Single robots meta tag and search engine indexing',
  sent_at: '2026-11-03T16:00:00Z',
  unread: false,
}

// jsdom lays nothing out, so this pins the classes a narrow row relies on.
describe('NotificationRow', () => {
  afterEach(cleanup)

  it('keeps the title whole and lets the task name give way', () => {
    render(
      <TooltipProvider>
        <NotificationRow n={n} />
      </TooltipProvider>,
    )
    const title = screen.getByRole('button', {name: 'New artifact'})
    expect(title.className).toContain('shrink-0')
    expect(title.className).toContain('max-w-full')
    const task = screen.getByText(n.task_name!)
    expect(task.className).toContain('shrink')
    expect(task.className).toContain('truncate')
  })
})
