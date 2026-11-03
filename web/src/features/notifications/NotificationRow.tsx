import {
  Bell,
  ChevronRight,
  CircleCheck,
  CircleX,
  Eye,
  FileText,
  MessageSquareText,
  TriangleAlert,
  type LucideIcon,
} from 'lucide-react'
import {Button} from '@/components/ui/button'
import {Tooltip} from '@/components/ui/tooltip'
import {fullTime, relTime} from '@/lib/format'
import type {AppNotification} from '@/lib/types'
import {cn} from '@/lib/utils'
import {kindLabel, shortMessage, shortTitle, targetLabel} from './logic'
import {markNotificationRead, openTarget} from './open-target'

// The icon says at a glance what kind of notification a row is.
const KIND_LOOK: Record<string, {icon: LucideIcon; tone: string}> = {
  success: {icon: CircleCheck, tone: 'bg-success-soft text-success'},
  failure: {icon: CircleX, tone: 'bg-danger-soft text-danger'},
  artifact: {icon: FileText, tone: 'bg-accent-soft text-accent-text'},
  provider: {icon: TriangleAlert, tone: 'bg-warning-soft text-warning'},
  task: {icon: MessageSquareText, tone: 'bg-info-soft text-info'},
}
const OTHER = {icon: Bell, tone: 'bg-surface-3 text-muted-foreground'}

// A row does what clicking the macOS notification does: it turns read and what
// it is about opens. The read mark is shown at once, and the target is not
// kept waiting for the daemon's answer.
function openNotification(n: AppNotification) {
  if (n.unread) void markNotificationRead(n.id)
  openTarget(n)
}

// One notification as one row: the kind's icon with the unread dot on its
// corner, the title with the message under it (the title keeps its width and
// the task name beside it gives way first), when it was sent and where a click leads; the
// mark-read eye shows while the pointer or focus is on the row. The whole row
// opens the notification through a stretched button under the title, so the
// row holds no button inside a button and the eye stays its own target. The
// stretched button is what ↑/↓ move between. The icon, the time and the
// chevron sit above it for their tooltips; a click on them still opens the
// row, which the row's own handler sees for anything that is not a button.
export function NotificationRow({n}: {n: AppNotification}) {
  const {icon: Icon, tone} = KIND_LOOK[n.kind] ?? OTHER
  const target = targetLabel(n)
  const message = shortMessage(n.message, n.task_name)
  return (
    <div
      data-notification-row
      onClick={(e) => {
        if (!(e.target as Element).closest('button')) openNotification(n)
      }}
      className="group relative flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors focus-within:bg-surface-2 hover:bg-surface-2"
    >
      <Tooltip content={kindLabel(n.kind)}>
        <div
          className={cn(
            'relative z-[1] mt-px flex size-7 shrink-0 items-center justify-center rounded-lg [&_svg]:size-3.5',
            tone,
          )}
        >
          <Icon />
          {n.unread && (
            <span
              aria-label="Unread"
              className="absolute -top-0.5 -right-0.5 size-2 rounded-full bg-accent ring-2 ring-surface"
            />
          )}
        </div>
      </Tooltip>
      <div className="min-w-0 flex-1">
        <div className="flex items-baseline gap-2">
          <button
            type="button"
            data-notification-open
            onClick={() => openNotification(n)}
            className={cn(
              "max-w-full shrink-0 truncate text-left text-[13px] leading-5 outline-none after:absolute after:inset-0 after:content-[''] focus-visible:after:ring-2 focus-visible:after:ring-accent/40 focus-visible:after:ring-inset",
              n.unread ? 'font-semibold' : 'font-medium',
            )}
          >
            {shortTitle(n.title)}
          </button>
          {n.task_name && (
            <span className="min-w-0 shrink truncate text-[12px] text-subtle-foreground">{n.task_name}</span>
          )}
        </div>
        {/* The message as the notification showed it, line breaks kept, cut
            after a few lines so one long-winded task does not take the page. */}
        {message && (
          <p className="line-clamp-4 text-[12.5px] leading-[18px] whitespace-pre-line text-muted-foreground">
            {message}
          </p>
        )}
      </div>
      <div className="flex h-7 shrink-0 items-center gap-1">
        <div className="relative z-[1] flex items-center opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100">
          {n.unread && (
            <Tooltip content="Mark read">
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label="Mark read"
                onClick={() => void markNotificationRead(n.id)}
              >
                <Eye />
              </Button>
            </Tooltip>
          )}
        </div>
        <Tooltip content={fullTime(n.sent_at)}>
          <span className="relative z-[1] w-16 text-right text-[12px] text-subtle-foreground tabular-nums">
            {relTime(n.sent_at)}
          </span>
        </Tooltip>
        {target ? (
          <Tooltip content={target}>
            <span className="relative z-[1] flex size-5 items-center justify-center text-subtle-foreground opacity-50 transition-opacity group-hover:opacity-100">
              <ChevronRight className="size-4" />
            </span>
          </Tooltip>
        ) : (
          <span aria-hidden className="size-5" />
        )}
      </div>
    </div>
  )
}
