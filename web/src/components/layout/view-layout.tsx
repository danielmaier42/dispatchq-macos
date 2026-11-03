import type {ReactNode} from 'react'
import {cn} from '@/lib/utils'

// ViewLayout is the frame every view uses: a header with the title and the
// view's actions, an optional toolbar row under it (filters), then the
// scrolling content. Views never position their own chrome. When the actions
// leave the title less than 18rem, they move to a line of their own.
export function ViewLayout({
  title,
  subtitle,
  actions,
  toolbar,
  children,
  className,
  contentClassName,
  wide,
}: {
  title: ReactNode
  subtitle?: ReactNode
  actions?: ReactNode
  toolbar?: ReactNode
  children: ReactNode
  className?: string
  contentClassName?: string
  // wide drops the reading-width cap, for dense tables.
  wide?: boolean
}) {
  return (
    <div className={cn('flex h-full min-h-0 flex-col', className)}>
      <header className="shrink-0 border-b border-border bg-background/80 backdrop-blur-xl">
        <div
          className={cn(
            'mx-auto flex min-h-14 w-full flex-wrap items-center gap-x-3 gap-y-2 px-6 py-2.5',
            !wide && 'max-w-[1120px]',
          )}
        >
          <div className="min-w-0 flex-[1_1_18rem]">
            <h1 className="truncate text-[17px] leading-6 font-semibold tracking-[-0.01em]">{title}</h1>
            {subtitle && <p className="truncate text-[12.5px] text-muted-foreground">{subtitle}</p>}
          </div>
          {actions && (
            <div className="ml-auto flex max-w-full shrink-0 flex-wrap items-center justify-end gap-2">
              {actions}
            </div>
          )}
        </div>
        {toolbar && (
          <div
            className={cn(
              'mx-auto flex w-full flex-wrap items-center gap-2 px-6 pb-2.5',
              !wide && 'max-w-[1120px]',
            )}
          >
            {toolbar}
          </div>
        )}
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div className={cn('mx-auto w-full px-6 py-5', !wide && 'max-w-[1120px]', contentClassName)}>
          {children}
        </div>
      </div>
    </div>
  )
}

// Section is a titled block inside a view (Dashboard cards, Settings groups).
export function Section({
  title,
  description,
  actions,
  children,
  className,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <section className={cn('mb-7 last:mb-0', className)}>
      {(title || actions) && (
        <div className="mb-2.5 flex items-end gap-3">
          <div className="min-w-0 flex-1">
            {title && <h2 className="text-[13px] font-semibold">{title}</h2>}
            {description && <p className="text-[12.5px] text-muted-foreground">{description}</p>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </div>
      )}
      {children}
    </section>
  )
}

// DayHeading heads one day's entries in a list split by day.
export function DayHeading({children}: {children: ReactNode}) {
  return (
    <div className="sticky top-0 z-[1] -mx-1 mb-1.5 bg-background/90 px-1 pt-3 pb-1.5 text-[11.5px] font-semibold tracking-wide text-subtle-foreground uppercase backdrop-blur first:pt-0">
      {children}
    </div>
  )
}
