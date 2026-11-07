import {Check} from 'lucide-react'
import type {CSSProperties, KeyboardEvent} from 'react'
import {APPEARANCES, type Appearance} from '@/lib/theme'
import {cn} from '@/lib/utils'

// Each tile depicts a scheme whatever the window shows right now, so a mockup
// carries its own copy of that scheme's surface tokens (mirroring styles.css)
// and draws with the usual token classes inside. The accent is the live one.
const SCHEME: Record<'light' | 'dark', CSSProperties> = {
  light: {
    '--background': 'oklch(0.985 0.002 260)',
    '--foreground': 'oklch(0.2 0.01 260)',
    '--surface': 'oklch(1 0 0)',
    '--surface-3': 'oklch(0.94 0.004 260)',
    '--sidebar': 'oklch(0.965 0.004 260)',
    '--border': 'oklch(0.91 0.004 260)',
  } as CSSProperties,
  dark: {
    '--background': 'oklch(0.17 0.004 260)',
    '--foreground': 'oklch(0.96 0.003 260)',
    '--surface': 'oklch(0.21 0.005 260)',
    '--surface-3': 'oklch(0.28 0.006 260)',
    '--sidebar': 'oklch(0.19 0.005 260)',
    '--border': 'oklch(0.29 0.006 260)',
  } as CSSProperties,
}

const TIPS: Record<string, string> = {
  '': 'Follows the system: light or dark, as its appearance setting says',
  light: 'Always light, whatever the system does',
  dark: 'Always dark, whatever the system does',
}

function WindowMock({scheme, className}: {scheme: 'light' | 'dark'; className?: string}) {
  return (
    <div style={SCHEME[scheme]} className={cn('absolute inset-0 flex bg-background', className)}>
      <div className="flex w-[30%] flex-col gap-1 border-r border-border bg-sidebar p-1.5 pt-3">
        <div className="mb-0.5 flex gap-0.5">
          {[0, 1, 2].map((i) => (
            <span key={i} className="size-1 rounded-full bg-foreground/20" />
          ))}
        </div>
        <span className="h-1.5 rounded-sm bg-accent" />
        <span className="h-1.5 w-4/5 rounded-sm bg-foreground/15" />
        <span className="h-1.5 w-3/5 rounded-sm bg-foreground/15" />
        <span className="h-1.5 w-4/5 rounded-sm bg-foreground/15" />
      </div>
      <div className="flex flex-1 flex-col gap-1.5 p-2 pt-3">
        <span className="h-2 w-1/2 rounded-sm bg-foreground/70" />
        <div className="flex flex-col gap-1 rounded-[3px] border border-border bg-surface p-1.5">
          <span className="h-1.5 w-full rounded-sm bg-foreground/15" />
          <span className="h-1.5 w-2/3 rounded-sm bg-foreground/15" />
        </div>
        <div className="flex flex-col gap-1 rounded-[3px] border border-border bg-surface p-1.5">
          <span className="h-1.5 w-5/6 rounded-sm bg-foreground/15" />
          <span className="flex items-center justify-end">
            <span className="h-2 w-6 rounded-sm bg-accent" />
          </span>
        </div>
      </div>
    </div>
  )
}

// AppearanceTiles picks System, Light or Dark from three window previews. It
// is a radio group: ←/→ move the choice, and the choice applies at once.
export function AppearanceTiles({value, onChange}: {value: Appearance; onChange: (v: Appearance) => void}) {
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step =
      e.key === 'ArrowRight' || e.key === 'ArrowDown'
        ? 1
        : e.key === 'ArrowLeft' || e.key === 'ArrowUp'
          ? -1
          : 0
    if (!step) return
    e.preventDefault()
    const i = APPEARANCES.findIndex((a) => a.value === value)
    const next = (i + step + APPEARANCES.length) % APPEARANCES.length
    onChange(APPEARANCES[next].value)
    e.currentTarget.querySelectorAll<HTMLButtonElement>('[role=radio]')[next]?.focus()
  }

  return (
    <div role="radiogroup" aria-label="Appearance" onKeyDown={onKeyDown} className="grid grid-cols-3 gap-3">
      {APPEARANCES.map((a) => {
        const on = a.value === value
        return (
          <button
            key={a.value || 'system'}
            type="button"
            role="radio"
            aria-checked={on}
            tabIndex={on ? 0 : -1}
            onClick={() => onChange(a.value)}
            className="group flex flex-col items-center gap-2 rounded-lg outline-none"
          >
            <div
              className={cn(
                'relative aspect-[16/10] w-full overflow-hidden rounded-lg border shadow-xs transition-[box-shadow,border-color]',
                on
                  ? 'border-accent ring-3 ring-accent/30'
                  : 'border-border group-hover:border-border-strong group-focus-visible:ring-3 group-focus-visible:ring-accent/30',
              )}
            >
              {a.value === '' ? (
                <>
                  <WindowMock scheme="light" />
                  <WindowMock
                    scheme="dark"
                    className="[clip-path:polygon(62%_0,100%_0,100%_100%,38%_100%)]"
                  />
                </>
              ) : (
                <WindowMock scheme={a.value} />
              )}
            </div>
            <span className="flex items-center gap-1.5 text-[12.5px] font-medium">
              <span
                className={cn(
                  'flex size-3.5 items-center justify-center rounded-full border',
                  on ? 'border-accent bg-accent text-accent-foreground' : 'border-border-strong',
                )}
              >
                {on && <Check className="size-2.5" strokeWidth={3.5} />}
              </span>
              {a.label}
            </span>
            <span className="sr-only">{TIPS[a.value]}</span>
          </button>
        )
      })}
    </div>
  )
}
