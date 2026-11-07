import {describe, expect, it} from 'vitest'
import {PERMISSIONS_DEFAULT_LABEL, PERMISSIONS_SKIP_LABEL, taskDetailRows} from './details'

const permissions = (rows: [string, string][]) => rows.find(([k]) => k === 'Permissions')?.[1]

describe('task proposal rows', () => {
  // A new agent job without a grant keeps the provider's rules; its card says
  // so, because an unattended run refuses a command those rules do not allow.
  it('names the default permissions on a new agent job', () => {
    expect(permissions(taskDetailRows({name: 'Notify', prompt: 'p'}, true))).toBe(PERMISSIONS_DEFAULT_LABEL)
    expect(permissions(taskDetailRows({kind: 'agent', prompt: 'p', permissions: 'default'}, true))).toBe(
      PERMISSIONS_DEFAULT_LABEL,
    )
  })

  it('names the bypass on a new agent job that has it', () => {
    expect(permissions(taskDetailRows({prompt: 'p', permissions: 'skip'}, true))).toBe(PERMISSIONS_SKIP_LABEL)
  })

  it('shows no permissions for a script job', () => {
    expect(
      permissions(taskDetailRows({kind: 'script', prompt: 'echo', permissions: 'skip'}, true)),
    ).toBeUndefined()
  })

  it('shows permissions on an update only when the update changes them', () => {
    expect(permissions(taskDetailRows({model: 'opus'}, false))).toBeUndefined()
    expect(permissions(taskDetailRows({permissions: 'default'}, false))).toBe(PERMISSIONS_DEFAULT_LABEL)
    expect(permissions(taskDetailRows({permissions: 'skip'}, false))).toBe(PERMISSIONS_SKIP_LABEL)
  })
})
