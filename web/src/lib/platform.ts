import {useQuery} from '@tanstack/react-query'
import {get} from './api'
import {isAppWindow} from './native'
import type {HostInfo} from './types'

// Two questions decide what the UI may say about the computer it runs on.
//
// Which keys does the person press? That is the keyboard in front of them, so
// it is read from the browser: Command on a Mac, Control everywhere else. Every
// shortcut handler accepts either modifier (metaKey || ctrlKey), so the label
// only has to name the one this keyboard has.
//
// Which system services does the daemon have? That is the machine it runs on,
// so it comes from GET /api/host. Instructions that only hold on macOS (System
// Settings, the macOS Installer) are shown only when the host says darwin.

type NavigatorWithUAData = Navigator & {userAgentData?: {platform?: string}}

// isApplePlatform reports whether this keyboard has a Command key.
export function isApplePlatform(): boolean {
  if (typeof navigator === 'undefined') return false
  const nav = navigator as NavigatorWithUAData
  const name = nav.userAgentData?.platform || nav.platform || nav.userAgent || ''
  return /mac|iphone|ipad|ipod/i.test(name)
}

// keyCombo labels a shortcut that the page itself handles: "⌘K" or "Ctrl+K".
export function keyCombo(key: string): string {
  return isApplePlatform() ? '⌘' + key : 'Ctrl+' + key
}

// menuKeyCombo labels a shortcut that only the macOS app window's menu bar
// provides (New Task ⌘N, Refresh ⌘R); the page has no handler for it. Anywhere
// else the key does something different or nothing, so there is no label.
export function menuKeyCombo(key: string): string {
  return isApplePlatform() && isAppWindow() ? '⌘' + key : ''
}

// withShortcut appends a combo in parentheses when there is one.
export function withShortcut(label: string, combo: string): string {
  return combo ? `${label} (${combo})` : label
}

// modifierKey and enterKey are the key caps for "⌘ ↵" and "Ctrl Enter".
export const modifierKey = () => (isApplePlatform() ? '⌘' : 'Ctrl')
export const enterKey = () => (isApplePlatform() ? '↵' : 'Enter')

export const hostKey = ['host'] as const

// useHost reads what the daemon's operating system offers. It does not change
// while the daemon runs, so it is read once.
export const useHost = () =>
  useQuery({queryKey: hostKey, queryFn: () => get<HostInfo>('/api/host'), staleTime: Infinity})

// isMacHost is true only once the daemon has said it runs on macOS, so
// macOS-only instructions never flash up on another system while loading.
export const isMacHost = (host: HostInfo | undefined) => host?.os === 'darwin'
