import {FolderOpen, MapPin} from 'lucide-react'
import {useState} from 'react'
import {Button} from '@/components/ui/button'
import {Field} from '@/components/ui/field'
import {Input} from '@/components/ui/input'
import {toast} from '@/components/ui/toaster'
import {Tooltip} from '@/components/ui/tooltip'
import {api, errorMessage} from '@/lib/api'
import type {PathResult} from '@/lib/types'
import {EditorSection, Hint} from './parts'

// The native macOS folder dialog, opened by the daemon. It starts in the folder
// already in the field; 204 means it was cancelled.
async function chooseFolder(current: string): Promise<string | null> {
  const q = current ? '?path=' + encodeURIComponent(current) : ''
  const r = await api<PathResult | undefined>('POST', '/api/fs/choose' + q)
  return r?.path || null
}

export function WhereSection({
  value,
  error,
  hint,
  onChange,
}: {
  value: string
  error?: string
  hint?: string
  onChange: (dir: string) => void
}) {
  const [choosing, setChoosing] = useState(false)
  const choose = async () => {
    setChoosing(true)
    try {
      const path = await chooseFolder(value.trim())
      if (path) onChange(path)
    } catch (e) {
      toast.error('Folder dialog unavailable', {description: errorMessage(e)})
    } finally {
      setChoosing(false)
    }
  }
  return (
    <EditorSection icon={MapPin} title="Where">
      <Field
        label="Working directory"
        htmlFor="te-workingDir"
        error={error}
        hint={hint ? undefined : 'The task runs here; relative paths in the prompt resolve against it.'}
      >
        <div className="flex gap-2">
          <Input
            id="te-workingDir"
            value={value}
            aria-invalid={!!error}
            placeholder="Choose a folder…"
            spellCheck={false}
            autoComplete="off"
            className="font-mono text-[12.5px]"
            onChange={(e) => onChange(e.target.value)}
          />
          <Tooltip content="Choose a folder">
            <Button type="button" onClick={choose} loading={choosing} aria-label="Choose folder">
              {!choosing && <FolderOpen />}
              Choose…
            </Button>
          </Tooltip>
        </div>
        {hint && !error && <Hint tone="warning">{hint}</Hint>}
      </Field>
    </EditorSection>
  )
}
