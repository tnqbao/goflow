import { useState } from 'react'
import { FlaskConical, KeyRound, Plus } from 'lucide-react'
import { errorMessage, idPath, request, useResource } from './api'
import type { Provider, ProviderKind } from './types'
import { ActionForm, Alert, Empty, Loading, Modal } from './ui'

const capabilities: Record<ProviderKind, string[]> = {
  mock: ['text', 'image', 'video'],
  gemini: ['text'],
  fal: ['image', 'video'],
}
const names: Record<ProviderKind, string> = {
  mock: 'Mock',
  gemini: 'Gemini',
  fal: 'fal.ai',
}

export function Providers() {
  const data = useResource<Provider[]>('/providers', true)
  const [editing, setEditing] = useState<Provider | 'new'>()
  const [busy, setBusy] = useState('')
  const [notice, setNotice] = useState('')
  const [error, setError] = useState('')
  const test = async (item: Provider) => {
    setBusy(item.id)
    setError('')
    setNotice('')
    try {
      const result = await request<{ check: string }>(
        '/providers/' + idPath(item.id) + '/test',
        { method: 'POST' },
      )
      const checks: Record<string, string> = {
        local_configuration: 'local configuration',
        model_catalog: 'model catalog',
        model_pricing: 'model pricing',
      }
      setNotice(
        `${item.id}: ${checks[result.check] ?? 'connectivity'} check passed.`,
      )
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy('')
    }
  }
  return (
    <div className="stack">
      <div className="inline-actions">
        <button className="button primary" onClick={() => setEditing('new')}>
          <Plus size={18} /> Add provider
        </button>
      </div>
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}
      {error && <Alert>{error}</Alert>}
      <section className="panel">
        <div className="panel-head">
          <h2>Configured providers</h2>
        </div>
        {data.error && <Alert>{data.error}</Alert>}
        {data.value?.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Provider</th>
                  <th>Capabilities</th>
                  <th>Models</th>
                  <th>Status</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {data.value.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <strong>{item.id}</strong>
                      <span className="secondary-line">
                        {names[item.type] ?? item.type}
                      </span>
                    </td>
                    <td>{item.capabilities.join(', ')}</td>
                    <td>{item.models?.join(', ') || '—'}</td>
                    <td>
                      <span
                        className={`badge ${item.enabled ? 'badge-succeeded' : 'badge-cancelled'}`}
                      >
                        {item.enabled ? 'Enabled' : 'Disabled'}
                      </span>
                    </td>
                    <td>
                      <div className="inline-actions">
                        {item.id !== 'mock' && (
                          <button
                            className="button secondary"
                            onClick={() => setEditing(item)}
                          >
                            Configure
                          </button>
                        )}
                        <button
                          className="button secondary"
                          disabled={!!busy}
                          onClick={() => void test(item)}
                        >
                          <FlaskConical size={16} />
                          {busy === item.id ? 'Testing…' : 'Test'}
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : data.value ? (
          <Empty>
            No provider configurations. Workflows can still use the built-in
            Mock.
          </Empty>
        ) : (
          !data.error && <Loading />
        )}
      </section>
      {editing && (
        <ProviderEditor
          item={editing === 'new' ? undefined : editing}
          close={() => setEditing(undefined)}
          done={() => {
            setEditing(undefined)
            data.refresh()
          }}
        />
      )}
    </div>
  )
}
function ProviderEditor({
  item,
  close,
  done,
}: {
  item?: Provider
  close: () => void
  done: () => void
}) {
  const [kind, setKind] = useState<ProviderKind>(item?.type ?? 'mock')
  return (
    <Modal title={item ? `Configure ${item.id}` : 'Add provider'} close={close}>
      <ActionForm
        submit={async (fields) => {
          const selected = fields.getAll('capability')
          if (!selected.length)
            throw new Error('Select at least one capability.')
          const key = String(fields.get('key') ?? '')
          const settings = {
            capabilities: selected,
            models: String(fields.get('models') ?? '')
              .split(',')
              .map((s) => s.trim())
              .filter(Boolean),
            enabled: fields.has('enabled'),
            maxConcurrent: Number(fields.get('limit') ?? 0),
            timeoutSeconds: Number(fields.get('timeout') ?? 0),
            ...(key ? { apiKey: key } : {}),
          }
          if (item)
            await request('/providers/' + idPath(item.id), {
              method: 'PATCH',
              body: settings,
            })
          else
            await request('/providers', {
              method: 'POST',
              body: {
                id: String(fields.get('id')).trim(),
                type: kind,
                ...settings,
              },
            })
          done()
        }}
      >
        {!item && (
          <label>
            Provider ID
            <input name="id" required maxLength={63} />
          </label>
        )}
        <label>
          Provider type
          <select
            value={kind}
            disabled={!!item}
            onChange={(event) => setKind(event.target.value as ProviderKind)}
          >
            {Object.entries(names).map(([key, title]) => (
              <option key={key} value={key}>
                {title}
              </option>
            ))}
          </select>
        </label>
        <div className="check-group" key={kind}>
          <span>Capabilities</span>
          {capabilities[kind].map((capability) => (
            <label className="check" key={capability}>
              <input
                name="capability"
                value={capability}
                type="checkbox"
                defaultChecked={item?.capabilities.includes(capability) ?? true}
              />
              {capability}
            </label>
          ))}
        </div>
        {kind !== 'mock' && (
          <>
            <label>
              Model IDs (comma separated)
              <input name="models" defaultValue={item?.models?.join(', ')} />
            </label>
            <label>
              {item ? 'New API key (optional)' : 'API key'}
              <span className="input-with-icon">
                <KeyRound size={16} />
                <input
                  name="key"
                  type="password"
                  autoComplete="off"
                  required={!item}
                  maxLength={4096}
                />
              </span>
            </label>
          </>
        )}
        <div className="form-grid">
          <label>
            Concurrent requests
            <input
              name="limit"
              type="number"
              min={0}
              max={100}
              defaultValue={item?.maxConcurrent}
            />
            <small>Leave blank for the default.</small>
          </label>
          <label>
            Timeout (seconds)
            <input
              name="timeout"
              type="number"
              min={0}
              max={86400}
              defaultValue={item?.timeoutSeconds}
            />
            <small>Leave blank for the default.</small>
          </label>
        </div>
        <label className="check">
          <input
            name="enabled"
            type="checkbox"
            defaultChecked={item?.enabled ?? true}
          />{' '}
          Enabled
        </label>
      </ActionForm>
    </Modal>
  )
}
