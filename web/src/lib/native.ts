// What the macOS app window (cmd/dispatchqapp) binds into the page. Every member
// is optional: in a plain browser tab none of them exist, and the UI has to
// work there too.
// What a click on a macOS notification hands the page: the inbox entry's id
// and what it is about.
export interface NotificationTarget {
  id?: string
  run_id?: string
  artifact_id?: string
  url?: string
}

declare global {
  interface Window {
    cqOpenExternal?: (url: string) => void
    // Opens the notification settings of the OS the window runs on; rejects
    // with the reason when the OS refused.
    cqOpenNotificationSettings?: () => Promise<void>
    cqSetAppearance?: (mode: string) => void
    cqReadAccent?: () => Promise<string>
    cqTakePendingNotification?: () => Promise<NotificationTarget | null>
    // Published by the page for the native side to call.
    openAdd?: () => void
    select?: (view: string) => void
    cqRefresh?: () => void
    cqOpenNotificationTarget?: (t: NotificationTarget) => void
    cqOpenPendingNotification?: () => void
    cqSystemAccent?: string
  }
}

export const isAppWindow = () => typeof window.cqOpenExternal === 'function'

// openExternal sends a link to the default browser from the app window, where
// WKWebView would ignore target=_blank; in a browser it is a new tab.
export function openExternal(url: string) {
  if (window.cqOpenExternal) window.cqOpenExternal(url)
  else window.open(url, '_blank', 'noopener')
}
