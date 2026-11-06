import {ChevronLeft, ChevronRight, Download, ExternalLink, SquareTerminal, Trash2} from 'lucide-react'
import {Dialog as DialogPrimitive} from 'radix-ui'
import {useEffect, useMemo, useRef, useState, type KeyboardEvent} from 'react'
import {Button} from '@/components/ui/button'
import {Dialog, DialogContent, DialogFooter} from '@/components/ui/dialog'
import {Kbd} from '@/components/ui/kbd'
import {toast} from '@/components/ui/toaster'
import {Tooltip} from '@/components/ui/tooltip'
import {openRunLog} from '@/features/log/store'
import {api, errorMessage} from '@/lib/api'
import {bytes, fullTime, relTime} from '@/lib/format'
import {invalidate, keys, queryClient, useArtifacts, useRuns} from '@/lib/queries'
import type {Artifact} from '@/lib/types'
import {deleteArtifact, downloadArtifact, markArtifactRead, openArtifactExternal} from './actions'
import {FileIcon} from './FileIcon'
import {canContinue} from '@/features/log/runs'
import {neighbours, sourceName} from './logic'
import {closeArtifact, stepArtifact, useArtifactViewer} from './store'
import {ViewerBody} from './ViewerBody'
import {usePlatform} from '@/lib/platform'

