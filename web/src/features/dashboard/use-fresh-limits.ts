import {useMutation} from '@tanstack/react-query'
import {useEffect} from 'react'
import {get} from '@/lib/api'
import {keys, queryClient} from '@/lib/queries'
import type {LimitsResponse} from '@/lib/types'

// useFreshLimits asks the providers for their allowance again when the
// Dashboard opens and on its Read now button; the background poll only ever
// reads what the daemon remembered. Read now also pings every account on an
// API key (?ping=1), which is how a topped-up account is unlocked before its
// daily ping; opening the page does not, since a ping is a real request. The
// daemon reuses a reading younger than half a minute, so opening the page
// repeatedly costs nothing. The pools rank on the readings just taken, so they
// are read after it. A failed read needs no message: the regular poll shows
// the last reading and why it failed.
export function useFreshLimits() {
  const {mutate, isPending} = useMutation({
    mutationFn: (ping: boolean) => get<LimitsResponse>(`/api/limits?fresh=1${ping ? '&ping=1' : ''}`),
    onSuccess: (data) => {
      queryClient.setQueryData(keys.limits, data)
      return queryClient.invalidateQueries({queryKey: keys.pools})
    },
  })

  useEffect(() => mutate(false), [mutate])

  return {reading: isPending, read: () => mutate(true)}
}
