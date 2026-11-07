import type {HostInfo, NotifyStatus} from '@/lib/types'

// NotifyCopy words the notification hints for the system the daemon runs on
// (GET /api/host). macOS keeps its own wording, which the components render
// as before; Windows and an unknown system get the strings below. Windows has
// no Banners/Alerts style and no permission prompt: a blocked state is fixed
// in Settings > System > Notifications, or by reinstalling when DispatchQ's
// Start menu registration is broken (see notify_status in GET /api/health).
export interface NotifyCopy {
  // mac is true once the daemon has said it runs on macOS.
  mac: boolean
  // settingsLabel names the app the settings button opens.
  settingsLabel: string
  blockedTitle: (status: NotifyStatus | undefined) => string
  // blockedBody is the explanation off a Mac; on a Mac the components keep
  // their existing text. Both read any status but "denied" as not_determined,
  // since they are only shown while notificationsBlocked() holds.
  blockedBody: (status: NotifyStatus | undefined) => string
  lingerLabel: string
  lingerDescription: string
}

const MAC: NotifyCopy = {
  mac: true,
  settingsLabel: 'System Settings',
  blockedTitle: () => "macOS is not showing DispatchQ's notifications",
  blockedBody: () => '',
  lingerLabel: 'Keep alerts on screen',
  lingerDescription: 'macOS decides how long a notification lingers: pick Alerts, not Banners',
}

const WINDOWS: NotifyCopy = {
  mac: false,
  settingsLabel: 'Settings',
  blockedTitle: (status) =>
    status === 'denied'
      ? "Windows is not showing DispatchQ's notifications"
      : "DispatchQ isn't registered for Windows notifications",
  blockedBody: (status) =>
    status === 'denied'
      ? 'Run outcomes and new artifacts are announced, but Windows is set to hide them. Turn DispatchQ back on under Settings > System > Notifications. If the switch is greyed out, a group policy turns notifications off and an administrator has to change it.'
      : "Run outcomes and new artifacts stay silent because DispatchQ's Start menu entry is missing or damaged. Reinstall DispatchQ to restore it; your tasks and history stay.",
  lingerLabel: 'How notifications appear',
  lingerDescription: 'Windows decides how long a notification stays on screen',
}

const OTHER: NotifyCopy = {
  mac: false,
  settingsLabel: 'System settings',
  blockedTitle: () => "The system is not showing DispatchQ's notifications",
  blockedBody: () =>
    'Run outcomes and new artifacts are announced, but the system hides them. Allow notifications for DispatchQ in its settings.',
  lingerLabel: 'How notifications appear',
  lingerDescription: 'The system decides how long a notification stays on screen',
}

export function notifyCopy(host: HostInfo | undefined): NotifyCopy {
  if (host?.os === 'darwin') return MAC
  return host?.os === 'windows' ? WINDOWS : OTHER
}
