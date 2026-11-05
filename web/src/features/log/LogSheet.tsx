import {ArrowDown, FileX} from 'lucide-react'
import {useMemo, useState} from 'react'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {EmptyState} from '@/components/ui/empty-state'
import {Segmented} from '@/components/ui/segmented'
import {Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader} from '@/components/ui/sheet'
import {Skeleton} from '@/components/ui/skeleton'
import {Spinner} from '@/components/ui/spinner'
import {Tooltip} from '@/components/ui/tooltip'
import {errorMessage} from '@/lib/api'
import {relTime, resumeAt, resumeText} from '@/lib/format'
import {useHealth} from '@/lib/queries'
import type {Run} from '@/lib/types'
import {ChatLog} from './components/ChatLog'
import {CopyButton} from './components/CopyButton'
import {RunArtifacts} from './components/RunArtifacts'
import {RunLogActions} from './components/RunLogActions'
import {StatusPill} from './components/StatusPill'
import {useNow} from '@/hooks/use-now'
import {useRunData} from './hooks/use-run-data'
import {useStickToBottom} from './hooks/use-stick-to-bottom'
import {runMetrics, runTimeTip} from './metrics'
import {chatTranscript, parseLog} from './parse-log'
import {runFolder} from './runs'
import {closeRunLog, useRunLog} from './store'

type Mode = 'chat' | 'raw'

// LogSheet shows one run as a chat or as its raw log, following it live while
// it runs. Mounted once by the shell; openRunLog(id) opens it from anywhere.
export function LogSheet() {
  const runId = useRunLog((s) => s.runId)
  // The last run stays rendered while the sheet slides out.
  const [shown, setShown] = useState(runId)
  if (runId && runId !== shown) setShown(runId)
  // Chat or Raw is a reading preference: it carries over to the next run.
  const [mode, setMode] = useState<Mode>('chat')

  return (
    <Sheet open={!!runId} onOpenChange={(open) => !open && closeRunLog()}>
      <SheetContent width="xl">
        {shown && <RunLogPanel key={shown} runId={shown} mode={mode} setMode={setMode} />}
      </SheetContent>
    </Sheet>
  )
}

function RunLogPanel({runId, mode, setMode}: {runId: string; mode: Mode; setMode: (m: Mode) => void}) {
  const {run, live, log, logError, loading} = useRunData(runId)
  const prompt = run?.task?.prompt ?? ''
  // A script run has a command and its output, not a conversation.
  const script = run?.task?.kind === 'script' || run?.provider?.kind === 'script'
  const entries = useMemo(() => parseLog(log ?? ''), [log])
  const {
    ref: bodyRef,
    onScroll,
    atBottom,
    toBottom,
  } = useStickToBottom<HTMLDivElement>(`${mode}:${log?.length ?? 0}`)

  return (
    <>
      <SheetHeader
        title={run?.task_name ?? 'Log'}
        description={run ? <RunSummary run={run} /> : undefined}
        actions={
          <>
            <Segmented<Mode>
              aria-label="Log format"
              size="sm"
              value={mode}
              onValueChange={setMode}
              options={[
                script
                  ? {value: 'chat', label: 'Output', tip: 'The script and what it printed'}
                  : {value: 'chat', label: 'Chat', tip: 'The conversation, with tool calls folded'},
                {value: 'raw', label: 'Raw', tip: 'The log file exactly as the run wrote it'},
              ]}
            />
            <CopyButton
              text={() => (mode === 'raw' ? (log ?? '') : chatTranscript(prompt, entries))}
              label={
                mode === 'raw' ? 'Copy the raw log' : script ? 'Copy the output' : 'Copy the conversation'
              }
            />
          </>
        }
      >
        {run && <RunAlerts run={run} />}
        <RunArtifacts runId={runId} />
      </SheetHeader>

      <div className="relative flex min-h-0 flex-1 flex-col">
        <SheetBody ref={bodyRef} onScroll={onScroll} className="bg-background/40">
          {loading || log === undefined ? (
            logError ? (
              <EmptyState
                icon={<FileX />}
                title="Could not read the log"
                description={errorMessage(logError)}
              />
            ) : (
              <LogSkeleton />
            )
          ) : mode === 'raw' ? (
            <pre className="font-mono text-[11.5px] leading-relaxed break-words whitespace-pre-wrap text-muted-foreground">
              {log || '(empty log)'}
            </pre>
          ) : !entries.length && !prompt.trim() ? (
            <EmptyState
              icon={<FileX />}
              title={live ? 'Nothing written yet' : 'Empty log'}
              description={
                live
                  ? 'The run has started; its output appears here as it comes.'
                  : 'This run left no output.'
              }
            />
          ) : (
            <ChatLog
              prompt={prompt}
              entries={entries}
              live={live}
              promptTitle={script ? 'Script' : 'Prompt'}
            />
          )}
          {live && log !== undefined && (
            <div className="mx-auto mt-4 flex max-w-[820px] items-center gap-2 text-[12px] text-muted-foreground">
              <Spinner className="size-3.5" />
              Running · following the log
            </div>
          )}
        </SheetBody>
        {!atBottom && (
          <Button
            variant="secondary"
            size="sm"
            onClick={toBottom}
            className="absolute bottom-3 left-1/2 -translate-x-1/2 rounded-full shadow-md"
          >
            <ArrowDown />
            {live ? 'Follow live output' : 'Jump to the end'}
          </Button>
        )}
      </div>

      <SheetFooter>{run && <RunLogActions run={run} />}</SheetFooter>
    </>
  )
}

// One line under the title: outcome, when, how long, where and on what.
function RunSummary({run}: {run: Run}) {
  const now = useNow(1000, run.status === 'running')
  const folder = runFolder(run)
  const p = run.provider
  const parts = [
    ...runMetrics(run, now).short,
    folder,
    p?.name ? [p.name, p.model].filter(Boolean).join(' · ') : '',
  ].filter(Boolean)
  return (
    <span className="mt-1 flex min-w-0 items-center gap-2">
      <StatusPill run={run} />
      <Tooltip content={runTimeTip(run, now)}>
        <span className="shrink-0">{relTime(run.started_at, now)}</span>
      </Tooltip>
      <span className="truncate text-subtle-foreground">{parts.join(' · ')}</span>
    </span>
  )
}

// A paused run says when it continues; a failed one
// says why, when the daemon recorded it.
function RunAlerts({run}: {run: Run}) {
  const limitedUntil = useHealth().data?.limited_until || undefined
  if (run.resume_pending)
    return (
      <Alert tone="warning" title="Rescheduled after a rate limit" className="mt-3">
        The session {resumeText(resumeAt(run.resume_at, limitedUntil))}. Cancel resume below drops it.
      </Alert>
    )
  if (run.error && run.status !== 'success' && run.status !== 'running')
    return (
      <Alert tone={run.status === 'canceled' ? 'neutral' : 'danger'} className="mt-3">
        {run.error}
      </Alert>
    )
  return null
}

function LogSkeleton() {
  return (
    <div className="mx-auto max-w-[820px] space-y-4">
      <Skeleton className="ml-auto h-16 w-2/3 rounded-xl" />
      <Skeleton className="h-4 w-3/4" />
      <Skeleton className="h-4 w-1/2" />
      <Skeleton className="h-9 w-full rounded-lg" />
      <Skeleton className="h-4 w-2/3" />
    </div>
  )
}
