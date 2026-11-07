import {describe, expect, it} from 'vitest'
import {envLine, httpsOnly, manualBody} from './feedback'

describe('envLine', () => {
  it('joins the known parts', () => {
    expect(envLine({app_version: '1.4.0', os_version: 'macOS 15.1'})).toBe('DispatchQ 1.4.0 · macOS 15.1')
    expect(envLine({app_version: '1.4.0', os_version: 'Windows 10.0 (build 26100)'})).toBe(
      'DispatchQ 1.4.0 · Windows 10.0 (build 26100)',
    )
    expect(envLine({app_version: '1.4.0'})).toBe('DispatchQ 1.4.0')
    expect(envLine({app_version: ''})).toBe('')
    expect(envLine(undefined)).toBe('')
  })
})

describe('manualBody', () => {
  it('keeps every message, separated by a blank line', () => {
    expect(manualBody(['first', 'second'])).toBe('first\n\nsecond')
  })
})

describe('httpsOnly', () => {
  it('lets only web links through', () => {
    expect(httpsOnly('https://github.com/x/y/issues/new')).toBe('https://github.com/x/y/issues/new')
    expect(httpsOnly('javascript:alert(1)')).toBe('')
    expect(httpsOnly(undefined)).toBe('')
  })
})
