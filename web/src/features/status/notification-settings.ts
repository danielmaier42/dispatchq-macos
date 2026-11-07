import {toast} from '@/components/ui/toaster'
import {errorMessage} from '@/lib/api'
import type {HostInfo} from '@/lib/types'

// Where the operator finds DispatchQ's notification switch, by the system the
// daemon runs on (GET /api/host, see useHost). The app window opens that place
// itself; everywhere else (a browser tab, a window without the hook) these
// words are the action. Before the host has answered they stay generic.
export function notificationSettingsHint(host: HostInfo | undefined): string {
  switch (host?.os) {
    case 'darwin':
      return 'Open System Settings → Notifications → DispatchQ'
    case 'windows':
      return 'Open Settings → System → Notifications → DispatchQ'
    default:
      return "Open your system's notification settings and allow DispatchQ"
  }
}

export type NotificationSettingsOutcome = 'opened' | 'manual' | 'failed'

// openNotificationSettings asks the app window to open the OS notification
// settings. It never does nothing: without the window hook it says where to
// go, and a refusal from the OS shows the reason together with the way there.
export async function openNotificationSettings(
  host: HostInfo | undefined,
): Promise<NotificationSettingsOutcome> {
  const hint = notificationSettingsHint(host)
  const open = window.cqOpenNotificationSettings
  if (typeof open !== 'function') {
    toast(hint)
    return 'manual'
  }
  try {
    await open()
    return 'opened'
  } catch (e) {
    toast.error(`Could not open the notification settings: ${errorMessage(e)}`, {description: hint})
    return 'failed'
  }
}
