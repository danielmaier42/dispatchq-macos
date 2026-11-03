import {CloudOff} from 'lucide-react'
import {useEffect, useMemo} from 'react'
import {useNav} from '@/app/nav'
import {ViewLayout} from '@/components/layout/view-layout'
import {Button} from '@/components/ui/button'
import {EmptyState} from '@/components/ui/empty-state'
import {errorMessage} from '@/lib/api'
import {invalidate, keys, useUpdate} from '@/lib/queries'
import {SaveBar} from './components/SaveBar'
import {SettingsNav, type NavMark} from './components/SettingsNav'
import {SectionTitle, SettingsSkeleton} from './components/parts'
import {editedSections} from './draft'
import {PoolsPane} from './pools/PoolsPane'
import {ProvidersPane} from './providers/ProvidersPane'
import {AboutSection} from './sections/AboutSection'
import {AppearanceSection} from './sections/AppearanceSection'
import {AssistantSection} from './sections/AssistantSection'
import {ExecutionSection} from './sections/ExecutionSection'
import {GeneralSection} from './sections/GeneralSection'
import {NotificationsSection} from './sections/NotificationsSection'
import {SystemSection} from './sections/SystemSection'
import {pickSection, SECTIONS, type SectionId} from './sections'
import {setSection, useSettingsUi} from './store'
import {updatePending} from './update'
import {useSettingsForm} from './use-settings-form'

export function SettingsView() {
  const form = useSettingsForm()
  const update = useUpdate().data
  const requested = useNav((s) => s.settingsSection)
  const last = useSettingsUi((s) => s.section)

  const current = pickSection({requested, last, updatePending: updatePending(update)})

  // A requested section (a link, the native menu) is taken over as the
  // session's own choice, and the request is cleared so it is not replayed.
  useEffect(() => {
    if (!requested) return
    setSection(current)
    useNav.setState({settingsSection: ''})
  }, [requested, current])

  const edited = useMemo(() => editedSections(form.pending), [form.pending])
  const marks: Partial<Record<SectionId, NavMark>> = {}
  for (const id of edited) marks[id as SectionId] = 'unsaved'
  if (updatePending(update)) marks.about = 'update'

  const def = SECTIONS.find((s) => s.id === current) ?? SECTIONS[0]

  return (
    <ViewLayout title="Settings">
      {/* The section list sits beside the section only where the content area,
          not the window, has room for both. */}
      <div className="@container/settings">
        <div className="flex flex-col gap-6 @3xl/settings:flex-row @3xl/settings:gap-8">
          <nav className="shrink-0 @3xl/settings:w-48">
            <SettingsNav sections={SECTIONS} current={current} marks={marks} onSelect={setSection} />
          </nav>
          <div className="min-w-0 flex-1">
            <SectionTitle title={def.label} description={def.description} />
            <SectionBody id={current} form={form} />
            {form.dirty && <SaveBar sections={edited} />}
          </div>
        </div>
      </div>
    </ViewLayout>
  )
}

function SectionBody({id, form}: {id: SectionId; form: ReturnType<typeof useSettingsForm>}) {
  // These three draw from their own data, not from the settings form.
  if (id === 'providers') return <ProvidersPane />
  if (id === 'pools') return <PoolsPane />
  if (id === 'about') return <AboutSection />
  if (id === 'appearance') return <AppearanceSection />

  const {value, stored} = form
  if (!value || !stored) {
    if (form.error && !form.isLoading) {
      return (
        <EmptyState
          icon={<CloudOff />}
          title="Settings could not be loaded"
          description={errorMessage(form.error)}
          action={<Button onClick={() => invalidate(keys.settings)}>Try again</Button>}
        />
      )
    }
    return <SettingsSkeleton />
  }

  switch (id) {
    case 'general':
      return <GeneralSection value={value} stored={stored} />
    case 'execution':
      return <ExecutionSection value={value} />
    case 'notifications':
      return <NotificationsSection value={value} stored={stored} />
    case 'assistant':
      return <AssistantSection value={value} />
    case 'system':
      return <SystemSection />
  }
}
