import {PenLine, RotateCcw, RefreshCw, Settings2} from 'lucide-react'
import {useEffect} from 'react'
import {openSettings} from '@/app/nav'
import {ViewLayout} from '@/components/layout/view-layout'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {confirm} from '@/components/ui/confirm'
import {Skeleton} from '@/components/ui/skeleton'
import {errorMessage} from '@/lib/api'
import {useFeedbackInfo} from '@/lib/queries'
import {ChatThread} from './components/ChatThread'
import {Composer} from './components/Composer'
import {DraftForm} from './components/DraftForm'
import {FeedbackIntro} from './components/FeedbackIntro'
import {envLine} from './feedback'
import {retry, sendMessage, setComposer, startOver, useFeedback, writeManually} from './store'
import {usePlatform} from '@/lib/platform'

export function FeedbackView() {
  const {isMac} = usePlatform()
  const info = useFeedbackInfo()
  const s = useFeedback()

  // Whether the assistant can run changes with Settings, so it is asked again
  // every time the page opens.
  useEffect(() => {
    void info.refetch()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const available = !info.isError && (info.data?.available ?? false)
  const reason = info.isError ? errorMessage(info.error) : info.data?.reason || 'unknown reason'
  const repo = info.data?.repo ?? ''
  const maxTurns = info.data?.max_turns ?? 3
  const loaded = !info.isPending
  const started = s.messages.length > 0 || s.mode === 'review'

  // Without the assistant the page is the plain form from the start.
  useEffect(() => {
    if (loaded && !available && s.mode === 'chat' && !s.messages.length) writeManually()
  }, [loaded, available, s.mode, s.messages.length])

  const reset = async () => {
    const dirty = s.composer.trim() || s.said.length || s.title.trim() || s.body.trim()
    if (
      dirty &&
      !(await confirm({
        title: 'Start over?',
        description: 'The conversation and the draft are discarded.',
        confirmLabel: 'Start over',
        destructive: true,
      }))
    )
      return
    startOver()
    void info.refetch()
  }

  const next = s.turns + 1
  const hint =
    s.mode !== 'chat' || s.turns === 0 || next > maxTurns
      ? undefined
      : next === maxTurns
        ? 'Last message: Claude drafts the issue after this one'
        : `Message ${next} of ${maxTurns}`

  return (
    <ViewLayout
      title="Feedback"
      subtitle="Report a problem or suggest an idea as a GitHub issue"
      actions={
        started && (
          <Button variant="ghost" onClick={() => void reset()} disabled={s.busy}>
            <RotateCcw />
            Start over
          </Button>
        )
      }
      contentClassName="max-w-[760px]"
    >
      {!loaded ? (
        <div className="flex flex-col items-center gap-3 pt-12">
          <Skeleton className="size-12 rounded-2xl" />
          <Skeleton className="h-4 w-56" />
          <Skeleton className="h-3 w-80" />
        </div>
      ) : (
        <div className="flex flex-col gap-4">
          {!available && (
            <Alert
              tone="warning"
              title="The assistant is unavailable"
              actions={
                /Settings/.test(reason) && (
                  <Button size="sm" onClick={() => openSettings('assistant')}>
                    <Settings2 />
                    Open Settings
                  </Button>
                )
              }
            >
              {reason.charAt(0).toUpperCase() + reason.slice(1)}. Write the issue yourself below.
            </Alert>
          )}

          {s.messages.length === 0 && s.mode === 'chat' && (
            <FeedbackIntro repo={repo} maxTurns={maxTurns} onPick={setComposer} />
          )}
          {(s.messages.length > 0 || s.busy) && <ChatThread messages={s.messages} typing={s.busy} />}

          {s.mode === 'chat' &&
            (s.failed ? (
              <Alert
                tone="danger"
                title="The assistant could not draft the issue"
                actions={
                  <>
                    <Button size="sm" onClick={retry}>
                      <RefreshCw />
                      Try again
                    </Button>
                    {/* The fallback keeps what was typed and lets the user
                        finish the issue by hand. */}
                    <Button size="sm" variant="primary" onClick={writeManually}>
                      <PenLine />
                      Write it myself
                    </Button>
                  </>
                }
              >
                Nothing you wrote is lost: try again, or turn it into the issue yourself.
              </Alert>
            ) : (
              <Composer
                value={s.composer}
                onChange={setComposer}
                onSend={sendMessage}
                busy={s.busy}
                error={s.error}
                hint={hint}
                placeholder={
                  s.messages.some((m) => m.role === 'assistant')
                    ? 'Answer Claude’s question…'
                    : 'Something that does not work, or something DispatchQ should be able to do…'
                }
              />
            ))}

          {s.mode === 'review' && <DraftForm repo={repo} env={envLine(info.data, isMac)} />}
        </div>
      )}
    </ViewLayout>
  )
}
