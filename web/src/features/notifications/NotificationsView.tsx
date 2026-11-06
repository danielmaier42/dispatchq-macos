import {BellOff, Eye, SearchX} from 'lucide-react'
import {useMemo, useState, type KeyboardEvent} from 'react'
import {DayHeading, ViewLayout} from '@/components/layout/view-layout'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {EmptyState} from '@/components/ui/empty-state'
import {FilterChip} from '@/components/ui/filter-chip'
import {FilterPopover, FilterSelect, optionLabel} from '@/components/ui/filter-popover'
import {SearchInput} from '@/components/ui/search-input'
import {Segmented} from '@/components/ui/segmented'
import {Skeleton} from '@/components/ui/skeleton'
import {errorMessage} from '@/lib/api'
import {groupByDay} from '@/lib/format'
import {useNotifications} from '@/lib/queries'
import {anyFilter, kindOptions, shown} from './logic'
import {NotificationRow} from './NotificationRow'
import {markAllNotificationsRead} from './open-target'
import {clearNotificationFilters, setNotificationFilters, useNotificationFilters} from './use-filters'
import {usePlatform} from '@/lib/platform'

// The Notifications view: every notification the daemon sent — a run's
// outcome, a new artifact, a provider in trouble, what a task said with
// `dispatchq notify` — as it was shown, newest first, with the ones not looked
// at yet counted in the sidebar.

const BATCH = 50

export function NotificationsView() {
  const {isMac} = usePlatform()
  const {data: notes, isPending, error} = useNotifications()
  const filters = useNotificationFilters()
  const [marking, setMarking] = useState(false)
  // A new filter starts again from the first batch.
  const [paging, setPaging] = useState({filters, limit: BATCH})
  const limit = paging.filters === filters ? paging.limit : BATCH

  const {options, kind} = useMemo(() => kindOptions(notes ?? [], filters), [notes, filters])
  const filtered = useMemo(
    () => (notes ?? []).filter((n) => shown(n, {...filters, kind})),
    [notes, filters, kind],
  )
  const visible = filtered.slice(0, limit)
  const days = groupByDay(visible, (n) => n.sent_at)
  const unread = notes?.filter((n) => n.unread).length ?? 0

  const markAll = async () => {
    setMarking(true)
    await markAllNotificationsRead()
    setMarking(false)
  }

  return (
    <ViewLayout
      title="Notifications"
      subtitle={unread ? `${unread} unread` : 'Everything DispatchQ announced'}
      actions={
        <Button onClick={markAll} loading={marking} disabled={!unread}>
          {!marking && <Eye />}
          Mark all read
        </Button>
      }
      toolbar={
        notes?.length ? (
          <>
            <SearchInput
              hotkey
              value={filters.query}
              onChange={(query) => setNotificationFilters({query})}
              placeholder="Search notifications"
              className="w-60"
            />
            <Segmented
              aria-label="Read state"
              value={filters.unread ? 'unread' : 'all'}
              onValueChange={(v) => setNotificationFilters({unread: v === 'unread'})}
              options={[
                {value: 'all', label: 'All', tip: 'Show every notification'},
                {
                  value: 'unread',
                  label: 'Unread',
                  tip: 'Show only the notifications you have not looked at yet',
                },
              ]}
            />
            {/* With a single kind there is nothing to filter by. */}
            {(options.length > 1 || kind) && (
              <FilterPopover active={kind ? 1 : 0} onReset={() => setNotificationFilters({kind: ''})}>
                <FilterSelect
                  label="Kind"
                  all="All kinds"
                  value={kind}
                  options={options}
                  onChange={(v) => setNotificationFilters({kind: v})}
                />
              </FilterPopover>
            )}
            {kind && (
              <FilterChip
                label="Kind"
                value={optionLabel(options, kind)}
                onRemove={() => setNotificationFilters({kind: ''})}
              />
            )}
          </>
        ) : undefined
      }
    >
      {isPending ? (
        <RowSkeletons />
      ) : error && !notes ? (
        <Alert tone="danger" title="Could not load notifications">
          {errorMessage(error)}
        </Alert>
      ) : !notes?.length ? (
        <EmptyState
          icon={<BellOff />}
          title="No notifications yet"
          description={`Everything DispatchQ announces — a run that failed, a new artifact, a message from a task — is listed here, even after the ${isMac ? 'macOS banner' : 'desktop notification'} is gone.`}
        />
      ) : !filtered.length ? (
        <EmptyState
          icon={<SearchX />}
          title="No notifications match"
          description={
            filters.query.trim()
              ? `Nothing matches “${filters.query.trim()}”. Try fewer or different words.`
              : filters.unread
                ? 'Everything here has been read. Switch to All to see the rest.'
                : 'Pick another kind to see notifications.'
          }
          action={
            <Button variant="secondary" onClick={clearNotificationFilters}>
              Clear filters
            </Button>
          }
        />
      ) : (
        <div onKeyDown={stepFocus}>
          {days.map((d) => (
            <section key={d.day} className="mb-4 last:mb-0">
              <DayHeading>{d.label}</DayHeading>
              <div className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface shadow-xs">
                {d.items.map((n) => (
                  <NotificationRow key={n.id} n={n} />
                ))}
              </div>
            </section>
          ))}
          <div className="mt-5 flex items-center justify-between gap-3 text-[12.5px] text-muted-foreground">
            <span>
              {filtered.length} notification{filtered.length === 1 ? '' : 's'}
              {anyFilter(filters) ? ' matching' : ''}
              {visible.length < filtered.length ? ` · showing ${visible.length}` : ''}
            </span>
            {visible.length < filtered.length && (
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setPaging({filters, limit: limit + BATCH})}
              >
                Show {Math.min(BATCH, filtered.length - visible.length)} more
              </Button>
            )}
          </div>
        </div>
      )}
    </ViewLayout>
  )
}

// ↑ and ↓ move between rows, the way an inbox reads, from the row's open
// button or its mark-read eye; Enter opens the row.
function stepFocus(e: KeyboardEvent<HTMLDivElement>) {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return
  const row = (e.target as HTMLElement).closest('[data-notification-row]')
  if (!row) return
  const rows = [...e.currentTarget.querySelectorAll<HTMLElement>('[data-notification-row]')]
  const to = rows[rows.indexOf(row as HTMLElement) + (e.key === 'ArrowDown' ? 1 : -1)]
  if (!to) return
  e.preventDefault()
  to.querySelector<HTMLElement>('[data-notification-open]')?.focus()
}

function RowSkeletons() {
  return (
    <div className="divide-y divide-border overflow-hidden rounded-xl border border-border bg-surface">
      {Array.from({length: 7}, (_, i) => (
        <div key={i} className="flex items-start gap-3 px-3 py-3">
          <Skeleton className="size-7 rounded-lg" />
          <div className="flex-1 space-y-2 pt-1">
            <Skeleton className="h-3.5 w-2/5" />
            <Skeleton className="h-3 w-3/4" />
          </div>
          <Skeleton className="h-3 w-12" />
        </div>
      ))}
    </div>
  )
}
