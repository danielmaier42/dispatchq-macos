import {MessageSquareText} from 'lucide-react'
import {EXAMPLES} from '../feedback'

// The empty chat: what happens to the message, and a few ways to start. The
// chips are examples, not a menu: picking one only fills the composer.
export function FeedbackIntro({
  repo,
  maxTurns,
  onPick,
}: {
  repo: string
  maxTurns: number
  onPick: (text: string) => void
}) {
  const questions = Math.max(maxTurns - 1, 0)
  return (
    <div className="flex animate-in flex-col items-center px-4 pt-10 pb-6 text-center">
      <div className="mb-4 flex size-12 items-center justify-center rounded-2xl bg-ai-soft text-ai [&_svg]:size-6">
        <MessageSquareText />
      </div>
      <h2 className="text-[15px] font-semibold">What would you like to report?</h2>
      <p className="mt-1 max-w-md text-[13px] text-muted-foreground">
        Something that does not work, or something DispatchQ should be able to do. Claude turns it into a
        GitHub issue for <span className="font-mono text-[12px]">{repo || 'GitHub'}</span>
        {questions > 0 && (
          <>
            {' '}
            and may ask up to {questions === 1 ? 'one short question' : `${questions} short questions`} first
          </>
        )}
        . Nothing reaches GitHub until you file it yourself.
      </p>
      <div className="mt-5 flex flex-wrap items-center justify-center gap-2">
        <span className="text-[12.5px] text-subtle-foreground">For example:</span>
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
