import type {SelectOption} from '@/components/ui/select'
import {executionLabel} from '@/lib/providers'
import type {HealthState, Provider, ProviderLimits} from '@/lib/types'

export type HealthTone = 'success' | 'warning' | 'danger'

// Readiness comes from the daemon, which probes the CLI itself; the card only
// says what it was told and never guesses.
const HEALTH: Record<HealthState, [HealthTone, string]> = {
  ready: ['success', 'Ready'],
  not_installed: ['danger', 'Not installed'],
  not_authenticated: ['danger', 'Not logged in'],
  invalid_configuration: ['danger', 'Invalid configuration'],
  disabled: ['warning', 'Switched off'],
  check_failed: ['warning', 'Could not be checked'],
}

export function healthInfo(state: string): {tone: HealthTone; label: string} {
  const known = HEALTH[state as HealthState]
  if (known) return {tone: known[0], label: known[1]}
  return {tone: 'warning', label: state || 'Unknown'}
}

export const byName = (a: Provider, b: Provider) => (a.name || a.id).localeCompare(b.name || b.id)

export interface KindGroup {
  kind: string
  name: string
  providers: Provider[]
}

// groupByKind files the providers under their harness. The default provider
// always comes first: its group leads, and it leads its group; the rest follow
// by name.
export function groupByKind(list: Provider[]): KindGroup[] {
  const groups = new Map<string, KindGroup>()
  for (const p of list) {
    const g = groups.get(p.kind) ?? {kind: p.kind, name: p.type_name || p.kind, providers: []}
    g.providers.push(p)
    groups.set(p.kind, g)
  }
  const first = (p: Provider) => (p.default ? 0 : 1)
  const out = [...groups.values()]
  for (const g of out) g.providers.sort((a, b) => first(a) - first(b) || byName(a, b))
  const hasDefault = (g: KindGroup) => (g.providers[0]?.default ? 0 : 1)
  return out.sort((a, b) => hasDefault(a) - hasDefault(b) || a.name.localeCompare(b.name))
}

// fallbackOptions offers every other configured provider as the one that takes
// over while this one is rate-limited. A stored id that has since gone keeps
// its place, marked, so saving does not quietly drop the fallback the operator
// chose.
export function fallbackOptions(self: string, providers: Provider[], stored: string): SelectOption[] {
  const others = providers.filter((p) => p.id !== self)
  const opts: SelectOption[] = [
    {value: '', label: 'Wait for the limit'},
    ...others.map((p) => ({
      value: p.id,
      label: (p.name || p.id) + (p.enabled ? '' : ' (switched off)'),
      description: executionLabel(p.execution) || undefined,
    })),
  ]
  if (stored && !others.some((p) => p.id === stored))
    opts.push({value: stored, label: `${stored} (unavailable)`})
  return opts
}

// A window is coloured before it runs dry: at 0% left the provider's tasks
// already wait, and the point is to see it coming.
export function windowTone(left: number): 'danger' | 'warning' | 'accent' {
  return left <= 10 ? 'danger' : left <= 25 ? 'warning' : 'accent'
}

// A provider with nothing to report (switched off, or a harness without an
// allowance) gets no Limits row rather than a row saying so.
export function hasLimitsRow(l: ProviderLimits | undefined): l is ProviderLimits {
  return !!l && ((l.windows?.length ?? 0) > 0 || l.state === 'unavailable')
}

// Where each CLI keeps its configuration inside Linux when nothing points it
// elsewhere, for the placeholder of a Linux (WSL) provider's field. The
// daemon's own default is this computer's, which a Linux provider never uses.
const LINUX_CONFIG_DIRS: Record<string, string> = {
  'claude-code': '~/.claude',
  codex: '~/.codex',
  opencode: '~/.config/opencode',
}

export function linuxConfigDir(kind: string): string {
  return LINUX_CONFIG_DIRS[kind] ?? "the CLI's own, inside Linux"
}
