import {localDate} from '@/lib/format'
import type {Stats, StatsDay, StatsRange} from '@/lib/types'
import {dayLong} from './usage'

// The periods the Usage header offers. Segmented wants string values, so the
// option value is the query's own spelling.
export const RANGES: {value: string; range: StatsRange; label: string}[] = [
  {value: '7', range: 7, label: '7 days'},
  {value: '14', range: 14, label: '14 days'},
  {value: '30', range: 30, label: '30 days'},
  {value: '90', range: 90, label: '90 days'},
  {value: 'all', range: 'all', label: 'All'},
]

// periodLabel names the window the numbers on screen cover. It reads the
// response rather than the control, so while a new range loads the labels
// still match the numbers they sit next to.
export function periodLabel(stats: Pick<Stats, 'days' | 'all'>): string {
  if (stats.all) return 'All time'
  return stats.days === 1 ? 'Today' : `Last ${stats.days} days`
}

export type ChartUnit = 'day' | 'week'

// A chart column: one day, or one week of days. date is its first day, end
// its last; a week at either end of the window holds only the days inside it.
export interface ChartBucket extends StatsDay {
  end: string
}

// Beyond this many days a column per day gets too thin to read or hover, so
// the chart switches to weeks.
export const WEEKLY_AFTER = 45

export function chartBuckets(days: StatsDay[]): {unit: ChartUnit; buckets: ChartBucket[]} {
  if (days.length > WEEKLY_AFTER) return {unit: 'week', buckets: byWeek(days)}
  return {unit: 'day', buckets: days.map((d) => ({...d, end: d.date}))}
}

// byWeek sums consecutive days into Monday-to-Sunday weeks, oldest first.
export function byWeek(days: StatsDay[]): ChartBucket[] {
  const weeks: ChartBucket[] = []
  for (const d of days) {
    const last = weeks[weeks.length - 1]
    if (last && mondayOf(d.date) === mondayOf(last.date)) {
      last.end = d.date
      last.runs += d.runs
      last.tokens += d.tokens
      last.cost_usd += d.cost_usd
    } else {
      weeks.push({...d, end: d.date})
    }
  }
  return weeks
}

function parse(date: string): Date {
  const [y, m, d] = date.split('-').map(Number)
  return new Date(y, m - 1, d)
}

function mondayOf(date: string): string {
  const d = parse(date)
  d.setDate(d.getDate() - ((d.getDay() + 6) % 7))
  return localDate(d)
}

// shortDate is "Oct 6", with the year only when it is not this year's.
function shortDate(date: string, today: string): string {
  return parse(date).toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: date.slice(0, 4) === today.slice(0, 4) ? undefined : 'numeric',
  })
}

// weekSpan is the days a week column holds: "Oct 6 – 12", "Sep 29 – Oct 5",
// or "Oct 13 – today" for the week still running.
export function weekSpan(b: ChartBucket, today = localDate(new Date())): string {
  const from = shortDate(b.date, today)
  if (b.date === b.end) return b.end === today ? 'Today' : from
  const to =
    b.end === today
      ? 'today'
      : b.date.slice(0, 7) === b.end.slice(0, 7)
        ? String(parse(b.end).getDate())
        : shortDate(b.end, today)
  return `${from} – ${to}`
}

// axisLabels names the chart's columns, '' where a label is left out. Long
// ranges label every n-th column, counted from the newest so today always has
// one. The month shows where it changes from the previous label, so a skipped
// column cannot hide the turn of a month.
export function axisLabels(buckets: ChartBucket[], unit: ChartUnit, today = localDate(new Date())): string[] {
  const step = Math.max(1, Math.ceil(buckets.length / 16))
  const last = buckets.length - 1
  let prevMonth = ''
  return buckets.map((b, i) => {
    if ((last - i) % step !== 0) return ''
    const month = b.date.slice(0, 7)
    const newMonth = month !== prevMonth
    prevMonth = month
    if (unit === 'day' && b.date === today) return 'Today'
    if (unit === 'week' && b.end === today) return 'This week'
    if (newMonth) return shortDate(b.date, today)
    return String(parse(b.date).getDate())
  })
}

// narrowSkips marks the labels a narrow chart leaves out: every second shown
// one, counted from the newest so the latest label always stays.
export function narrowSkips(labels: string[]): boolean[] {
  let shown = 0
  const skips = labels.map(() => false)
  for (let i = labels.length - 1; i >= 0; i--) {
    if (labels[i]) skips[i] = shown++ % 2 === 1
  }
  return skips
}

// bucketTitle heads a column's tooltip.
export function bucketTitle(b: ChartBucket, unit: ChartUnit, today = localDate(new Date())): string {
  if (unit === 'day' || b.date === b.end) return dayLong(b.date)
  return `Week · ${weekSpan(b, today)}`
}
