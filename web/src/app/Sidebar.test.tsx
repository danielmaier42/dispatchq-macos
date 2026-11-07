import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {act, cleanup, fireEvent, render, screen} from '@testing-library/react'
import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import {navigate} from './nav'
import {Sidebar} from './Sidebar'

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHealth: () => ({isError: false}),
  useSettings: () => ({data: undefined}),
  useUpdate: () => ({data: undefined}),
}))
vi.mock('./badges', () => ({useBadges: () => ({notifications: 3})}))

// The window width is a media query; each test sets whether it matches, and
// resize() flips it the way widening the window would. It is installed before
// the imports run, since the theme reads matchMedia at load.
const media = vi.hoisted(() => {
  const state = {narrow: false, listeners: new Set<() => void>()}
  window.matchMedia = ((query: string) => ({
    get matches() {
      return query.includes('max-width') ? state.narrow : false
    },
    media: query,
    addEventListener: (_: string, l: () => void) => state.listeners.add(l),
    removeEventListener: (_: string, l: () => void) => state.listeners.delete(l),
  })) as unknown as typeof window.matchMedia
  return state
})
const resize = (narrow: boolean) => {
  media.narrow = narrow
  act(() => media.listeners.forEach((l) => l()))
}

beforeEach(() => navigate('dashboard'))
afterEach(cleanup)

const show = () =>
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <Sidebar />
      </TooltipProvider>
    </QueryClientProvider>,
  )

describe('Sidebar', () => {
  it('shows the full sidebar in a wide window', () => {
    media.narrow = false
    show()
    expect(screen.getByText('Search...')).toBeTruthy()
    expect(screen.queryByLabelText('Expand sidebar')).toBeNull()
  })

  it('folds to icons in a narrow window and keeps the badge', () => {
    media.narrow = true
    show()
    expect(screen.queryByText('Search...')).toBeNull()
    expect(screen.getByLabelText('Queue')).toBeTruthy()
    expect(screen.getByLabelText('Notifications').textContent).toBe('3')
  })

  it('lays the full sidebar over the view until a view is picked', () => {
    media.narrow = true
    show()
    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    expect(screen.getByText('Search...')).toBeTruthy()
    act(() => navigate('queue'))
    expect(screen.queryByText('Search...')).toBeNull()
  })

  it('puts the overlay away on Escape and when the window widens', () => {
    media.narrow = true
    show()
    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    fireEvent.keyDown(document, {key: 'Escape'})
    expect(screen.queryByLabelText('Collapse sidebar')).toBeNull()

    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    resize(false)
    expect(screen.queryByLabelText('Collapse sidebar')).toBeNull()
    expect(screen.queryByLabelText('Expand sidebar')).toBeNull()

    resize(true)
    expect(screen.queryByLabelText('Collapse sidebar')).toBeNull()
  })

  it('puts the overlay away when the current view is picked again', () => {
    media.narrow = true
    show()
    fireEvent.click(screen.getByLabelText('Expand sidebar'))
    fireEvent.click(screen.getByText('Dashboard'))
    expect(screen.queryByLabelText('Collapse sidebar')).toBeNull()
  })
})
