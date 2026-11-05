import {Plus, X} from 'lucide-react'
import {Button} from '@/components/ui/button'
import {Input} from '@/components/ui/input'
import type {ProviderKind} from '@/lib/types'
import {KEY_SUGGESTION, isSecretName, newEnvRow, type EnvRow} from './env-logic'

// The environment variables a provider's CLI is started with. Values are
// write-only: a stored one shows as a placeholder and is kept unless a new one
// is typed.
export function EnvEditor({
  kind,
  rows,
  onChange,
  idPrefix,
}: {
  kind: ProviderKind | ''
  rows: EnvRow[]
  onChange: (rows: EnvRow[]) => void
  idPrefix: string
}) {
  const suggestion = kind ? KEY_SUGGESTION[kind] : undefined
  const suggest = suggestion && !rows.some((r) => r.name.trim() === suggestion)
  const patch = (key: number, p: Partial<EnvRow>) =>
    onChange(rows.map((r) => (r.key === key ? {...r, ...p} : r)))

  return (
    <div className="flex flex-col gap-2">
      {rows.map((r, i) => (
        <div key={r.key} className="flex items-center gap-2">
          <Input
            id={`${idPrefix}-name-${i}`}
            aria-label="Variable name"
            className="w-52 shrink-0 font-mono text-[12.5px]"
            value={r.name}
            placeholder="NAME"
            readOnly={r.stored}
            onChange={(e) => patch(r.key, {name: e.target.value})}
            spellCheck={false}
            autoCapitalize="off"
          />
          <Input
            id={`${idPrefix}-value-${i}`}
            aria-label={`Value of ${r.name || 'the variable'}`}
            className="font-mono text-[12.5px]"
            type={isSecretName(r.name) ? 'password' : 'text'}
            value={r.value}
            placeholder={r.stored ? 'Stored · type to replace' : 'value'}
            onChange={(e) => patch(r.key, {value: e.target.value})}
            spellCheck={false}
            autoComplete="off"
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${r.name || 'variable'}`}
            onClick={() => onChange(rows.filter((x) => x.key !== r.key))}
          >
            <X />
          </Button>
        </div>
      ))}
      <div className="flex flex-wrap gap-2">
        <Button type="button" variant="secondary" size="sm" onClick={() => onChange([...rows, newEnvRow()])}>
          <Plus /> Add variable
        </Button>
        {suggest && (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => onChange([...rows, newEnvRow(suggestion)])}
          >
            <Plus /> <span className="font-mono">{suggestion}</span>
          </Button>
        )}
      </div>
    </div>
  )
}
