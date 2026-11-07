import {useModels, useProviders} from './queries'
import type {Model, Provider, ProviderExecution} from './types'

export const providerName = (p: Pick<Provider, 'id' | 'name'>) => p.name || p.id

// executionLabel names where a provider runs, for a badge or an option note.
// A provider on this computer gets no label: that is every provider outside
// DispatchQ for Windows, and the default there.
export function executionLabel(e?: ProviderExecution | null): string {
  if (!e?.type) return ''
  if (e.type === 'wsl') return `Linux · ${e.distro ?? '?'}${e.user ? ` · ${e.user}` : ''}`
  return `Unknown environment (${String(e.type)})`
}

export function defaultProvider(providers: Provider[] | undefined): Provider | undefined {
  return providers?.find((p) => p.default)
}

// modelLabel turns a model id into what the catalog calls it, falling back to
// the id: a task may name a model discovery never listed.
export function modelLabel(models: Model[] | undefined, id: string): string {
  return models?.find((m) => m.id === id)?.label ?? id
}

// useModelLabel resolves model ids against the global catalog.
export function useModelLabel() {
  const models = useModels().data
  return (id: string) => modelLabel(models, id)
}

export function useProviderList() {
  return useProviders().data ?? []
}
