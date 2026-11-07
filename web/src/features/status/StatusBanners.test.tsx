import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import {StatusBanners} from './StatusBanners'

// The theme reads matchMedia when it loads, which jsdom does not have.
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

const health = vi.hoisted(() => ({data: {} as object}))

vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHealth: () => ({data: health.data, isError: false}),
}))
vi.mock('@/features/whats-new/store', () => ({useWhatsNewStatus: () => ({data: undefined})}))

const PAST = '2000-01-01T00:00:00Z'
const FUTURE = '2099-01-01T00:00:00Z'

const show = (providers: object[], until: string) => {
  health.data = {
    wake_error: '',
    notify_status: 'authorized',
    limited_until: until,
    limited_providers: providers,
  }
  render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <StatusBanners />
      </TooltipProvider>
    </QueryClientProvider>,
  )
}

describe('limit banners', () => {
  afterEach(cleanup)

  it('shows no bar for a used-up credit balance, as for a used-up week', () => {
    show([{name: 'API', until: FUTURE, fallback: 'Claude', reason: 'credit_exhausted'}], FUTURE)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('announces a rate limit beside a credit-held provider without naming the latter', () => {
    show(
      [
        {name: 'API', until: FUTURE, reason: 'credit_exhausted'},
        {name: 'Codex', until: FUTURE},
      ],
      FUTURE,
    )
    expect(screen.getByRole('status').textContent).toMatch(/^Rate limit reached, waiting on Codex/)
    expect(screen.getByRole('status').textContent).not.toMatch(/API/)
  })

  it('keeps a rate limit on another provider after the credit retry time has passed', () => {
    show(
      [
        {name: 'API', until: PAST, reason: 'credit_exhausted'},
        {name: 'Codex', until: FUTURE},
      ],
      PAST,
    )
    expect(screen.queryByText('No credit left')).toBeNull()
    expect(screen.getByRole('status').textContent).toMatch(/^Rate limit reached, waiting on Codex/)
  })
})
