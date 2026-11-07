import {cleanup, fireEvent, render, screen} from '@testing-library/react'
import {afterEach, describe, expect, it, vi} from 'vitest'
import type {HostInfo, Settings} from '@/lib/types'
import {NotificationsSection} from './NotificationsSection'

// The theme reads matchMedia when it loads, which jsdom does not have.
vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia
})

const state = vi.hoisted(() => ({host: undefined as HostInfo | undefined, notify: 'authorized'}))

vi.mock('@/lib/platform', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHost: () => ({data: state.host}),
}))
vi.mock('@/lib/queries', async (importOriginal) => ({
  ...(await importOriginal<object>()),
  useHealth: () => ({data: {notify_status: state.notify}}),
}))
vi.mock('../components/Channels', () => ({Channels: () => null}))
vi.mock('../components/parts', () => ({SaveMode: () => null}))

afterEach(cleanup)

const show = (host: HostInfo | undefined, notify = 'authorized') => {
  state.host = host
  state.notify = notify
  const s = {} as Settings
  return render(<NotificationsSection value={s} stored={s} />).container.textContent ?? ''
}

describe('NotificationsSection copy', () => {
  it('keeps the Alerts advice on macOS', () => {
    const text = show({os: 'darwin', uninstall: true})
    expect(text).toContain('On this computer')
    expect(text).toContain('Keep alerts on screen')
    expect(text).toContain('pick Alerts, not Banners')
  })

  it('names neither the Mac nor Alerts on Windows', () => {
    const text = show({os: 'windows', uninstall: false})
    expect(text).toContain('On this computer')
    expect(text).toContain('Windows decides how long a notification stays on screen')
    expect(text).toContain('Settings')
    expect(text).not.toMatch(/\bMac\b|macOS|Alerts|Banners|System Settings/)
  })

  it('words the blocked warning for Windows', () => {
    const text = show({os: 'windows', uninstall: false}, 'denied')
    expect(text).toContain("Windows is not showing DispatchQ's notifications")
    expect(text).toContain('Settings > System > Notifications')
    expect(text).toContain('Open Settings')
    expect(text).not.toMatch(/macOS|System Settings|Pick Alerts/)
  })

  it('asks for a reinstall when the Windows registration failed', () => {
    const text = show({os: 'windows', uninstall: false}, 'not_determined')
    expect(text).toContain('Reinstall DispatchQ')
    expect(text).not.toMatch(/allowed to notify|macOS/)
  })

  it('keeps the macOS blocked warning on a Mac', () => {
    const text = show({os: 'darwin', uninstall: true}, 'denied')
    expect(text).toContain("macOS is not showing DispatchQ's notifications")
    expect(text).toContain('Turn Allow notifications back on')
    expect(text).toContain('Pick Alerts')
    expect(text).toContain('Open System Settings')
  })
})

describe('NotificationsSection settings button', () => {
  afterEach(() => {
    delete window.cqOpenNotificationSettings
    delete window.cqOpenExternal
  })

  it('opens the OS notification settings through the window hook', () => {
    const open = vi.fn().mockResolvedValue(undefined)
    const external = vi.fn()
    window.cqOpenNotificationSettings = open
    window.cqOpenExternal = external
    show({os: 'windows', uninstall: false})
    fireEvent.click(screen.getByRole('button', {name: /^Settings/}))
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith()
    expect(external).not.toHaveBeenCalled()
  })

  it('opens them from the blocked-notifications alert too', () => {
    const open = vi.fn().mockResolvedValue(undefined)
    window.cqOpenNotificationSettings = open
    show({os: 'darwin', uninstall: true}, 'denied')
    fireEvent.click(screen.getByRole('button', {name: 'Open System Settings'}))
    expect(open).toHaveBeenCalledTimes(1)
  })
})
