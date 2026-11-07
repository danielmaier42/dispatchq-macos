import {afterEach, describe, expect, it, vi} from 'vitest'
import {
  enterKey,
  isApplePlatform,
  isMacHost,
  keyCombo,
  menuKeyCombo,
  modifierKey,
  withShortcut,
} from './platform'

const onPlatform = (name: string) => vi.spyOn(navigator, 'platform', 'get').mockReturnValue(name)

afterEach(() => {
  vi.restoreAllMocks()
  delete window.cqOpenExternal
})

describe('keyboard labels', () => {
  it('name the Command key on a Mac', () => {
    onPlatform('MacIntel')
    expect(isApplePlatform()).toBe(true)
    expect(keyCombo('K')).toBe('⌘K')
    expect(modifierKey()).toBe('⌘')
    expect(enterKey()).toBe('↵')
  })

  it('name Control and Enter on Windows', () => {
    onPlatform('Win32')
    expect(isApplePlatform()).toBe(false)
    expect(keyCombo('K')).toBe('Ctrl+K')
    expect(keyCombo('1')).toBe('Ctrl+1')
    expect(modifierKey()).toBe('Ctrl')
    expect(enterKey()).toBe('Enter')
  })

  it('prefer the client hints platform where the browser has it', () => {
    onPlatform('MacIntel')
    Object.defineProperty(navigator, 'userAgentData', {value: {platform: 'Windows'}, configurable: true})
    try {
      expect(keyCombo('S')).toBe('Ctrl+S')
    } finally {
      delete (navigator as {userAgentData?: unknown}).userAgentData
    }
  })

  it('offer menu-bar shortcuts only in the macOS app window', () => {
    onPlatform('MacIntel')
    expect(menuKeyCombo('N')).toBe('')
    window.cqOpenExternal = () => {}
    expect(menuKeyCombo('N')).toBe('⌘N')
    onPlatform('Win32')
    expect(menuKeyCombo('N')).toBe('')
  })

  it('leave a label bare without a combo', () => {
    expect(withShortcut('New task form', '')).toBe('New task form')
    expect(withShortcut('Search', 'Ctrl+K')).toBe('Search (Ctrl+K)')
  })
})

describe('isMacHost', () => {
  it('holds only once the daemon says darwin', () => {
    expect(isMacHost(undefined)).toBe(false)
    expect(isMacHost({os: 'windows', uninstall: false})).toBe(false)
    expect(isMacHost({os: 'darwin', uninstall: true})).toBe(true)
  })
})
