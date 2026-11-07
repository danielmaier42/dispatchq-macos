import {QueryClient, QueryClientProvider} from '@tanstack/react-query'
import {cleanup, render} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import {TooltipProvider} from '@/components/ui/tooltip'
import type {HostInfo} from '@/lib/types'
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

const state = vi.hoisted(() => ({host: undefined as HostInfo | undefined, notify: 'denied'}))

vi.mock('@/lib/platform', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHost: () => ({data: state.host}),
}))
vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHealth: () => ({
    data: {wake_error: '', notify_status: state.notify, limited_until: '', limited_providers: []},
    isError: false,
  }),
}))
vi.mock('@/features/whats-new/store', () => ({useWhatsNewStatus: () => ({data: undefined})}))

afterEach(cleanup)

const show = (host: HostInfo | undefined, notify: string) => {
  state.host = host
  state.notify = notify
  return (
    render(
      <QueryClientProvider client={new QueryClient()}>
        <TooltipProvider>
          <StatusBanners />
        </TooltipProvider>
      </QueryClientProvider>,
    ).container.textContent ?? ''
  )
}

const WINDOWS: HostInfo = {os: 'windows', uninstall: false}
const MAC: HostInfo = {os: 'darwin', uninstall: true}

describe('notification banner', () => {
  it('points a denied Windows user at Settings > System > Notifications', () => {
    const text = show(WINDOWS, 'denied')
    expect(text).toContain("Windows is not showing DispatchQ's notifications")
    expect(text).toContain('Settings > System > Notifications')
    expect(text).toContain('Open Settings')
    expect(text).not.toMatch(/macOS|System Settings|Alerts/)
  })

  it('asks for a reinstall when Windows registration failed', () => {
    const text = show(WINDOWS, 'not_determined')
    expect(text).toContain('Reinstall DispatchQ')
    expect(text).not.toMatch(/allowed to notify|macOS|Alerts/)
  })

  it('stays hidden on Windows while the status is unknown', () => {
    expect(show(WINDOWS, 'unknown')).not.toMatch(/notifications/)
  })

  it('keeps the macOS wording on a Mac', () => {
    const text = show(MAC, 'denied')
    expect(text).toContain("macOS is not showing DispatchQ's notifications")
    expect(text).toContain('Allow notifications')
    expect(text).toContain('Open System Settings')
    expect(text).toContain('Pick Alerts')
  })
})
