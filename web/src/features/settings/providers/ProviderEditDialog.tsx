import {Pencil} from 'lucide-react'
import {useState, type FormEvent} from 'react'
import {ModelSelect} from '@/components/domain/model-select'
import {Alert} from '@/components/ui/alert'
import {Button} from '@/components/ui/button'
import {
  Dialog,
  DialogBody,
  DialogClose,
  DialogContent,
  DialogFooter,
  DialogHeader,
} from '@/components/ui/dialog'
import {Field} from '@/components/ui/field'
import {Input} from '@/components/ui/input'
import {Select} from '@/components/ui/select'
import {toast} from '@/components/ui/toaster'
import {errorMessage} from '@/lib/api'
import {useProviderList} from '@/lib/providers'
import type {Provider} from '@/lib/types'
import {saveProvider} from './actions'
import {EnvEditor} from './EnvEditor'
import {envPayload, envProblem, envRowsFor} from './env-logic'
import {fallbackOptions} from './provider-logic'

export function ProviderEditDialog({
  provider,
  open,
  onOpenChange,
}: {
  provider: Provider
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="md">
        {/* Mounted only while open, so every opening starts from what is stored now. */}
        {open && <EditForm provider={provider} onDone={() => onOpenChange(false)} />}
      </DialogContent>
    </Dialog>
  )
}

function EditForm({provider: p, onDone}: {provider: Provider; onDone: () => void}) {
  const providers = useProviderList()
  const [name, setName] = useState(p.name || '')
  const [binary, setBinary] = useState(p.binary_path || '')
  const [dir, setDir] = useState(p.config_dir || '')
  const [model, setModel] = useState(p.default_model || '')
  const [fallback, setFallback] = useState(p.fallback_provider || '')
  const [fallbackModel, setFallbackModel] = useState(p.fallback_model || '')
  const [env, setEnv] = useState(() => envRowsFor(p.env ?? []))
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const problem = envProblem(env)
    if (problem) {
      setError(problem)
      return
    }
    setSaving(true)
    setError('')
    try {
      // enabled is left out: the switch on the card owns it, and PUT keeps it.
      await saveProvider(p.id, {
        name: name.trim(),
        binary_path: binary.trim(),
        config_dir: dir.trim(),
        default_model: model,
        fallback_provider: fallback,
        fallback_model: fallback ? fallbackModel : '',
        env: envPayload(env),
      })
      toast.success('Provider saved')
      onDone()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form onSubmit={submit} className="flex min-h-0 flex-col">
      <DialogHeader
        icon={<Pencil />}
        title={`Edit ${p.name || p.id}`}
        description={
          <>
            <span className="font-mono">{p.id}</span> · {p.type_name || p.kind}. The id and the type cannot
            change.
          </>
        }
      />
      <DialogBody className="flex flex-col gap-4">
        <Field
          label="Name"
          htmlFor="pe-name"
          hint="What this provider is called in the app and in run messages"
        >
          <Input
            id="pe-name"
            value={name}
            placeholder={p.id}
            onChange={(e) => setName(e.target.value)}
            autoFocus
          />
        </Field>
        <Field
          label="Binary"
          htmlFor="pe-binary"
          hint={
            <>
              Absolute path to the CLI. The background daemon can't see your shell's PATH, so a full path is
              safest. Empty auto-detects
              {p.detected ? (
                <>
                  : <span className="font-mono">{p.detected}</span>
                </>
              ) : (
                '.'
              )}
            </>
          }
          aside={
            p.detected && binary !== p.detected ? (
              <Button type="button" variant="link" size="sm" onClick={() => setBinary(p.detected ?? '')}>
                Use detected
              </Button>
            ) : undefined
          }
        >
          <Input
            id="pe-binary"
            className="font-mono text-[12.5px]"
            value={binary}
            placeholder={p.detected || '/path/to/the CLI'}
            onChange={(e) => setBinary(e.target.value)}
            spellCheck={false}
          />
        </Field>
        <Field
          label="Configuration directory"
          htmlFor="pe-dir"
          hint="Where the CLI keeps its account and sessions; another directory is another account. Absolute path (~ is fine); empty uses its own default."
        >
          <Input
            id="pe-dir"
            className="font-mono text-[12.5px]"
            value={dir}
            placeholder={p.default_config_dir || "the CLI's own"}
            onChange={(e) => setDir(e.target.value)}
            spellCheck={false}
          />
        </Field>
        <Field
          label="Environment variables"
          hint={
            p.kind === 'claude-code' ? (
              <>
                Passed to the CLI on every run and check. Set{' '}
                <span className="font-mono">ANTHROPIC_API_KEY</span> to run this provider on API billing
                instead of its login; it then shows no plan limits and its runs count in Usage with their real
                cost. Values are stored on this Mac and are never shown again.
              </>
            ) : (
              'Passed to the CLI on every run and check. Values are stored on this Mac and are never shown again.'
            )
          }
        >
          <EnvEditor kind={p.kind} rows={env} onChange={setEnv} idPrefix="pe-env" />
        </Field>
        <Field
          label="Default model"
          htmlFor="pe-model"
          hint="Used for tasks on this provider that name no model"
        >
          <ModelSelect
            id="pe-model"
            provider={p.id}
            value={model}
            onValueChange={setModel}
            emptyLabel="Provider default"
          />
        </Field>
        <div className="flex flex-col gap-4 rounded-lg border border-border bg-surface-2/50 p-3.5">
          <Field
            label="When the limit is reached"
            htmlFor="pe-fallback"
            hint="Where this provider's tasks run while its allowance is used up. Without one they wait for the window to reopen. An interrupted session doesn't travel: the substitute starts fresh."
          >
            <Select
              id="pe-fallback"
              value={fallback}
              // A model name means nothing to a different harness, so choosing
              // another provider clears it.
              onValueChange={(v) => {
                setFallback(v)
                setFallbackModel('')
              }}
              options={fallbackOptions(p.id, providers, p.fallback_provider)}
            />
          </Field>
          {fallback && (
            <Field
              label="Model there"
              htmlFor="pe-fallback-model"
              hint="What those substituted runs are given. Decided by DispatchQ keeps the task's model between two accounts of the same CLI and takes the other provider's default model otherwise."
            >
              <ModelSelect
                id="pe-fallback-model"
                provider={fallback}
                value={fallbackModel}
                onValueChange={setFallbackModel}
                emptyLabel="Decided by DispatchQ"
              />
            </Field>
          )}
        </div>
        {error && (
          <Alert tone="danger" title="Not saved">
            {error}
          </Alert>
        )}
      </DialogBody>
      <DialogFooter>
        <DialogClose asChild>
          <Button type="button" variant="ghost">
            Cancel
          </Button>
        </DialogClose>
        <Button type="submit" variant="primary" loading={saving}>
          Save
        </Button>
      </DialogFooter>
    </form>
  )
}
