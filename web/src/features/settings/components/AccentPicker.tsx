import {Check, Monitor, Pipette} from 'lucide-react'
import {useEffect, useRef, type ReactNode} from 'react'
import {Tooltip} from '@/components/ui/tooltip'
import {ACCENT_PRESETS, readableOn, useTheme} from '@/lib/theme'
import {useSaveAccent} from '@/features/settings/use-appearance'
import {cn} from '@/lib/utils'

// AccentPicker offers the Mac's accent, the macOS palette and any colour. The
// theme store repaints on every change, so the window itself is the preview;
// the custom picker repaints while dragging and saves once it settles.
export function AccentPicker() {
  const accent = useTheme((s) => s.accent)
  const systemAccent = useTheme((s) => s.systemAccent)
  const saveAccent = useSaveAccent()
  // pending is the custom colour still waiting for its save. Leaving the view
  // writes it at once instead of dropping it, and any other pick replaces it.
  const pending = useRef<{timer: number; hex: string} | null>(null)
  const custom = !!accent && !ACCENT_PRESETS.some((p) => p.hex === accent)

  useEffect(
    () => () => {
      const p = pending.current
      if (!p) return
      window.clearTimeout(p.timer)
      void saveAccent(p.hex)
    },
    [saveAccent],
  )

  const pick = (hex: string) => {
    if (pending.current) window.clearTimeout(pending.current.timer)
    pending.current = null
    void saveAccent(hex)
  }

  const pickCustom = (hex: string) => {
    useTheme.getState().setAccent(hex)
    if (pending.current) window.clearTimeout(pending.current.timer)
    pending.current = {
      hex,
      timer: window.setTimeout(() => {
        pending.current = null
        void saveAccent(hex)
      }, 400),
    }
  }

  return (
    <div role="radiogroup" aria-label="Accent colour" className="flex flex-wrap items-start gap-x-2 gap-y-3">
      <Swatch
        label="System"
        tip={`Follow the system accent colour${systemAccent ? ` (now ${systemAccent})` : ''}`}
        color={systemAccent}
        selected={!accent}
        onSelect={() => pick('')}
        badge={<Monitor className="size-3" />}
      />
      <span aria-hidden className="mx-1 mt-1.5 h-5 w-px bg-border" />
      {ACCENT_PRESETS.map((p) => (
        <Swatch
          key={p.hex}
          label={p.name}
          color={p.hex}
          selected={accent === p.hex}
          onSelect={() => pick(p.hex)}
          hideLabel
        />
      ))}
      <span aria-hidden className="mx-1 mt-1.5 h-5 w-px bg-border" />
      <Tooltip content={custom ? `Custom colour ${accent}` : 'Pick any colour'}>
        <label
          className={cn(
            'relative flex size-8 cursor-pointer items-center justify-center rounded-full border transition-shadow focus-within:ring-3 focus-within:ring-accent/40',
            custom
              ? 'border-transparent ring-2 ring-foreground/70 ring-offset-2 ring-offset-surface'
              : 'border-dashed border-border-strong bg-surface-2 text-muted-foreground hover:text-foreground',
          )}
          style={custom ? {background: accent, color: readableOn(accent)} : undefined}
        >
          {custom ? <Check className="size-3.5" strokeWidth={3} /> : <Pipette className="size-3.5" />}
          <input
            type="color"
            aria-label="Custom accent colour"
            value={accent || systemAccent || '#0a84ff'}
            onChange={(e) => pickCustom(e.target.value)}
            className="absolute inset-0 cursor-pointer opacity-0"
          />
        </label>
      </Tooltip>
    </div>
  )
}

function Swatch({
  label,
  tip,
  color,
  selected,
  onSelect,
  badge,
  hideLabel,
}: {
  label: string
  tip?: string
  color: string
  selected: boolean
  onSelect: () => void
  badge?: ReactNode
  hideLabel?: boolean
}) {
  return (
    <Tooltip content={tip ?? label}>
      <button
        type="button"
        role="radio"
        aria-checked={selected}
        aria-label={label}
        onClick={onSelect}
        className="group flex flex-col items-center gap-1 outline-none"
      >
        <span
          className={cn(
            'relative flex size-8 items-center justify-center rounded-full shadow-xs ring-offset-2 ring-offset-surface transition-shadow group-focus-visible:ring-3 group-focus-visible:ring-accent/40',
            selected ? 'ring-2 ring-foreground/70' : 'group-hover:ring-2 group-hover:ring-border-strong',
          )}
          style={{background: color || 'var(--accent)', color: readableOn(color)}}
        >
          {selected && <Check className="size-3.5" strokeWidth={3} />}
          {badge && (
            <span className="absolute -right-1 -bottom-1 flex size-4 items-center justify-center rounded-full border border-border bg-surface text-muted-foreground">
              {badge}
            </span>
          )}
        </span>
        {!hideLabel && <span className="text-[11px] text-muted-foreground">{label}</span>}
      </button>
    </Tooltip>
  )
}
