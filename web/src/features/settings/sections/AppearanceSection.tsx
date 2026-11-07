import {RowGroup} from '@/components/layout/settings-row'
import {Section} from '@/components/layout/view-layout'
import {Badge} from '@/components/ui/badge'
import {Button} from '@/components/ui/button'
import {Switch} from '@/components/ui/switch'
import {resolvedAccent, useTheme} from '@/lib/theme'
import {useSaveAppearance} from '@/features/settings/use-appearance'
import {AccentPicker} from '../components/AccentPicker'
import {AppearanceTiles} from '../components/AppearanceTiles'
import {SaveMode} from '../components/parts'

// Like the pause switch, the look is applied and written at once: a choice
// about how the window looks is judged by looking at it.
export function AppearanceSection() {
  const appearance = useTheme((s) => s.appearance)
  const accent = useTheme((s) => s.accent)
  const shown = useTheme(resolvedAccent)
  const saveAppearance = useSaveAppearance()

  return (
    <>
      <Section
        title="Appearance"
        description="Follow the system, or keep DispatchQ light or dark whatever the system is doing"
        actions={<SaveMode mode="instant" />}
      >
        <RowGroup className="p-4">
          <AppearanceTiles value={appearance} onChange={(v) => void saveAppearance(v)} />
        </RowGroup>
      </Section>

      <Section
        title="Accent colour"
        description="Buttons, selections and highlights. System follows the accent colour set in the system settings."
        actions={<SaveMode mode="instant" />}
      >
        <RowGroup>
          <div className="p-4">
            <AccentPicker />
          </div>
          <div className="flex flex-wrap items-center gap-3 bg-surface-2/60 px-4 py-3">
            <span className="text-[12px] text-muted-foreground">Preview</span>
            <div aria-hidden className="pointer-events-none flex items-center gap-3">
              <Button variant="primary" size="sm" tabIndex={-1}>
                Run now
              </Button>
              <Switch checked tabIndex={-1} />
              <Badge tone="accent" dot>
                Queued
              </Badge>
              <span className="text-[12.5px] font-medium text-accent-text">Link text</span>
            </div>
            <span className="ml-auto font-mono text-[12px] text-subtle-foreground">
              {accent ? shown : `System · ${shown}`}
            </span>
          </div>
        </RowGroup>
      </Section>
    </>
  )
}
