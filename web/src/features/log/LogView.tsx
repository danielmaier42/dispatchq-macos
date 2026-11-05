import {History, SearchX, WifiOff} from 'lucide-react'
import {Fragment, useMemo} from 'react'
import {DayHeading, ViewLayout} from '@/components/layout/view-layout'
import {Button} from '@/components/ui/button'
import {EmptyState} from '@/components/ui/empty-state'
import {Skeleton} from '@/components/ui/skeleton'
import {groupByDay} from '@/lib/format'
import {useHealth, useProviders, useRuns} from '@/lib/queries'
import {LogToolbar} from './components/LogToolbar'
import {RunRow} from './components/RunRow'
import {WorkflowGroup} from './components/WorkflowGroup'
import {clearLogFilters, filtersOf, showMoreRuns, useLogFilters} from './filters-store'
import {
  anyFilter,
  emptyReason,
  entryStart,
  filterMenus,
  groupWorkflows,
  runMatches,
  staleChoices,
} from './runs'

// The Log is the run history: every run, newest first, split by day, with the
// runs of one workflow boxed together. Opening a run shows its log.
export function LogView() {
  const runsQuery = useRuns()
  const runs = runsQuery.data
  const state = useLogFilters()
  const limitedUntil = useHealth().data?.limited_until || undefined
  const showProvider = (useProviders().data?.length ?? 0) > 1

  const {f, menus} = useMemo(() => {
    const all = runs ?? []
    const asked = filtersOf(state)
    const first = filterMenus(all, asked)
    const f = {...asked, ...staleChoices(asked, first)}
    return {f, menus: f.group === asked.group ? first : filterMenus(all, f)}
  }, [runs, state])

  const filtered = useMemo(() => (runs ?? []).filter((r) => runMatches(r, f)), [runs, f])
  const days = useMemo(
    () => groupByDay(groupWorkflows(filtered.slice(0, state.limit)), entryStart),
    [filtered, state.limit],
  )
  const shown = Math.min(state.limit, filtered.length)

  return (
    <ViewLayout
      title="Log"
      subtitle={runs?.length ? `${runs.length} run${runs.length === 1 ? '' : 's'}` : undefined}
      toolbar={runs?.length ? <LogToolbar f={f} menus={menus} /> : undefined}
    >
      {!runs ? (
        runsQuery.isError ? (
          <EmptyState
            icon={<WifiOff />}
            title="Could not load the run history"
            description="The daemon did not answer. The list fills in as soon as it does."
            action={<Button onClick={() => void runsQuery.refetch()}>Try again</Button>}
          />
        ) : (
          <LoadingRows />
        )
      ) : !runs.length ? (
        <EmptyState
          icon={<History />}
          title="Nothing logged yet"
          description="Runs will appear here after tasks execute."
        />
      ) : !filtered.length ? (
        <EmptyState
          icon={<SearchX />}
          title="No runs match"
          description={emptyReason(f)}
          action={
            anyFilter(f) && (
              <Button variant="secondary" onClick={clearLogFilters}>
                Clear filters
              </Button>
            )
          }
        />
      ) : (
        <>
          {days.map((d) => (
            <Fragment key={d.day}>
              <DayHeading>{d.label}</DayHeading>
              <div className="mb-4 divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface shadow-xs">
                {d.items.map((e) =>
                  e.kind === 'run' ? (
                    <RunRow
                      key={e.run.run_id}
                      run={e.run}
                      showProvider={showProvider}
                      limitedUntil={limitedUntil}
                    />
                  ) : (
                    <WorkflowGroup
                      key={e.id}
                      runs={e.runs}
                      showProvider={showProvider}
                      limitedUntil={limitedUntil}
                    />
                  ),
                )}
              </div>
            </Fragment>
          ))}
          <div className="flex items-center justify-between gap-3 pt-1 pb-2 text-[12.5px] text-muted-foreground">
            <span className="tabular-nums">
              {shown === filtered.length
                ? `${filtered.length} ${anyFilter(f) ? 'matching ' : ''}run${filtered.length === 1 ? '' : 's'}`
                : `Showing ${shown} of ${filtered.length}${anyFilter(f) ? ' matching' : ''} runs`}
            </span>
            {shown < filtered.length && (
              <Button variant="secondary" size="sm" onClick={showMoreRuns}>
                Show more
              </Button>
            )}
          </div>
        </>
      )}
    </ViewLayout>
  )
}

function LoadingRows() {
  return (
    <div className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface">
      {Array.from({length: 8}, (_, i) => (
        <div key={i} className="flex items-center gap-3 px-3 py-3">
          <span className="w-2" />
          <div className="flex-1 space-y-1.5">
            <Skeleton className="h-3.5 w-1/3" />
            <Skeleton className="h-3 w-1/2" />
          </div>
          <Skeleton className="h-5 w-[104px] rounded-full" />
        </div>
      ))}
    </div>
  )
}
