import {Command} from 'cmdk'
import {Dialog as DialogPrimitive} from 'radix-ui'
import {
  Download,
  Moon,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Sparkles,
  Sun,
  SunMoon,
  type LucideIcon,
} from 'lucide-react'
import {useEffect, useState, type ReactNode} from 'react'
import {DialogOverlay} from '@/components/ui/dialog'
import {Kbd} from '@/components/ui/kbd'
import {ask, openAssistant} from '@/features/assistant/store'
import {runNow} from '@/features/queue/actions'
import {setPaused} from '@/features/settings/actions'
import {openRunLog} from '@/features/log/store'
import {pickAndImportTask} from '@/features/task-editor/import'
import {openAddTask, openTaskEditor} from '@/features/task-editor/store'
import {openWhatsNew} from '@/features/whats-new/store'
import {refreshAll, useRuns, useSettings, useTasks} from '@/lib/queries'
import {useSaveAppearance} from '@/features/settings/use-appearance'
import {navigate} from './nav'
import {setPaletteOpen, usePalette} from './palette-store'
import {VIEWS} from './views'
import {usePlatform} from '@/lib/platform'

// The command palette (⌘K): jump to a view, start an action, or find a task or
// a recent run by name — everything the app can do, a few keystrokes away.
export function CommandPalette() {
  const {mod, appShortcuts} = usePlatform()
  const open = usePalette((s) => s.open)
  const tasks = useTasks().data ?? []
  const runs = useRuns().data ?? []
  const settings = useSettings().data
  const saveAppearance = useSaveAppearance()
  // What is typed, so it can go to the assistant as a question.
  const [query, setQuery] = useState('')
  // A closed palette forgets it, so the next one starts empty.
  const [wasOpen, setWasOpen] = useState(open)
  if (open !== wasOpen) {
    setWasOpen(open)
    if (!open) setQuery('')
  }

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === 'k') {
        e.preventDefault()
        setPaletteOpen(!usePalette.getState().open)
      }
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [])

  const run = (fn: () => void) => () => {
    setPaletteOpen(false)
    fn()
  }

  return (
    <DialogPrimitive.Root open={open} onOpenChange={setPaletteOpen}>
      <DialogPrimitive.Portal>
        <DialogOverlay className="bg-overlay/60" />
        <DialogPrimitive.Content
          aria-describedby={undefined}
          className="fixed top-[14vh] left-1/2 z-50 w-[calc(100vw-48px)] max-w-[600px] -translate-x-1/2 animate-in overflow-hidden rounded-xl border border-border bg-surface shadow-lg outline-none data-[state=closed]:animate-out"
        >
          <DialogPrimitive.Title className="sr-only">Command palette</DialogPrimitive.Title>
          <Command loop className="flex flex-col">
            <Command.Input
              autoFocus
              value={query}
              onValueChange={setQuery}
              placeholder="Type a command, search tasks, or ask DispatchQ…"
              className="h-12 border-b border-border bg-transparent px-4 text-[14px] outline-none placeholder:text-subtle-foreground"
            />
            <Command.List className="max-h-[min(420px,60vh)] overflow-y-auto p-1.5">
              {/* A longer text is a question for the assistant, offered at the end. */}
              {query.trim().length <= 2 && (
                <Command.Empty className="py-10 text-center text-[13px] text-muted-foreground">
                  Nothing found.
                </Command.Empty>
              )}

              <Group heading="Actions">
                <Item icon={Sparkles} onSelect={run(openAssistant)} hint={`${mod}J`}>
                  Ask DispatchQ
                </Item>
                <Item
                  icon={Plus}
                  onSelect={run(() => openAddTask())}
                  hint={appShortcuts ? `${mod}N` : undefined}
                >
                  New task
                </Item>
                <Item icon={Download} onSelect={run(pickAndImportTask)}>
                  Import task…
                </Item>
                <Item
                  icon={RefreshCw}
                  onSelect={run(() => void refreshAll())}
                  hint={appShortcuts ? `${mod}R` : undefined}
                >
                  Refresh everything
                </Item>
                <Item
                  icon={Sparkles}
                  onSelect={run(openWhatsNew)}
                  keywords={['rename', 'claudeq', 'migrate']}
                >
                  What’s new in DispatchQ
                </Item>
                {settings &&
                  (settings.paused ? (
                    <Item icon={Play} onSelect={run(() => void setPaused(false))}>
                      Resume all runs
                    </Item>
                  ) : (
                    <Item icon={Pause} onSelect={run(() => void setPaused(true))}>
                      Pause all runs
                    </Item>
                  ))}
              </Group>

              <Group heading="Go to">
                {VIEWS.map((v, i) => (
                  <Item key={v.id} icon={v.icon} onSelect={run(() => navigate(v.id))} hint={`${mod}${i + 1}`}>
                    {v.label}
                  </Item>
                ))}
              </Group>

              <Group heading="Appearance">
                <Item
                  icon={SunMoon}
                  onSelect={run(() => saveAppearance(''))}
                  keywords={['theme', 'system', 'auto']}
                >
                  Follow the system appearance
                </Item>
                <Item icon={Sun} onSelect={run(() => saveAppearance('light'))} keywords={['theme']}>
                  Light appearance
                </Item>
                <Item icon={Moon} onSelect={run(() => saveAppearance('dark'))} keywords={['theme']}>
                  Dark appearance
                </Item>
              </Group>

              {tasks.length > 0 && (
                <Group heading="Tasks">
                  {tasks.map((t) => (
                    <Command.Item
                      key={t.id}
                      value={`task ${t.name} ${t.id} ${t.group ?? ''}`}
                      onSelect={run(() => openTaskEditor({mode: 'edit', task: t}))}
                      className={itemClass}
                    >
                      <span className="flex-1 truncate">{t.name}</span>
                      <button
                        type="button"
                        className="rounded px-1.5 py-0.5 text-[11.5px] text-muted-foreground hover:bg-surface hover:text-foreground"
                        onClick={(e) => {
                          e.stopPropagation()
                          setPaletteOpen(false)
                          void runNow(t, {follow: true})
                        }}
                      >
                        Run now
                      </button>
                    </Command.Item>
                  ))}
                </Group>
              )}

              {runs.length > 0 && (
                <Group heading="Recent runs">
                  {runs.slice(0, 15).map((r) => (
                    <Command.Item
                      key={r.run_id}
                      value={`run ${r.task_name} ${r.run_id}`}
                      onSelect={run(() => openRunLog(r.run_id))}
                      className={itemClass}
                    >
                      <span className="flex-1 truncate">{r.task_name}</span>
                      <span className="text-[11.5px] text-subtle-foreground">
                        {new Date(r.started_at).toLocaleString()}
                      </span>
                    </Command.Item>
                  ))}
                </Group>
              )}
              {/* Last, so a typed command still wins the Enter key. */}
              {query.trim().length > 2 && (
                <Group heading="Assistant" forceMount>
                  <Command.Item
                    forceMount
                    // A fixed value, so the typed text cannot rank it above a real match.
                    value="ask-dispatchq"
                    onSelect={run(() => ask(query))}
                    className={itemClass}
                  >
                    <Sparkles />
                    <span className="flex-1 truncate">Ask DispatchQ “{query.trim()}”</span>
                  </Command.Item>
                </Group>
              )}
            </Command.List>
            <div className="flex items-center gap-3 border-t border-border bg-surface-2/60 px-3 py-2 text-[11.5px] text-subtle-foreground">
              <span className="flex items-center gap-1">
                <Kbd>↑</Kbd>
                <Kbd>↓</Kbd> to move
              </span>
              <span className="flex items-center gap-1">
                <Kbd>↵</Kbd> to choose
              </span>
              <span className="flex items-center gap-1">
                <Kbd>esc</Kbd> to close
              </span>
            </div>
          </Command>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  )
}

