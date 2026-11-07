import type {SelectOption} from '@/components/ui/select'
import type {ProviderExecution, WSLDistro} from '@/lib/types'

export type RunsIn = 'native' | 'wsl'

// distroOptions lists the installed distributions, the default first, with
// what WSL says about each: a WSL 1 distribution is offered but marked.
export function distroOptions(list: WSLDistro[]): SelectOption[] {
  return [...list]
    .sort((a, b) => Number(b.default) - Number(a.default) || a.name.localeCompare(b.name))
    .map((d) => ({
      value: d.name,
      label: d.name + (d.default ? ' (default)' : ''),
      description:
        [d.version === 1 ? 'WSL 1, providers need WSL 2' : '', d.state ?? ''].filter(Boolean).join(' · ') ||
        undefined,
    }))
}

// executionPayload is what a new provider sends: nothing for Windows, so a
// native provider is stored exactly as before environments existed.
export function executionPayload(
  runsIn: RunsIn,
  distro: string,
  user: string,
): ProviderExecution | undefined {
  if (runsIn !== 'wsl') return undefined
  return {type: 'wsl', distro, user: user.trim()}
}
