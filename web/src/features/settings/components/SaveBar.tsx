import {CircleDot} from 'lucide-react'
import {useEffect} from 'react'
import {Button} from '@/components/ui/button'
import {Kbd} from '@/components/ui/kbd'
import {keyCombo} from '@/lib/platform'
import {saveDraft} from '../actions'
import {SECTIONS} from '../sections'
import {discardDraft, useSettingsUi} from '../store'

// SaveBar sits at the foot of Settings while typed changes wait to be saved,
// naming where they are. ⌘S saves from anywhere in the view.
export function SaveBar({sections}: {sections: Set<string>}) {
  const saving = useSettingsUi((s) => s.saving)
  const where = SECTIONS.filter((s) => sections.has(s.id)).map((s) => s.label)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 's') {
        e.preventDefault()
        void saveDraft()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="sticky bottom-0 z-10 -mx-2 mt-6 px-2 pb-4">
      <div
        role="region"
        aria-label="Unsaved changes"
        className="flex animate-in items-center gap-3 rounded-xl border border-border-strong bg-surface/95 py-2 pr-2 pl-4 shadow-lg backdrop-blur"
      >
        <CircleDot className="size-4 shrink-0 text-accent-text" />
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium">Unsaved changes</div>
          {where.length > 0 && (
            <div className="truncate text-[12px] text-muted-foreground">in {where.join(', ')}</div>
          )}
        </div>
        <Button variant="ghost" onClick={discardDraft} disabled={saving}>
          Discard
        </Button>
        <Button variant="primary" onClick={() => void saveDraft()} loading={saving}>
          Save
          <Kbd className="ml-0.5 border-accent-foreground/30 bg-transparent text-accent-foreground/80">
            {keyCombo('S')}
          </Kbd>
        </Button>
      </div>
    </div>
  )
}