const itemClass =
  'flex h-9 cursor-default items-center gap-2.5 rounded-md px-2.5 text-[13px] outline-none select-none data-[selected=true]:bg-surface-3 [&_svg]:size-4 [&_svg]:text-muted-foreground'

function Group({
  heading,
  forceMount,
  children,
}: {
  heading: string
  forceMount?: boolean
  children: ReactNode
}) {
  return (
    <Command.Group
      heading={heading}
      forceMount={forceMount}
      className="mb-1 [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:pt-2 [&_[cmdk-group-heading]]:pb-1 [&_[cmdk-group-heading]]:text-[11px] [&_[cmdk-group-heading]]:font-semibold [&_[cmdk-group-heading]]:tracking-wide [&_[cmdk-group-heading]]:text-subtle-foreground [&_[cmdk-group-heading]]:uppercase"
    >
      {children}
    </Command.Group>
  )
}

function Item({
  icon: Icon,
  onSelect,
  hint,
  keywords,
  children,
}: {
  icon: LucideIcon
  onSelect: () => void
  hint?: string
  keywords?: string[]
  children: string
}) {
  return (
    <Command.Item onSelect={onSelect} keywords={keywords} className={itemClass}>
      <Icon />
      <span className="flex-1">{children}</span>
      {hint && <span className="text-[11.5px] text-subtle-foreground">{hint}</span>}
    </Command.Item>
  )
}
