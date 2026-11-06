import type {FeedbackInfo} from '@/lib/types'

// Starting points for an empty chat; a click puts one into the composer.
export const EXAMPLES = [
  'A task I scheduled for tonight did not run',
  'I would like to pause a whole group at once',
  'The Log shows a wrong duration for a resumed run',
]

// envLine is what the daemon appends to the issue body ("DispatchQ 1.2 · macOS
// 15.1"), stated in the review step so the user knows what travels along. A
// macOS version is a bare number. Other systems report their full name.
export function envLine(
  info: Pick<FeedbackInfo, 'app_version' | 'os_version'> | undefined,
  isMac = true,
): string {
  if (!info) return ''
  const os = info.os_version && (isMac ? 'macOS ' + info.os_version : info.os_version)
  return [info.app_version && 'DispatchQ ' + info.app_version, os].filter(Boolean).join(' · ')
}

// manualBody seeds the hand-written issue with everything the user already
// said, so a failed assistant never costs them their words.
export function manualBody(said: string[]): string {
  return said.join('\n\n')
}

// Only a web link may leave the app: the URL is built by the daemon, but it
// ends up in the system's opener.
export function httpsOnly(url: string | undefined): string {
  return /^https?:\/\//i.test(url ?? '') ? (url as string) : ''
}
