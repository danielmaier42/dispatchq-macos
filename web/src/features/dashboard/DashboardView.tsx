import {ViewLayout} from '@/components/layout/view-layout'
import {useNow} from '@/hooks/use-now'
import {AllowanceStrip} from './AllowanceStrip'
import {OverviewCards} from './OverviewCards'
import {PoolsSection} from './PoolsSection'
import {useFreshLimits} from './use-fresh-limits'

// The state of things at a glance: how much of each provider's allowance is
// left and where pool tasks would go, then what runs, what is next and what failed. The daemon reads
// the allowance without spending any (every quarter of an hour, after every
// run, and whenever this page opens or asks) and the background poll only
// ever reads what it remembered. The sidebar's New task is the one primary
// action, so the header carries none.
export function DashboardView() {
  const {reading, read} = useFreshLimits()
  const now = useNow()
  return (
    <ViewLayout title="Dashboard" subtitle="What runs now, what is next, and how much allowance is left">
      <AllowanceStrip now={now} reading={reading} onRefresh={read} />
      <PoolsSection />
      <OverviewCards />
    </ViewLayout>
  )
}
