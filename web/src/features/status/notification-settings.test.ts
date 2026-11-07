import {afterEach, beforeEach, describe, expect, it, vi} from 'vitest'

const toast = vi.hoisted(() => Object.assign(vi.fn(), {error: vi.fn()}))
vi.mock('@/components/ui/toaster', () => ({toast}))

import type {HostInfo} from '@/lib/types'
import {notificationSettingsHint, openNotificationSettings} from './notification-settings'

const WINDOWS: HostInfo = {os: 'windows', uninstall: false}
const MAC: HostInfo = {os: 'darwin', uninstall: true}

describe('notificationSettingsHint', () => {
  it('names the real place on the system the daemon reports', () => {
    expect(notificationSettingsHint(MAC)).toBe('Open System Settings → Notifications → DispatchQ')
    expect(notificationSettingsHint(WINDOWS)).toBe('Open Settings → System → Notifications → DispatchQ')
    expect(notificationSettingsHint({os: 'linux', uninstall: false})).toMatch(/notification settings/)
  })

  // A Windows browser or a spoofed user agent must not decide this: only the
  // daemon knows which system's settings apply, and until it answers the hint
  // names no system.
  it('stays generic until the host has answered', () => {
    expect(notificationSettingsHint(undefined)).toBe(
      "Open your system's notification settings and allow DispatchQ",
    )
  })
})

describe('openNotificationSettings', () => {
  beforeEach(() => {
    toast.mockClear()
    toast.error.mockClear()
  })
  afterEach(() => {
    delete window.cqOpenNotificationSettings
    delete window.cqOpenExternal
    vi.restoreAllMocks()
  })

  it('asks the app window to open the settings and passes it nothing', async () => {
    const open = vi.fn().mockResolvedValue(undefined)
    window.cqOpenNotificationSettings = open
    await expect(openNotificationSettings(WINDOWS)).resolves.toBe('opened')
    expect(open).toHaveBeenCalledTimes(1)
    expect(open).toHaveBeenCalledWith()
    expect(toast).not.toHaveBeenCalled()
    expect(toast.error).not.toHaveBeenCalled()
  })

  // The Windows bug: the old code handed a macOS-only URL to cqOpenExternal,
  // which the Windows window drops, so the click did nothing at all.
  it('never routes through the external-link hook', async () => {
    const external = vi.fn()
    window.cqOpenExternal = external
    window.cqOpenNotificationSettings = vi.fn().mockResolvedValue(undefined)
    await openNotificationSettings(WINDOWS)
    expect(external).not.toHaveBeenCalled()
  })

  it('shows the OS refusal together with the way there by hand', async () => {
    // webview rejects a bound call with the Go error's text, a plain string.
    window.cqOpenNotificationSettings = vi
      .fn()
      .mockRejectedValue('could not open Settings > System > Notifications: Access is denied.')
    await expect(openNotificationSettings(WINDOWS)).resolves.toBe('failed')
    expect(toast.error).toHaveBeenCalledWith(
      'Could not open the notification settings: could not open Settings > System > Notifications: Access is denied.',
      {description: 'Open Settings → System → Notifications → DispatchQ'},
    )
  })

  it('says where to go when there is no window hook (browser tab, Windows)', async () => {
    await expect(openNotificationSettings(WINDOWS)).resolves.toBe('manual')
    expect(toast).toHaveBeenCalledWith('Open Settings → System → Notifications → DispatchQ')
  })

  it('says where to go when there is no window hook (browser tab, macOS)', async () => {
    await expect(openNotificationSettings(MAC)).resolves.toBe('manual')
    expect(toast).toHaveBeenCalledWith('Open System Settings → Notifications → DispatchQ')
  })

  it('treats an app window without the hook as unsupported, not as a no-op', async () => {
    window.cqOpenExternal = vi.fn()
    await expect(openNotificationSettings(WINDOWS)).resolves.toBe('manual')
    expect(toast).toHaveBeenCalledTimes(1)
  })
})
