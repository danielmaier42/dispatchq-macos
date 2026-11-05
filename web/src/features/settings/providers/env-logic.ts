import type {ProviderEnvInput, ProviderKind} from '@/lib/types'

// One line of the environment editor. A stored row has a value the daemon keeps
// and never shows; leaving its field empty keeps that value.
export interface EnvRow {
  key: number
  name: string
  value: string
  stored: boolean
}

let nextKey = 0

export const newEnvRow = (name = '', stored = false): EnvRow => ({key: nextKey++, name, value: '', stored})

export const envRowsFor = (names: readonly string[]): EnvRow[] => names.map((n) => newEnvRow(n, true))

// The variable that puts a harness on pay-per-token billing instead of its login.
export const KEY_SUGGESTION: Partial<Record<ProviderKind, string>> = {
  'claude-code': 'ANTHROPIC_API_KEY',
  codex: 'OPENAI_API_KEY',
}

export const isSecretName = (name: string) => /KEY|TOKEN|SECRET|PASSWORD/i.test(name)

const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/

// Why the rows cannot be saved, or '' when they can. The daemon checks the same
// things (and which names other fields own); this only saves a round trip.
export function envProblem(rows: readonly EnvRow[]): string {
  const seen = new Set<string>()
  for (const r of rows) {
    const name = r.name.trim()
    if (!name) {
      if (r.value) return 'Give every variable a name.'
      continue
    }
    if (!NAME.test(name))
      return `${name} is not a valid name: letters, digits and _ only, not starting with a digit.`
    if (seen.has(name)) return `${name} is listed twice.`
    seen.add(name)
    // A stored variable keeps its value when the field is empty; a new one with
    // none would store an empty value, which is never what was meant.
    if (!r.stored && !r.value) return `${name} needs a value.`
  }
  return ''
}

// The list the daemon receives. Rows without a name are dropped; a stored row
// with an empty field sends no value, so the stored one stays.
export function envPayload(rows: readonly EnvRow[]): ProviderEnvInput[] {
  return rows
    .filter((r) => r.name.trim())
    .map((r) => (r.stored && r.value === '' ? {name: r.name.trim()} : {name: r.name.trim(), value: r.value}))
}
