import {useIsFetching} from '@tanstack/react-query'
import {useEffect, useState} from 'react'
import {ExternalLink, PanelLeftClose, PanelLeftOpen, Plus, Search, Sparkles} from 'lucide-react'
import {Button} from '@/components/ui/button'
import {CountBadge} from '@/components/ui/badge'
import {Kbd} from '@/components/ui/kbd'
import {Tooltip} from '@/components/ui/tooltip'
import {toggleAssistant, useAssistant} from '@/features/assistant/store'
import {openAddTask} from '@/features/task-editor/store'
import {useMediaQuery} from '@/hooks/use-media-query'
import {useHealth, useSettings, useUpdate} from '@/lib/queries'
import {openExternal} from '@/lib/native'
import {keyCombo, menuKeyCombo, withShortcut} from '@/lib/platform'
import {cn} from '@/lib/utils'
import {useBadges} from './badges'
import {navigate, useNav} from './nav'
import {openPalette} from './palette-store'
import {VIEWS, type ViewDef} from './views'

const WEBSITE = 'https://www.dispatchq.de'

// Below this window width the sidebar folds to a rail of icons, so the view
// keeps its room; the rail's toggle lays the full sidebar over the view.
const NARROW = '(max-width: 719px)'

export function Sidebar() {
  const narrow = useMediaQuery(NARROW)
  const view = useNav((s) => s.view)
  // The overlay belongs to the view it was opened on: picking a view, or
  // widening the window, puts it away again.
  const [openOn, setOpenOn] = useState<string | null>(null)
  const [wasNarrow, setWasNarrow] = useState(narrow)
  if (narrow !== wasNarrow) {
    setWasNarrow(narrow)
    setOpenOn(null)
  }
  const overlay = narrow && openOn === view
  const setOpen = (open: boolean) => setOpenOn(open ? view : null)
  useEffect(() => {
    if (!overlay) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpenOn(null)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [overlay])

  if (!narrow) return <Panel />
  return (
    <>
      <Rail onExpand={() => setOpen(true)} />
      {overlay && (
        <div className="fixed inset-0 z-40 flex">
          <Panel onCollapse={() => setOpen(false)} className="shadow-xl" />
          <div className="flex-1 bg-black/20" onClick={() => setOpen(false)} aria-hidden />
        </div>
      )}
    </>
  )
}

function Panel({onCollapse, className}: {onCollapse?: () => void; className?: string}) {
  const view = useNav((s) => s.view)
  const badges = useBadges()
  const assistantOpen = useAssistant((s) => s.open)
  const main = VIEWS.filter((v) => !v.foot)
  const foot = VIEWS.filter((v) => v.foot)

  return (
    <aside className={cn('flex w-[228px] shrink-0 flex-col border-r border-border bg-sidebar', className)}>
      <div className="flex items-center gap-2.5 px-4 pt-4 pb-3">
        <img src="/logo.svg" alt="" className="size-7 rounded-lg shadow-sm" />
        <div className="min-w-0 flex-1">
          <div className="text-[14px] leading-4 font-semibold tracking-[-0.01em]">DispatchQ</div>
          <Version />
        </div>
        {onCollapse && (
          <Tooltip content="Collapse sidebar">
            <Button variant="ghost" size="icon-sm" onClick={onCollapse} aria-label="Collapse sidebar">
              <PanelLeftClose />
            </Button>
          </Tooltip>
        )}
      </div>

      <div className="flex flex-col gap-1.5 px-3 pb-3">
        <div className="flex gap-1.5">
          <Button
            variant="primary"
            className="min-w-0 flex-1 justify-start"
            onClick={() => {
              onCollapse?.()
              toggleAssistant()
            }}
            aria-pressed={assistantOpen}
          >
            <Sparkles /> Ask DispatchQ
            <span className="ml-auto text-[11px] font-normal opacity-75">{keyCombo('J')}</span>
          </Button>
          <Tooltip content={withShortcut('New task form', menuKeyCombo('N'))}>
            <Button
              variant="secondary"
              size="icon"
              onClick={() => {
                onCollapse?.()
                openAddTask()
              }}
              aria-label="New task form"
            >
              <Plus />
            </Button>
          </Tooltip>
        </div>
        <button
          onClick={() => {
            onCollapse?.()
            openPalette()
          }}
          className="flex h-8 items-center gap-2 rounded-md border border-border bg-surface px-2.5 text-[12.5px] text-subtle-foreground shadow-xs transition-colors hover:text-foreground"
        >
          <Search className="size-3.5" />
          <span className="flex-1 text-left">Search & commands</span>
          <Kbd>{keyCombo('K')}</Kbd>
        </button>
      </div>

      <nav className="flex flex-col gap-0.5 px-2" aria-label="Views">
        {main.map((v, i) => (
          <NavItem
            key={v.id}
            def={v}
            active={view === v.id}
            count={badges[v.id] ?? 0}
            shortcut={i + 1}
            onPick={onCollapse}
          />
        ))}
      </nav>

      <div className="flex-1" />

      <nav className="flex flex-col gap-0.5 px-2 pb-2" aria-label="More">
        {foot.map((v, i) => (
          <NavItem
            key={v.id}
            def={v}
            active={view === v.id}
            count={badges[v.id] ?? 0}
            shortcut={main.length + i + 1}
            onPick={onCollapse}
          />
        ))}
      </nav>
      <Footer />
    </aside>
  )
}

// Rail is the folded sidebar: the same actions and views as icons, each named
// in a tooltip, and a toggle that brings the full sidebar back.
function Rail({onExpand}: {onExpand: () => void}) {
  const view = useNav((s) => s.view)
  const badges = useBadges()
  const assistantOpen = useAssistant((s) => s.open)
  const main = VIEWS.filter((v) => !v.foot)
  const foot = VIEWS.filter((v) => v.foot)
  const icon = (def: ViewDef, shortcut: number) => (
    <RailItem
      key={def.id}
      def={def}
      active={view === def.id}
      count={badges[def.id] ?? 0}
      shortcut={shortcut}
    />
  )

  return (
    <aside className="flex w-[52px] shrink-0 flex-col items-center gap-1.5 border-r border-border bg-sidebar pt-4 pb-2">
      <img src="/logo.svg" alt="" className="mb-1 size-7 rounded-lg shadow-sm" />
      <Tooltip content="Expand sidebar" side="right">
        <Button variant="ghost" size="icon" onClick={onExpand} aria-label="Expand sidebar">
          <PanelLeftOpen />
        </Button>
      </Tooltip>
      <Tooltip content={withShortcut('Ask DispatchQ', keyCombo('J'))} side="right">
        <Button
          variant="primary"
          size="icon"
          onClick={toggleAssistant}
          aria-pressed={assistantOpen}
          aria-label="Ask DispatchQ"
        >
          <Sparkles />
        </Button>
      </Tooltip>
      <Tooltip content={withShortcut('New task form', menuKeyCombo('N'))} side="right">
        <Button variant="secondary" size="icon" onClick={() => openAddTask()} aria-label="New task form">
          <Plus />
        </Button>
      </Tooltip>
      <Tooltip content={withShortcut('Search & commands', keyCombo('K'))} side="right">
        <Button variant="secondary" size="icon" onClick={openPalette} aria-label="Search & commands">
          <Search />
        </Button>
      </Tooltip>

      <nav className="mt-1.5 flex flex-col gap-0.5" aria-label="Views">
        {main.map((v, i) => icon(v, i + 1))}
      </nav>
      <div className="flex-1" />
      <nav className="flex flex-col gap-0.5" aria-label="More">
        {foot.map((v, i) => icon(v, main.length + i + 1))}
      </nav>
      <RailStatus />
    </aside>
  )
}

function RailItem({
  def,
  active,
  count,
  shortcut,
}: {
  def: ViewDef
  active: boolean
  count: number
  shortcut: number
}) {
  const Icon = def.icon
  return (
    <Tooltip content={withShortcut(def.label, keyCombo(String(shortcut)))} side="right">
      <button
        onClick={() => navigate(def.id)}
        aria-current={active ? 'page' : undefined}
        aria-label={def.label}
        className={cn(
          'relative flex size-8 items-center justify-center rounded-md transition-colors',
          active
            ? 'bg-accent-soft text-accent-text'
            : 'text-muted-foreground hover:bg-surface-3/70 hover:text-foreground',
        )}
      >
        <Icon className="size-4" />
        {count > 0 && (
          <CountBadge
            count={count}
            className={cn(
              'absolute -top-1 -right-1.5 h-[15px] min-w-[15px] px-1 text-[9.5px]',
              def.id === 'settings' && 'bg-danger text-white',
            )}
          />
        )}
      </button>
    </Tooltip>
  )
}

function NavItem({
  def,
  active,
  count,
  shortcut,
  onPick,
}: {
  def: ViewDef
  active: boolean
  count: number
  shortcut: number
  onPick?: () => void
}) {
  const Icon = def.icon
  return (
    <button
      onClick={() => {
        onPick?.()
        navigate(def.id)
      }}
      aria-current={active ? 'page' : undefined}
      className={cn(
        'group flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[13px] font-medium transition-colors',
        active
          ? 'bg-accent-soft text-accent-text'
          : 'text-foreground/80 hover:bg-surface-3/70 hover:text-foreground',
      )}
    >
      <Icon
        className={cn(
          'size-4 shrink-0',
          active ? 'text-accent-text' : 'text-muted-foreground group-hover:text-foreground',
        )}
      />
      <span className="flex-1 truncate text-left">{def.label}</span>
      {count > 0 ? (
        <CountBadge count={count} className={def.id === 'settings' ? 'bg-danger text-white' : undefined} />
      ) : (
        <span className="text-[11px] text-subtle-foreground opacity-0 transition-opacity group-hover:opacity-100">
          {keyCombo(String(shortcut))}
        </span>
      )}
    </button>
  )
}

function Version() {
  const update = useUpdate().data
  if (!update?.current) return <div className="text-[11.5px] text-subtle-foreground">Task queue</div>
  return (
    <div className="truncate text-[11.5px] text-subtle-foreground">
      {update.supported ? 'v' + update.current : update.current}
    </div>
  )
}

// connection is whether the daemon answers and whether runs are paused.
function useConnection() {
  const health = useHealth()
  const settings = useSettings().data
  const fetching = useIsFetching() > 0
  const down = health.isError
  const state = down
    ? {dot: 'bg-danger', text: 'Daemon not reachable'}
    : settings?.paused
      ? {dot: 'bg-warning', text: 'All runs paused'}
      : {dot: 'bg-success', text: 'Connected'}
  return {...state, pulse: fetching && !down}
}

// The footer says whether the daemon answers and whether runs are paused.
function Footer() {
  const state = useConnection()
  return (
    <div className="flex items-center gap-2 border-t border-border px-4 py-2.5 text-[12px] text-muted-foreground">
      <span className={cn('size-2 rounded-full', state.dot, state.pulse && 'animate-pulse-dot')} />
      <span className="flex-1 truncate">{state.text}</span>
      <Tooltip content="DispatchQ website">
        <button
          onClick={() => openExternal(WEBSITE)}
          className="rounded p-1 hover:bg-surface-3 hover:text-foreground"
          aria-label="DispatchQ website"
        >
          <ExternalLink className="size-3.5" />
        </button>
      </Tooltip>
    </div>
  )
}

// RailStatus is the footer's connection state as a dot, named in its tooltip.
function RailStatus() {
  const state = useConnection()
  return (
    <Tooltip content={state.text} side="right">
      <span className="mt-1 flex size-8 items-center justify-center" role="status" aria-label={state.text}>
        <span className={cn('size-2 rounded-full', state.dot, state.pulse && 'animate-pulse-dot')} />
      </span>
    </Tooltip>
  )
}
