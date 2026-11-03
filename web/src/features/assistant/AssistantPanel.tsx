import {Eraser, Settings2, Sparkles, X} from 'lucide-react'
import {useEffect, useRef} from 'react'
import {openSettings} from '@/app/nav'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {Tooltip} from '@/components/ui/tooltip'
import {Composer} from '@/features/feedback/components/Composer'
import {openAddTask} from '@/features/task-editor/store'
import {errorMessage} from '@/lib/api'
import {useAssistantInfo} from '@/lib/queries'
import {Entry, Typing} from './components/Entry'
import {clearConversation, closeAssistant, sendMessage, setComposer, useAssistant} from './store'

// Ways to start, shown on an empty conversation. Picking one only fills the
// composer.
const EXAMPLES = [
  'What ran last night?',
  'Why did the last run fail?',
  'Show me the newest artifact',
  'Plan a job for every weekday at 7',
]

// AssistantPanel is the chat docked to the right of the window; in a window
// too narrow to give the view its room beside it, it lies over the view's
// right edge instead of squeezing it. It stays open across views, so an answer
// that opens a run or an artifact leaves the conversation where it was.
export function AssistantPanel() {
  const s = useAssistant()
  const info = useAssistantInfo()

  // Settings may have changed the provider since the panel was last open.
  useEffect(() => {
    if (s.open) void info.refetch()
  }, [s.open]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!s.open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('[role=dialog]')) closeAssistant()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [s.open])

  const end = useRef<HTMLDivElement>(null)
  useEffect(() => {
    end.current?.scrollIntoView({behavior: 'smooth', block: 'end'})
  }, [s.entries, s.busy])

  if (!s.open) return null
  const available = !info.isError && (info.data?.available ?? true)
  const why = info.isError ? errorMessage(info.error) : info.data?.reason || ''
  const reason = why.charAt(0).toUpperCase() + why.slice(1)

  return (
    <aside
      aria-label="Assistant"
      className="flex w-[380px] max-w-full shrink-0 animate-in flex-col border-l border-border bg-surface max-[1099px]:fixed max-[1099px]:inset-y-0 max-[1099px]:right-0 max-[1099px]:z-30 max-[1099px]:shadow-xl"
    >
      <header className="flex items-center gap-2 border-b border-border px-4 py-3">
        <div className="flex size-7 items-center justify-center rounded-full bg-ai-soft text-ai [&_svg]:size-3.5">
          <Sparkles />
        </div>
        <div className="min-w-0 flex-1">
          <div className="text-[14px] leading-4 font-semibold">Assistant</div>
          <div className="truncate text-[11.5px] text-subtle-foreground">
            {info.data?.provider ? `Answers on ${info.data.provider}` : 'Runs, artifacts, jobs'}
          </div>
        </div>
        {s.entries.length > 0 && (
          <Tooltip content="Clear the conversation">
            <Button
              size="icon-sm"
              variant="ghost"
              onClick={clearConversation}
              aria-label="Clear the conversation"
            >
              <Eraser />
            </Button>
          </Tooltip>
        )}
        <Tooltip content="Close (Esc)">
          <Button size="icon-sm" variant="ghost" onClick={closeAssistant} aria-label="Close the assistant">
            <X />
          </Button>
        </Tooltip>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto px-4 py-4">
        {!available ? (
          <Alert
            tone="warning"
            title="The assistant cannot answer right now"
            actions={
              <>
                <Button size="sm" onClick={() => openSettings('assistant')}>
                  <Settings2 /> Settings
                </Button>
                <Button size="sm" variant="ghost" onClick={() => openAddTask()}>
                  New task form
                </Button>
              </>
            }
          >
            {reason}
          </Alert>
        ) : s.entries.length === 0 ? (
          <Intro onPick={setComposer} />
        ) : (
          <div className="flex flex-col gap-3" aria-live="polite">
            {s.entries.map((e) => (
              <Entry key={e.id} entry={e} />
            ))}
            {s.busy && <Typing />}
          </div>
        )}
        <div ref={end} />
      </div>

      {available && (
        <div className="border-t border-border p-3">
          <Composer
            value={s.composer}
            onChange={setComposer}
            onSend={sendMessage}
            busy={s.busy}
            placeholder="Ask about runs, open an artifact, plan a job…"
            enterSends
            rows={1}
          />
        </div>
      )}
    </aside>
  )
}

function Intro({onPick}: {onPick: (text: string) => void}) {
  return (
    <div className="flex animate-in flex-col items-center pt-8 text-center">
      <div className="mb-3 flex size-11 items-center justify-center rounded-2xl bg-ai-soft text-ai [&_svg]:size-5">
        <Sparkles />
      </div>
      <h2 className="text-[14px] font-semibold">What should DispatchQ do?</h2>
      <p className="mt-1 max-w-[290px] text-[12.5px] text-muted-foreground">
        Ask about runs and artifacts, or describe a job. Anything that changes the queue waits for your OK.
      </p>
      <div className="mt-4 flex flex-col items-stretch gap-1.5">
        {EXAMPLES.map((ex) => (
          <button
            key={ex}
            type="button"
            onClick={() => onPick(ex)}
            className="rounded-full border border-border bg-surface px-3 py-1 text-[12.5px] text-muted-foreground shadow-xs transition-colors hover:border-border-strong hover:text-foreground focus-visible:ring-3 focus-visible:ring-accent/20 focus-visible:outline-none"
          >
            {ex}
          </button>
        ))}
      </div>
    </div>
  )
}
