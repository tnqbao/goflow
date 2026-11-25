import { useState } from 'react'
import { Copy, Plus } from 'lucide-react'
import { errorMessage, idPath, request, useResource } from './api'
import type { Token, User } from './types'
import { ActionForm, Alert, date, Empty, Loading, Modal } from './ui'

export function Settings({ user }: { user: User }) {
  const tokens = useResource<Token[] | null>('/auth/tokens')
  const [creating, setCreating] = useState(false)
  const [issued, setIssued] = useState('')
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState('')
  const revoke = async (item: Token) => {
    if (!confirm(`Revoke the API token “${item.name}”?`)) return
    setError('')
    try {
      await request('/auth/tokens/' + idPath(item.id), { method: 'DELETE' })
      tokens.refresh()
    } catch (err) {
      setError(errorMessage(err))
    }
  }
  return (
    <div className="stack">
      <section className="panel">
        <div className="panel-head">
          <h2>Account</h2>
        </div>
        <div className="panel-body">
          <strong>{user.username}</strong>
          <span className="secondary-line">Administrator</span>
        </div>
      </section>
      <section className="panel">
        <div className="panel-head">
          <h2>API tokens</h2>
          <button className="button primary" onClick={() => setCreating(true)}>
            <Plus size={17} /> Create token
          </button>
        </div>
        {(error || tokens.error) && <Alert>{error || tokens.error}</Alert>}
        {tokens.value?.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Created</th>
                  <th>Expires</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {tokens.value.map((item) => (
                  <tr key={item.id}>
                    <td>{item.name}</td>
                    <td>{date(item.createdAt)}</td>
                    <td>{date(item.expiresAt)}</td>
                    <td>
                      <button
                        className="text-button"
                        onClick={() => void revoke(item)}
                      >
                        Revoke
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : tokens.value !== undefined ? (
          <Empty>No active API tokens.</Empty>
        ) : (
          !tokens.error && <Loading />
        )}
      </section>
      {creating && (
        <Modal title="Create API token" close={() => setCreating(false)}>
          <ActionForm
            label="Create token"
            submit={async (fields) => {
              const result = await request<{ token: string }>('/auth/tokens', {
                method: 'POST',
                body: { name: String(fields.get('name')) },
              })
              setCreating(false)
              setIssued(result.token)
              setCopied(false)
              tokens.refresh()
            }}
          >
            <label>
              Token name
              <input name="name" required maxLength={64} />
            </label>
          </ActionForm>
        </Modal>
      )}
      {issued && (
        <Modal
          title="New API token"
          close={() => {
            setIssued('')
            setError('')
          }}
        >
          <div className="panel-body stack">
            <p>Copy this token now. It is shown only once.</p>
            <textarea
              aria-label="New API token"
              className="code-input"
              readOnly
              value={issued}
              rows={3}
              onFocus={(event) => event.currentTarget.select()}
            />
            {error && <Alert>{error}</Alert>}
            <button
              className="button primary"
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(issued)
                  setCopied(true)
                } catch {
                  setError(
                    'Clipboard unavailable. Select and copy the token manually.',
                  )
                }
              }}
            >
              <Copy size={16} />
              {copied ? 'Copied' : 'Copy token'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
