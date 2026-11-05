import {useLimits} from '@/lib/queries'
import {creditGone} from './limits'

export {NO_CREDIT} from '@/lib/format'

// useNoCredit is the set of provider ids whose API credit balance is used up.
export function useNoCredit(): Set<string> {
  return new Set((useLimits().data ?? []).filter(creditGone).map((p) => p.id))
}
