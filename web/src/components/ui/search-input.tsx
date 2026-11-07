import {Search, X} from 'lucide-react'
import {useEffect, useRef, useState} from 'react'
import {keyCombo} from '@/lib/platform'
import {cn} from '@/lib/utils'
import {inputClass} from './input'
import {Kbd} from './kbd'

// SearchInput filters as you type, after a short pause so a fast typist does not
// rebuild a long list on every key. Escape clears it; a second Escape leaves it.
// With `hotkey`, ⌘F (Ctrl+F off a Mac) focuses it while no dialog is open.
export function SearchInput({
  value,
  onChange,
  placeholder = 'Search',
  hotkey,
  className,
  delay = 120,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
  hotkey?: boolean
  className?: string
  delay?: number
}) {
  const [text, setText] = useState(value)
  const ref = useRef<HTMLInputElement>(null)
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)

  // A reset from outside (Clear filters) replaces what was typed.
  const [shown, setShown] = useState(value)
  if (shown !== value) {
    setShown(value)
    setText(value)
  }
  useEffect(() => () => clearTimeout(timer.current), [])

  useEffect(() => {
    if (!hotkey) return
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key !== 'f') return
      if (document.querySelector('[role=dialog]')) return
      e.preventDefault()
      ref.current?.focus()
      ref.current?.select()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [hotkey])

  const commit = (v: string, now = false) => {
    setText(v)
    clearTimeout(timer.current)
    if (now) onChange(v)
    else timer.current = setTimeout(() => onChange(v), delay)
  }

  return (
    <div className={cn('relative w-64 min-w-0', className)}>
      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-subtle-foreground" />
      <input
        ref={ref}
        type="text"
        value={text}
        aria-label={placeholder}
        placeholder={placeholder}
        onChange={(e) => commit(e.target.value)}
        onKeyDown={(e) => {
          if (e.key !== 'Escape') return
          e.preventDefault()
          e.stopPropagation()
          if (text) commit('', true)
          else ref.current?.blur()
        }}
        className={cn(inputClass, 'peer h-8 pr-14 pl-8')}
      />
      {text ? (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => commit('', true)}
          className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded p-1 text-subtle-foreground hover:bg-surface-3 hover:text-foreground"
        >
          <X className="size-3.5" />
        </button>
      ) : (
        hotkey && (
          <Kbd className="pointer-events-none absolute top-1/2 right-1.5 -translate-y-1/2 peer-focus:hidden">
            {keyCombo('F')}
          </Kbd>
        )
      )}
    </div>
  )
}
