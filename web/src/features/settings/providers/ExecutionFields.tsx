import {useQuery} from '@tanstack/react-query'
import {Search} from 'lucide-react'
import {useEffect, useState} from 'react'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {Field} from '@/components/ui/field'
import {Input} from '@/components/ui/input'
import {Select} from '@/components/ui/select'
import {errorMessage, get, post} from '@/lib/api'
import {useWSLStatus} from '@/lib/queries'
import type {ProviderKind, WSLDetection, WSLDistro} from '@/lib/types'
import {distroOptions, type RunsIn} from './execution-logic'

export interface ExecutionChoice {
  runsIn: RunsIn
  distro: string
  user: string
}

// ExecutionFields is the "Runs in" choice of a new provider. It exists only in
// DispatchQ for Windows: everywhere else the daemon reports no WSL support and
// nothing is shown. Asking which distributions exist starts wsl.exe, so that
// happens only after the operator picks Linux; a PC without WSL never runs it.
export function ExecutionFields({
  kind,
  value,
  onChange,
  onDetected,
}: {
  kind: ProviderKind | ''
  value: ExecutionChoice
  onChange: (v: ExecutionChoice) => void
  onDetected: (d: WSLDetection) => void
}) {
  const status = useWSLStatus().data
  const linux = value.runsIn === 'wsl'
  const distros = useQuery({
    queryKey: ['wsl', 'distros'],
    queryFn: ({signal}) => get<WSLDistro[]>('/api/wsl/distros', signal),
    enabled: linux && !!status?.available,
    staleTime: 30_000,
    retry: false,
  })
  const [finding, setFinding] = useState(false)
  const [found, setFound] = useState<WSLDetection | null>(null)
  const [findError, setFindError] = useState('')

  const distro = value.distro || distros.data?.find((d) => d.default)?.name || distros.data?.[0]?.name || ''
  // The preselected distribution is the one saved unless another is picked.
  useEffect(() => {
    if (linux && !value.distro && distro) onChange({...value, distro})
  }, [linux, distro, value, onChange])

  if (!status?.supported) return null

  const set = (patch: Partial<ExecutionChoice>) => {
    setFound(null)
    setFindError('')
    onChange({...value, distro, ...patch})
  }
  const find = async () => {
    setFinding(true)
    setFindError('')
    try {
      const d = await post<WSLDetection>('/api/wsl/detect', {distro, user: value.user.trim(), kind})
      setFound(d)
      onDetected(d)
    } catch (err) {
      setFindError(errorMessage(err))
    } finally {
      setFinding(false)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <Field
        label="Runs in"
        htmlFor="np-runs-in"
        hint={
          linux
            ? "Linux (WSL) runs this provider inside a Linux system on this PC. Use it if the CLI is installed and logged in there. Its login, settings and folders are the Linux ones, separate from Windows'. Fixed once added."
            : 'Where the CLI runs. Fixed once added.'
        }
      >
        <Select
          id="np-runs-in"
          value={value.runsIn}
          onValueChange={(v) => set({runsIn: v as RunsIn})}
          options={[
            {value: 'native', label: 'Windows (default)'},
            {value: 'wsl', label: 'Linux (WSL)'},
          ]}
        />
      </Field>
      {linux && !status.available && (
        <Alert tone="warning" title="WSL isn't installed">
          DispatchQ works fully without it. Install WSL and a Linux distribution first to run a provider
          there.
        </Alert>
      )}
      {linux && status.available && (
        <>
          <div className="grid grid-cols-2 gap-3">
            <Field
              label="Linux system"
              htmlFor="np-distro"
              error={distros.error ? errorMessage(distros.error) : ''}
            >
              <Select
                id="np-distro"
                value={distro}
                onValueChange={(v) => set({distro: v})}
                placeholder={distros.isLoading ? 'Asking WSL…' : 'No distribution'}
                options={distroOptions(distros.data ?? [])}
              />
            </Field>
            <Field
              label="Linux user"
              htmlFor="np-user"
              hint="Optional. Empty uses the distribution's default user."
            >
              <Input
                id="np-user"
                className="font-mono text-[12.5px]"
                value={value.user}
                placeholder="default user"
                onChange={(e) => set({user: e.target.value})}
                spellCheck={false}
              />
            </Field>
          </div>
          <div className="flex items-center gap-3">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={find}
              loading={finding}
              disabled={!distro || !kind}
            >
              {!finding && <Search />}
              Find the CLI in Linux
            </Button>
            {found && (
              <span className="min-w-0 truncate font-mono text-[12px] text-muted-foreground">
                {found.binary || 'not installed'} · {found.user} · {found.arch}
              </span>
            )}
          </div>
          {findError && (
            <Alert tone="danger" title="WSL did not answer">
              {findError}
            </Alert>
          )}
          {found?.warnings?.map((w) => (
            <Alert key={w} tone="warning">
              {w}
            </Alert>
          ))}
        </>
      )}
    </div>
  )
}
