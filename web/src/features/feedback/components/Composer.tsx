import {ArrowUp} from 'lucide-react'
import {useEffect, useLayoutEffect, useRef} from 'react'
import {Button} from '@/components/ui/button'
import {Kbd} from '@/components/ui/kbd'
import {enterKey, modifierKey} from '@/lib/platform'
import {cn} from '@/lib/utils'

// Composer is the chat input: it grows with its text up to a cap, and
// ⌘↵ sends, like the other multi-line inputs in the app (plain ↵ is a newline).
// A back-and-forth chat (the assistant, the feedback page) sends on plain ↵
// instead, with ⇧↵ for a newline, as chat apps do.
export function Composer({
  value,
  onChange,
  onSend,
  busy,
  placeholder,
  hint,
  error,
  enterSends = false,
  rows = 2,
}: {
  value: string
  onChange: (v: string) => void
  onSend: () => void
  busy: boolean
  placeholder: string
  hint?: string
  error?: string
  enterSends?: boolean
  rows?: number
}) {
  const ref = useRef<HTMLTextAreaElement>(null)

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 240) + 'px'
  }, [value])

  // Back in the user's hands once Claude has answered.
  useEffect(() => {
    if (!busy) ref.current?.focus()
  }, [busy])

  return (
    <div>
      <div
        className={cn(
          'rounded-xl border bg-surface shadow-sm transition-[border-color,box-shadow] focus-within:border-accent focus-within:ring-3 focus-within:ring-accent/20',
          error ? 'border-danger' : 'border-border',
        )}
      >
        <textarea
          ref={ref}
          rows={rows}
          value={value}
          disabled={busy}
          placeholder={placeholder}
          aria-label="Message"
          aria-invalid={!!error}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key !== 'Enter' || e.nativeEvent.isComposing) return
            if (e.metaKey || e.ctrlKey || (enterSends && !e.shiftKey)) {
              e.preventDefault()
              onSend()
            }
          }}
          className="block max-h-60 w-full resize-none bg-transparent px-3.5 pt-3 pb-1 text-[13.5px] leading-relaxed outline-none placeholder:text-subtle-foreground disabled:opacity-60"
        />
        <div className="flex items-center gap-2 px-2.5 pb-2.5">
          <span className="flex-1 truncate pl-1 text-[12px] text-subtle-foreground">{hint}</span>
          <span className="flex items-center gap-0.5 text-[11px] text-subtle-foreground">
            {!enterSends && <Kbd>{modifierKey()}</Kbd>}
            <Kbd>{enterKey()}</Kbd>
          </span>
          <Button size="sm" variant="primary" loading={busy} onClick={onSend} aria-label="Send">
            {!busy && <ArrowUp />}
            Send
          </Button>
        </div>
      </div>
      {error && <p className="mt-1.5 text-[12px] text-danger">{error}</p>}
    </div>
  )
}
