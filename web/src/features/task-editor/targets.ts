import {executionLabel} from '@/lib/providers'
import type {Pool, Provider} from '@/lib/types'
import {POOL_PREFIX, poolOf} from './form'

export interface TargetOption {
  value: string
  label: string
  // Shown under the label: why a kept choice cannot run, or where it runs.
  note?: string
  broken?: boolean
}

export interface TargetGroups {
  providers: TargetOption[]
  pools: TargetOption[]
}

const healthText = (p: Provider) => (p.health ? p.health.state.replace(/_/g, ' ') : 'unavailable')
const nameOf = (x: {id: string; name?: string}) => x.name || x.id

// targetOptions lists where a task may run. Only providers that can actually
// run are offered. A task already pointed at one that cannot keeps it in the
// list, marked — an existing task stays visible with the reason it is blocked
// rather than being quietly moved.
//
// Pools sit below the providers in the same field.
export function targetOptions(providers: Provider[], pools: Pool[], current: string): TargetGroups {
  const poolID = poolOf(current)
  const dflt = providers.find((p) => p.default)
  const offered = providers
    .filter((p) => p.enabled && p.health?.state === 'ready')
    .sort((a, b) => nameOf(a).localeCompare(nameOf(b)))
  const picked = providers.find((p) => p.id === current)
  if (current && picked && !offered.some((p) => p.id === current)) offered.push(picked)

  const list: TargetOption[] = [
    {value: '', label: dflt ? `Default (${nameOf(dflt)})` : 'Default provider'},
    ...offered.map((p) => {
      const ready = p.health?.state === 'ready'
      return {
        value: p.id,
        label: nameOf(p),
        broken: !ready,
        note: ready ? executionLabel(p.execution) || undefined : healthText(p),
      }
    }),
  ]
  if (current && !poolID && !picked)
    list.push({value: current, label: current, broken: true, note: 'not configured'})

  const poolList: TargetOption[] = pools.map((p) => ({value: POOL_PREFIX + p.id, label: nameOf(p)}))
  if (poolID && !pools.some((p) => p.id === poolID))
    poolList.push({value: current, label: poolID, broken: true, note: 'not configured'})

  return {providers: list, pools: poolList}
}

// modelProvider is the provider whose models (and reasoning effort) apply to a
// choice. A pool's members are of one type, so its first member speaks for all
// of them.
export function modelProvider(target: string, pools: Pool[]): string {
  const poolID = poolOf(target)
  if (!poolID) return target
  return pools.find((p) => p.id === poolID)?.members[0]?.provider ?? ''
}

// takesEffort reports whether the harness behind a choice accepts a reasoning
// effort ("" is the default provider).
export function takesEffort(target: string, providers: Provider[], pools: Pool[]): boolean {
  const id = modelProvider(target, pools)
  const p = id ? providers.find((x) => x.id === id) : providers.find((x) => x.default)
  return !!p?.reasoning_effort
}

// REASONING_LEVELS are what a harness that takes a reasoning effort accepts.
// The list is dispatchq's, not a provider's: it is a suggestion in a picker, and a
// value a provider does not know is refused by the provider, not here.
export const REASONING_LEVELS = ['low', 'medium', 'high', 'xhigh']

export function effortOptions(current: string) {
  const opts = [
    {value: '', label: 'Provider default'},
    ...REASONING_LEVELS.map((l) => ({value: l, label: l})),
  ]
  if (current && !REASONING_LEVELS.includes(current))
    opts.push({value: current, label: `${current} (custom)`})
  return opts
}