// The quick-look viewer: a full-size dialog rather than a side sheet, because
// an artifact is a document (a web page, a PDF) that wants the window's width,
// and ← → step through the list like Quick Look in Finder. It also stacks
// cleanly over the run log sheet, which opens artifacts too.
export function ArtifactViewer() {
  const {artifactId, sequence} = useArtifactViewer()
  const {data: arts} = useArtifacts()
  const artifact = artifactId ? arts?.find((a) => a.id === artifactId) : undefined

  // The order ← → step in: the list as the Artifacts view filtered it, still
  // existing, or every artifact when the viewer was opened from elsewhere.
  const ids = useMemo(() => {
    const all = arts?.map((a) => a.id) ?? []
    if (!sequence || !artifactId || !sequence.includes(artifactId)) return all
    const present = new Set(all)
    return sequence.filter((id) => present.has(id))
  }, [arts, sequence, artifactId])
  const {index, prev, next} = artifactId ? neighbours(ids, artifactId) : {index: -1, prev: null, next: null}

  // An id the list does not know is read again first: a notification for an
  // artifact published a moment ago can beat the next poll. Still missing, it
  // is gone (deleted, or a stale notification), and the viewer stays closed
  // with a word why.
  useEffect(() => {
    if (!artifactId || !arts || arts.some((a) => a.id === artifactId)) return
    let stale = false
    void invalidate(keys.artifacts).then(() => {
      const fresh = queryClient.getQueryData<Artifact[]>(keys.artifacts)
      if (stale || useArtifactViewer.getState().artifactId !== artifactId) return
      if (fresh?.some((a) => a.id === artifactId)) return
      toast.error('That artifact is no longer available')
      closeArtifact()
    })
    return () => {
      stale = true
    }
  }, [artifactId, arts])

  // Opening an artifact marks it read, once per open.
  const marked = useRef<string | null>(null)
  useEffect(() => {
    if (!artifact) marked.current = null
    else if (artifact.unread && marked.current !== artifact.id) {
      marked.current = artifact.id
      void markArtifactRead(artifact.id)
    }
  }, [artifact])

  const onKeyDown = (e: KeyboardEvent) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return
    const t = e.target as HTMLElement
    if (t.closest('input, textarea, select, [contenteditable=true]')) return
    const to = e.key === 'ArrowLeft' ? prev : e.key === 'ArrowRight' ? next : null
    if (!to) return
    e.preventDefault()
    stepArtifact(to)
  }

  return (
    <Dialog open={!!artifact} onOpenChange={(o) => !o && closeArtifact()}>
      <DialogContent size="full" onKeyDown={onKeyDown} aria-describedby={undefined}>
        {artifact && (
          <>
            <ViewerHeader a={artifact} index={index} total={ids.length} prev={prev} next={next} />
            <div className="min-h-0 flex-1">
              <ViewerBody key={artifact.id} a={artifact} />
            </div>
            <ViewerFooter a={artifact} after={next ?? prev} />
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function ViewerHeader({
  a,
  index,
  total,
  prev,
  next,
}: {
  a: Artifact
  index: number
  total: number
  prev: string | null
  next: string | null
}) {
  const src = sourceName(a)
  return (
    <div className="flex shrink-0 items-center gap-3 border-b border-border py-3 pr-12 pl-3">
      <div className="flex items-center">
        <Tooltip
          content={
            <>
              Previous <Kbd className="ml-1">←</Kbd>
            </>
          }
        >
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Previous artifact"
            disabled={!prev}
            onClick={() => prev && stepArtifact(prev)}
          >
            <ChevronLeft />
          </Button>
        </Tooltip>
        <Tooltip
          content={
            <>
              Next <Kbd className="ml-1">→</Kbd>
            </>
          }
        >
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Next artifact"
            disabled={!next}
            onClick={() => next && stepArtifact(next)}
          >
            <ChevronRight />
          </Button>
        </Tooltip>
      </div>
      <FileIcon fileName={a.file_name} contentType={a.content_type} size="sm" />
      <div className="min-w-0 flex-1">
        <DialogPrimitive.Title className="truncate text-[14.5px] leading-5 font-semibold">
          {a.title}
        </DialogPrimitive.Title>
        <div className="flex min-w-0 items-center gap-1.5 text-[12px] text-muted-foreground">
          <span className="truncate">
            {a.file_name} · {bytes(a.size)}
          </span>
          {src && (
            <>
              <span>·</span>
              <Tooltip content={src.chain + (a.run_id ? '\nClick to open the run’s log' : '')}>
                {a.run_id ? (
                  <button
                    type="button"
                    className="min-w-0 truncate rounded outline-none hover:text-accent-text hover:underline focus-visible:ring-2 focus-visible:ring-accent/40"
                    onClick={() => {
                      closeArtifact()
                      openRunLog(a.run_id!)
                    }}
                  >
                    from {src.name}
                  </button>
                ) : (
                  <span className="min-w-0 truncate">from {src.name}</span>
                )}
              </Tooltip>
            </>
          )}
          <span>·</span>
          <Tooltip content={fullTime(a.published_at)}>
            <span className="shrink-0">{relTime(a.published_at)}</span>
          </Tooltip>
        </div>
      </div>
      {total > 1 && index >= 0 && (
        <span className="shrink-0 text-[12px] text-subtle-foreground tabular-nums">
          {index + 1} of {total}
        </span>
      )}
      <Tooltip content="Save a copy">
        <Button variant="ghost" size="icon-sm" aria-label="Download" onClick={() => downloadArtifact(a)}>
          <Download />
        </Button>
      </Tooltip>
      <Button size="sm" onClick={() => openArtifactExternal(a)}>
        <ExternalLink />
        Open externally
      </Button>
    </div>
  )
}

// The footer offers what is left to do with the artifact: delete it, or pick
// up the chat that published it. Closing is the X and Esc, not a button here.
function ViewerFooter({a, after}: {a: Artifact; after: string | null}) {
  const {terminal} = usePlatform()
  const run = useRuns().data?.find((r) => r.run_id === a.run_id)
  const [continuing, setContinuing] = useState(false)

  // "Continue in Chat…" resumes the session that published the artifact, the
  // way the log sheet does. Artifacts outlive runs, so the button only shows
  // while the producing run is in history and resumable (finished, with a
  // session id and a working dir).
  const continueRun = async () => {
    if (!run) return
    setContinuing(true)
    try {
      await api('POST', `/api/runs/${encodeURIComponent(run.run_id)}/continue`)
      toast.success(`Opening ${terminal}…`)
    } catch (e) {
      toast.error('Continue failed', {description: errorMessage(e)})
    } finally {
      setContinuing(false)
    }
  }

  const remove = () => deleteArtifact(a, () => (after ? stepArtifact(after) : closeArtifact()))

  return (
    <DialogFooter className="justify-between">
      <Button variant="danger-ghost" size="sm" onClick={remove}>
        <Trash2 />
        Delete
      </Button>
      <div className="flex items-center gap-2">
        {canContinue(run) && (
          <Tooltip
            content={`Opens ${terminal} in the task’s folder and resumes the chat that published this artifact — with the full conversation context`}
          >
            <Button onClick={continueRun} loading={continuing}>
              {!continuing && <SquareTerminal />}
              Continue in Chat…
            </Button>
          </Tooltip>
        )}
      </div>
    </DialogFooter>
  )
}
