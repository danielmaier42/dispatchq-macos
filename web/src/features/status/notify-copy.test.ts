import {describe, expect, it} from 'vitest'
import {notifyCopy} from './notify-copy'

const MAC_WORDS = /\bMac\b|macOS|Alerts|Banners|System Settings/

describe('notifyCopy', () => {
  it('keeps the macOS wording on a Mac', () => {
    const c = notifyCopy({os: 'darwin', uninstall: true})
    expect(c.mac).toBe(true)
    expect(c.settingsLabel).toBe('System Settings')
    expect(c.blockedTitle('denied')).toBe("macOS is not showing DispatchQ's notifications")
    expect(c.lingerDescription).toContain('pick Alerts, not Banners')
  })

  it('sends a denied Windows user to Settings and mentions policy', () => {
    const c = notifyCopy({os: 'windows', uninstall: false})
    expect(c.mac).toBe(false)
    expect(c.settingsLabel).toBe('Settings')
    expect(c.blockedTitle('denied')).toBe("Windows is not showing DispatchQ's notifications")
    expect(c.blockedBody('denied')).toContain('Settings > System > Notifications')
    expect(c.blockedBody('denied')).toContain('group policy')
  })

  it('asks for a reinstall when the Windows registration is broken, not for permission', () => {
    const c = notifyCopy({os: 'windows', uninstall: false})
    expect(c.blockedTitle('not_determined')).toContain('registered')
    expect(c.blockedBody('not_determined')).toContain('Reinstall DispatchQ')
    expect(c.blockedBody('not_determined')).not.toMatch(/allow|permission/i)
  })

  it('uses no macOS words off a Mac or before the daemon answered', () => {
    for (const host of [{os: 'windows', uninstall: false}, {os: 'linux', uninstall: false}, undefined]) {
      const c = notifyCopy(host)
      const all = [
        c.settingsLabel,
        c.lingerLabel,
        c.lingerDescription,
        c.blockedTitle('denied'),
        c.blockedTitle('not_determined'),
        c.blockedBody('denied'),
        c.blockedBody('not_determined'),
      ].join(' ')
      expect(all).not.toMatch(MAC_WORDS)
    }
  })
})
