import {isAppWindow} from '@/lib/native'
import {keys, queryClient, useHealthPlatform} from '@/lib/queries'
import type {Health} from '@/lib/types'

// The words the dashboard uses for the machine the daemon runs on. macOS keeps
// the wording DispatchQ has always had. Other systems get neutral words.
export interface PlatformWords {
  isMac: boolean
  // "Mac" / "computer"
  device: string
  // "this Mac" / "this computer"
  machine: string
  // Whether the new-task and refresh shortcuts (⌘N, ⌘R) work. Only the native
  // window's menu handles them. A browser tab keeps Ctrl+N for a new window,
  // and the page itself has no handler for either.
  appShortcuts: boolean
  // The shortcut modifier in front of a key: "⌘" / "Ctrl+" (as in "⌘K")
  mod: string
  // The shortcut modifier as a key cap of its own: "⌘" / "Ctrl"
  modKey: string
  // "Finder" / "the file manager"
  fileManager: string
  // "Terminal" / "a terminal"
  terminal: string
}

const mac: PlatformWords = {
  isMac: true,
  device: 'Mac',
  machine: 'this Mac',
  appShortcuts: true,
  mod: '⌘',
  modKey: '⌘',
  fileManager: 'Finder',
  terminal: 'Terminal',
}

const other: PlatformWords = {
  isMac: false,
  device: 'computer',
  machine: 'this computer',
  appShortcuts: false,
  mod: 'Ctrl+',
  modKey: 'Ctrl',
  fileManager: 'the file manager',
  terminal: 'a terminal',
}

// An unknown platform (a daemon too old to say, or health not loaded yet) is
// macOS, the only system older daemons ran on.
export function platformWords(platform: string | undefined): PlatformWords {
  if (!platform || platform === 'darwin') return mac
  return isAppWindow() ? {...other, appShortcuts: true} : other
}

export const usePlatform = () => platformWords(useHealthPlatform())

// For code outside React components: the last answer of the health poll.
export const currentPlatform = () => platformWords(queryClient.getQueryData<Health>(keys.health)?.platform)
