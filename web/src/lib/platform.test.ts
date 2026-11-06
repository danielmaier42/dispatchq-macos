import {describe, expect, it} from 'vitest'
import {platformWords} from './platform'

describe('platformWords', () => {
  it('keeps the macOS wording for macOS and for an unknown platform', () => {
    for (const p of ['darwin', '', undefined]) {
      const w = platformWords(p)
      expect(w.isMac).toBe(true)
      expect(w.machine).toBe('this Mac')
      expect(w.mod).toBe('⌘')
    }
  })

  it('uses neutral words elsewhere', () => {
    const w = platformWords('linux')
    expect(w.isMac).toBe(false)
    expect(w.machine).toBe('this computer')
    expect(w.mod).toBe('Ctrl+')
    expect(w.fileManager).toBe('the file manager')
    // Ctrl+N and Ctrl+R need a native window.
    expect(w.appShortcuts).toBe(false)
  })
})
