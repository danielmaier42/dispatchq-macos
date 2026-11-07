import type {ReactNode} from 'react'
import {Badge} from '@/components/ui/badge'
import {Skeleton} from '@/components/ui/skeleton'
import {Tooltip} from '@/components/ui/tooltip'
import {keyCombo} from '@/lib/platform'

// SectionTitle heads the content side with what the open section is for.
export function SectionTitle({title, description}: {title: string; description: string}) {
  return (
    <div className="mb-5">
      <h2 className="text-[15px] font-semibold tracking-[-0.01em]">{title}</h2>
      <p className="text-[12.5px] text-muted-foreground">{description}</p>
    </div>
  )
}

// SaveMode tells a group's title how its changes are written, so nobody has
// to guess whether a click already counted.
export function SaveMode({mode, dirty}: {mode: 'instant' | 'draft'; dirty?: boolean}) {
  if (mode === 'draft' && dirty)
    return (
      <Badge tone="accent" dot>
        Unsaved
      </Badge>
    )
  return (
    <Tooltip
      content={
        mode === 'instant'
          ? 'Changes here apply as soon as you make them'
          : `Typed changes wait for Save (${keyCombo('S')})`
      }
    >
      <span className="cursor-default text-[11.5px] text-subtle-foreground">
        {mode === 'instant' ? 'Applies instantly' : 'Needs Save'}
      </span>
    </Tooltip>
  )
}

export function GroupTitle({children, aside}: {children: ReactNode; aside?: ReactNode}) {
  return (
    <span className="flex items-center gap-2">
      {children}
      {aside}
    </span>
  )
}

export function SettingsSkeleton() {
  return (
    <div className="space-y-6">
      {[3, 2].map((rows, i) => (
        <div key={i}>
          <Skeleton className="mb-3 h-4 w-40" />
          <div className="divide-y divide-border rounded-lg border border-border bg-surface">
            {Array.from({length: rows}, (_, j) => (
              <div key={j} className="flex items-center gap-4 px-4 py-3.5">
                <div className="flex-1 space-y-2">
                  <Skeleton className="h-3.5 w-48" />
                  <Skeleton className="h-3 w-72" />
                </div>
                <Skeleton className="h-5 w-9 rounded-full" />
              </div>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}
