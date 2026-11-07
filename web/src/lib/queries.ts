import {keepPreviousData, QueryClient, useQuery, type QueryKey} from '@tanstack/react-query'
import {get} from './api'
import type {
  AppNotification,
  Artifact,
  AssistantInfo,
  FeedbackInfo,
  Group,
  Health,
  LimitsResponse,
  Model,
  Pool,
  Provider,
  ProviderKindInfo,
  QueueTask,
  Run,
  Settings,
  Stats,
  StatsRange,
  UpdateStatus,
  WSLStatus,
} from './types'

// Every list is kept current in the background, not only the open view: a view
// that is clicked shows the present straight away. The intervals mirror what
// each list costs the daemon — lists are memory reads (5 s), health is a few
// syscalls (30 s), the update check is a cached GitHub answer (60 s).
export const LIST_POLL = 5_000

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 2_000,
      retry: 1,
      refetchOnWindowFocus: true,
      structuralSharing: true,
    },
  },
})

export const keys = {
  tasks: ['tasks'] as const,
  groups: ['groups'] as const,
  runs: ['runs'] as const,
  run: (id: string) => ['runs', id] as const,
  runLog: (id: string) => ['runs', id, 'log'] as const,
  artifacts: ['artifacts'] as const,
  notifications: ['notifications'] as const,
  providers: ['providers'] as const,
  providerKinds: ['providers', 'kinds'] as const,
  limits: ['limits'] as const,
  pools: ['pools'] as const,
  settings: ['settings'] as const,
  models: (provider = '') => ['models', provider] as const,
  // stats is the prefix of every range's entry, for invalidate.
  stats: ['stats'] as const,
  statsRange: (range: StatsRange) => ['stats', range] as const,
  health: ['health'] as const,
  update: ['update'] as const,
  feedback: ['feedback'] as const,
  assistant: ['assistant'] as const,
}

function poll<T>(key: QueryKey, path: string, interval: number | false = LIST_POLL) {
  return {
    queryKey: key,
    queryFn: ({signal}: {signal: AbortSignal}) => get<T>(path, signal),
    refetchInterval: interval,
  }
}

export const useTasks = () => useQuery(poll<QueueTask[]>(keys.tasks, '/api/tasks'))
export const useGroups = () => useQuery(poll<Group[]>(keys.groups, '/api/groups'))
export const useRuns = () => useQuery(poll<Run[]>(keys.runs, '/api/runs'))
export const useArtifacts = () => useQuery(poll<Artifact[]>(keys.artifacts, '/api/artifacts'))
export const useNotifications = () =>
  useQuery(poll<AppNotification[]>(keys.notifications, '/api/notifications'))
export const useProviders = () => useQuery(poll<Provider[]>(keys.providers, '/api/providers', 15_000))
export const useProviderKinds = () =>
  useQuery({
    ...poll<ProviderKindInfo[]>(keys.providerKinds, '/api/providers/kinds', false),
    staleTime: Infinity,
  })
// The limits poll reads only what the daemon last read from the providers;
// asking them again (?fresh=1) is for opening the Dashboard and ⌘R.
// Whether this build can run providers in WSL. The daemon answers without
// starting wsl.exe; the distribution list is asked only when Linux is chosen.
export const useWSLStatus = () =>
  useQuery({...poll<WSLStatus>(['wsl'], '/api/wsl', false), staleTime: Infinity})
export const useLimits = () => useQuery(poll<LimitsResponse>(keys.limits, '/api/limits'))
export const usePools = () => useQuery(poll<Pool[]>(keys.pools, '/api/pools', 15_000))
export const useSettings = () => useQuery(poll<Settings>(keys.settings, '/api/settings', 30_000))
export const useModels = (provider = '') =>
  useQuery({
    ...poll<Model[]>(
      keys.models(provider),
      '/api/models' + (provider ? '?provider=' + encodeURIComponent(provider) : ''),
      false,
    ),
    staleTime: 5 * 60_000,
  })
export const DEFAULT_STATS_RANGE: StatsRange = 14
// useStats reads one window of the usage history. Without a range it is the
// daemon's default of 14 days, which is what the Dashboard shows. Switching the
// range keeps the previous window on screen until the new one arrives.
export const useStats = (range: StatsRange = DEFAULT_STATS_RANGE) =>
  useQuery({
    ...poll<Stats>(
      keys.statsRange(range),
      '/api/stats' + (range === DEFAULT_STATS_RANGE ? '' : '?days=' + range),
      30_000,
    ),
    placeholderData: keepPreviousData,
  })
export const useHealth = () => useQuery(poll<Health>(keys.health, '/api/health', 30_000))
export const useUpdate = () => useQuery(poll<UpdateStatus>(keys.update, '/api/update', 60_000))
export const useAssistantInfo = () =>
  useQuery({...poll<AssistantInfo>(keys.assistant, '/api/assistant', false), staleTime: 60_000})

export const useFeedbackInfo = () =>
  useQuery({...poll<FeedbackInfo>(keys.feedback, '/api/feedback', false), staleTime: 60_000})

// invalidate re-reads the given lists now, after a change the UI just made.
export function invalidate(...ks: QueryKey[]) {
  return Promise.all(ks.map((k) => queryClient.invalidateQueries({queryKey: k})))
}

// refreshAll is View → Refresh (⌘R): every list is read again, and the
// providers' limits are asked for fresh.
export async function refreshAll() {
  await queryClient.invalidateQueries()
  try {
    queryClient.setQueryData(keys.limits, await get<LimitsResponse>('/api/limits?fresh=1'))
  } catch {
    /* the regular poll shows the last reading and its error */
  }
}
