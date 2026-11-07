import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, fireEvent, render, screen} from '@testing-library/react'
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
vi.mock('@/lib/platform', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHost: () => ({data: {os: 'windows', uninstall: false}}),
}))
vi.mock('@/features/whats-new/store', () => ({useWhatsNewStatus: () => ({data: undefined})}))

describe('notification banner settings button', () => {
  afterEach(() => {
    cleanup()
    delete window.cqOpenNotificationSettings
    delete window.cqOpenExternal
  })

  it('opens the OS notification settings through the window hook', () => {
    const open = vi.fn().mockResolvedValue(undefined)
    const external = vi.fn()
    window.cqOpenNotificationSettings = open
    window.cqOpenExternal = external
    health.data = {wake_error: '', notify_status: 'denied', limited_until: '', limited_providers: []}
    render(
      <QueryClientProvider client={new QueryClient()}>
        <TooltipProvider>
          <StatusBanners />
        </TooltipProvider>
      </QueryClientProvider>,
    )
    fireEvent.click(screen.getByRole('button', {name: 'Open Settings'}))
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith()
    expect(external).not.toHaveBeenCalled()
  })
})
