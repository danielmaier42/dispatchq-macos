import {describe, expect, it} from 'vitest'
import {executionLabel} from '@/lib/providers'
import {distroOptions, executionPayload} from './execution-logic'
import {linuxConfigDir} from './provider-logic'

describe('execution', () => {
  it('sends nothing for a Windows provider', () => {
    expect(executionPayload('native', 'Ubuntu', 'dm')).toBeUndefined()
    expect(executionPayload('wsl', 'Ubuntu', ' dm ')).toEqual({type: 'wsl', distro: 'Ubuntu', user: 'dm'})
  })

  it('labels only providers that run elsewhere', () => {
    expect(executionLabel(undefined)).toBe('')
    expect(executionLabel({})).toBe('')
    expect(executionLabel({type: ''})).toBe('')
    expect(executionLabel({type: 'wsl', distro: 'Ubuntu-24.04'})).toBe('Linux · Ubuntu-24.04')
    expect(executionLabel({type: 'wsl', distro: 'Ubuntu', user: 'alice'})).toBe('Linux · Ubuntu · alice')
  })

  it('lists the default distribution first and marks WSL 1', () => {
    const opts = distroOptions([
      {name: 'Debian', default: false, state: 'Stopped', version: 1},
      {name: 'Ubuntu', default: true, state: 'Running', version: 2},
    ])
    expect(opts.map((o) => o.value)).toEqual(['Ubuntu', 'Debian'])
    expect(opts[0].label).toBe('Ubuntu (default)')
    expect(opts[1].description).toBe('WSL 1, providers need WSL 2 · Stopped')
  })

  it('offers the Linux configuration directories as placeholders', () => {
    expect(linuxConfigDir('claude-code')).toBe('~/.claude')
    expect(linuxConfigDir('codex')).toBe('~/.codex')
  })
})
