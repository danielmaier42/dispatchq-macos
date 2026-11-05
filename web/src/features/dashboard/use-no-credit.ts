import {useLimits} from '@/lib/queries'
import {creditGone} from './limits'

// NO_CREDIT is all a run or banner says about a used-up API credit balance:
// the daemon retries on its own, so neither a reason nor a time is shown.
export const NO_CREDIT = 'No credit left'

// useNoCredit is the set of provider ids whose API credit balance is used up.
export function useNoCredit(): Set<string> {
  return new Set((useLimits().data ?? []).filter(creditGone).map((p) => p.id))
}
