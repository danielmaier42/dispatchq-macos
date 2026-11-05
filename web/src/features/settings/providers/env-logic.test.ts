import {describe, expect, it} from 'vitest'
import {envPayload, envProblem, envRowsFor, isSecretName, newEnvRow} from './env-logic'

describe('envPayload', () => {
  it('keeps a stored value by sending no value, and sends what was typed', () => {
    const rows = envRowsFor(['ANTHROPIC_API_KEY', 'ANTHROPIC_BASE_URL'])
    rows[1].value = 'https://gw.example'
    expect(envPayload(rows)).toEqual([
      {name: 'ANTHROPIC_API_KEY'},
      {name: 'ANTHROPIC_BASE_URL', value: 'https://gw.example'},
    ])
  })
  it('drops empty rows and trims names', () => {
    const row = newEnvRow('  FOO ')
    row.value = 'x'
    expect(envPayload([newEnvRow(), row])).toEqual([{name: 'FOO', value: 'x'}])
  })
})

describe('envProblem', () => {
  it('accepts a clean list', () => {
    expect(envProblem(envRowsFor(['A', 'B_2']))).toBe('')
    const typed = newEnvRow('FOO')
    typed.value = 'x'
    expect(envProblem([typed])).toBe('')
  })
  it('names the first problem', () => {
    expect(envProblem([newEnvRow('1BAD')])).toContain('1BAD')
    expect(envProblem(envRowsFor(['A', 'A']))).toContain('listed twice')
    expect(envProblem([newEnvRow('NEW')])).toContain('needs a value')
    const nameless = newEnvRow()
    nameless.value = 'x'
    expect(envProblem([nameless])).toContain('name')
  })
})

describe('isSecretName', () => {
  it('flags credentials but not plain settings', () => {
    expect(isSecretName('ANTHROPIC_API_KEY')).toBe(true)
    expect(isSecretName('ANTHROPIC_AUTH_TOKEN')).toBe(true)
    expect(isSecretName('ANTHROPIC_BASE_URL')).toBe(false)
  })
})
