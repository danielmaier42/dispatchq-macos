import {
  Bell,
  Boxes,
  Info,
  Layers,
  Palette,
  Play,
  Settings2,
  Sparkles,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

export const SECTION_IDS = [
  'general',
  'appearance',
  'execution',
  'notifications',
  'providers',
  'pools',
  'assistant',
  'system',
  'about',
] as const
export type SectionId = (typeof SECTION_IDS)[number]

export interface SectionDef {
  id: SectionId
  label: string
  icon: LucideIcon
  description: string
}

export const SECTIONS: SectionDef[] = [
  {
    id: 'general',
    label: 'General',
    icon: Settings2,
    description: 'Defaults every new task and every run starts from',
  },
  {
    id: 'appearance',
    label: 'Appearance',
    icon: Palette,
    description: 'How DispatchQ looks in its window and in a browser tab',
  },
  {
    id: 'execution',
    label: 'Execution',
    icon: Play,
    description: 'When runs start, how long they may hang and what is kept',
  },
  {
    id: 'notifications',
    label: 'Notifications',
    icon: Bell,
    description: 'Where run outcomes and new artifacts are announced',
  },
  {id: 'providers', label: 'Providers', icon: Boxes, description: 'The coding agents your tasks run on'},
  {id: 'pools', label: 'Pools', icon: Layers, description: 'Groups of providers that share a queue'},
  {
    id: 'assistant',
    label: 'Assistant',
    icon: Sparkles,
    description: 'The jobs Claude does for DispatchQ itself',
  },
  {id: 'system', label: 'System', icon: Wrench, description: 'Removing DispatchQ from this computer'},
  {id: 'about', label: 'About', icon: Info, description: 'Version and software updates'},
]

export const isSectionId = (v: string): v is SectionId => (SECTION_IDS as readonly string[]).includes(v)

// pickSection decides what Settings shows: a section someone asked for (a
// link, the native menu), else the one last open this session, else About
// while an update waits, since that is where it is offered, else General.
export function pickSection({
  requested,
  last,
  updatePending,
}: {
  requested: string
  last: string
  updatePending: boolean
}): SectionId {
  if (isSectionId(requested)) return requested
  if (isSectionId(last)) return last
  return updatePending ? 'about' : 'general'
}
