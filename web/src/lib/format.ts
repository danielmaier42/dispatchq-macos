// Time and size formatting shared by every view. Dates are shown in the Mac's
// locale; "relative" strings are short and English like the rest of the UI.

export const pad2 = (n: number) => String(n).padStart(2, '0')

// localDate is the calendar day of a timestamp in local time, in the form an
// <input type=date> carries — what a from–to day filter compares against.
export function localDate(iso: string | Date): string {
  const d = new Date(iso)
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
}

export function inDateRange(iso: string, from: string, to: string): boolean {
  const d = localDate(iso)
  return !(from && d < from) && !(to && d > to)
}

export function baseName(p: string): string {
  const parts = String(p).replace(/\/+$/, '').split('/').filter(Boolean)
  return parts.length ? parts[parts.length - 1] : p
}

export function relTime(iso: string | Date, now = Date.now()): string {
  const d = new Date(iso)
  const s = (now - d.getTime()) / 1000
  const a = Math.abs(s)
  const fut = s < 0
  const f = (n: number, u: string) => (fut ? `in ${n} ${u}` : `${n} ${u} ago`)
  if (a < 60) return fut ? 'in a moment' : 'just now'
  if (a < 3600) return f(Math.floor(a / 60), 'min')
  if (a < 86400) return f(Math.floor(a / 3600), 'h')
  if (a < 604800) return f(Math.floor(a / 86400), 'd')
  return d.toLocaleDateString()
}

export function sameDay(a: Date, b: Date) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}

// clockTime is "14:05", with the date in front when it is not today.
export function clockTime(iso: string | Date): string {
  const d = new Date(iso)
  const t = d.toLocaleTimeString(undefined, {hour: '2-digit', minute: '2-digit'})
  return sameDay(d, new Date())
    ? t
    : `${d.toLocaleDateString(undefined, {day: 'numeric', month: 'short'})}, ${t}`
}

export function fullTime(iso: string | Date): string {
  return new Date(iso).toLocaleString(undefined, {dateStyle: 'medium', timeStyle: 'medium'})
}

export function duration(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  if (h) return `${h}h ${m}m`
  if (m) return `${m}m ${sec}s`
  return `${sec}s`
}

export function runDuration(r: {started_at: string; finished_at?: string}, now = Date.now()): string {
  const end = r.finished_at ? new Date(r.finished_at).getTime() : now
  return duration(end - new Date(r.started_at).getTime())
}

// dayLabel heads one day's entries in a list split by day: Today, Yesterday,
// then weekday and date (with the year once it is not this one).
export function dayLabel(iso: string): string {
  const day = localDate(iso)
  const now = new Date()
  const y = new Date(now)
  y.setDate(y.getDate() - 1)
  if (day === localDate(now)) return 'Today'
  if (day === localDate(y)) return 'Yesterday'
  const d = new Date(iso)
  const opts: Intl.DateTimeFormatOptions = {weekday: 'long', day: 'numeric', month: 'long'}
  if (d.getFullYear() !== now.getFullYear()) opts.year = 'numeric'
  return d.toLocaleDateString(undefined, opts)
}

// groupByDay splits a newest-first list into day sections, keeping order.
export function groupByDay<T>(items: T[], at: (t: T) => string): {day: string; label: string; items: T[]}[] {
  const out: {day: string; label: string; items: T[]}[] = []
  for (const it of items) {
    const day = localDate(at(it))
    const last = out[out.length - 1]
    if (last && last.day === day) last.items.push(it)
    else out.push({day, label: dayLabel(at(it)), items: [it]})
  }
  return out
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}

export function compact(n: number): string {
  return new Intl.NumberFormat(undefined, {notation: 'compact', maximumFractionDigits: 1}).format(n)
}

export function usd(n: number): string {
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: n < 1 ? 3 : 2,
  }).format(n)
}

// NO_CREDIT is the whole wording for a used-up API credit balance, wherever the
// app shows it. Retries go on quietly behind it: no reset time, no explanation,
// since the provider's own row already says which account it is.
export const NO_CREDIT = 'No credit left'

// resumeAt is when a rate-limited run continues: the gate's live reopen time
// wins over the one the run recorded, since another task's later limit can push
// the resume back after this run was paused.
export function resumeAt(runResumeAt: string | undefined, limitedUntil: string | undefined): Date | null {
  const a = runResumeAt ? new Date(runResumeAt) : null
  const b = limitedUntil ? new Date(limitedUntil) : null
  if (a && b) return a > b ? a : b
  return a ?? b
}

export function resumeText(at: Date | null): string {
  if (!at) return 'resumes once the rate limit resets'
  if (at <= new Date()) return 'resumes on the next check'
  return `resumes ${relTime(at)} · ${at.toLocaleString()}`
}
