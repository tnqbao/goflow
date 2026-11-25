import { useState } from 'react'
import { Download, FilePlus2, Play, Plus, Trash2 } from 'lucide-react'
import { idPath, request, useResource } from './api'
import { executionBody } from './data'
import type { Input, Run, Workflow } from './types'
import { ActionForm, Alert, Empty, Loading, Modal } from './ui'

const initialSpec = {
  inputs: [{ name: 'prompt', type: 'string', required: true }],
  steps: [{ name: 'scene', type: 'video', providerRef: 'mock' }],
  outputs: [{ name: 'video', from: 'scene' }],
}
type Dialog =
  | { kind: 'edit' | 'run' | 'delete'; item: Workflow }
  | { kind: 'new' | 'import' }

export function Workflows() {
  const data = useResource<Workflow[]>('/workflows', true)
  const [dialog, setDialog] = useState<Dialog>()
  const close = () => setDialog(undefined)
  const done = () => {
    data.refresh()
    close()
  }
  return (
    <div className="stack">
      <div className="inline-actions">
        <button
          className="button primary"
          onClick={() => setDialog({ kind: 'new' })}
        >
          <Plus size={18} /> New workflow
        </button>
        <button
          className="button secondary"
          onClick={() => setDialog({ kind: 'import' })}
        >
          <FilePlus2 size={17} /> Import JSON
        </button>
        <a className="text-button" href="/workflows">
          Open Mock demo
        </a>
      </div>
      <section className="panel">
        <div className="panel-head">
          <h2>Workflow definitions</h2>
        </div>
        {data.error && <Alert>{data.error}</Alert>}
        {data.value?.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Workflow</th>
                  <th>Steps</th>
                  <th>Inputs</th>
                  <th>Actions</th>
                </tr>
              </thead>
              <tbody>
                {data.value.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <button
                        className="row-link"
                        onClick={() => setDialog({ kind: 'edit', item })}
                      >
                        {item.id}
                      </button>
                      {item.spec.description && (
                        <span className="secondary-line">
                          {item.spec.description}
                        </span>
                      )}
                    </td>
                    <td>{item.spec.steps.length}</td>
                    <td>{item.spec.inputs?.length ?? 0}</td>
                    <td>
                      <div className="inline-actions">
                        <button
                          className="button secondary"
                          onClick={() => setDialog({ kind: 'run', item })}
                        >
                          <Play size={15} /> Run
                        </button>
                        <button
                          className="icon-button"
                          aria-label={`Export ${item.id}`}
                          onClick={() => exportWorkflow(item)}
                        >
                          <Download size={17} />
                        </button>
                        <button
                          className="icon-button"
                          aria-label={`Delete ${item.id}`}
                          onClick={() => setDialog({ kind: 'delete', item })}
                        >
                          <Trash2 size={17} />
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
            No workflow definitions. Create one or import a JSON definition.
          </Empty>
        ) : (
          !data.error && <Loading />
        )}
      </section>
      {dialog?.kind === 'new' && <Editor close={close} done={done} />}
      {dialog?.kind === 'edit' && (
        <Editor item={dialog.item} close={close} done={done} />
      )}
      {dialog?.kind === 'import' && (
        <ImportWorkflow close={close} done={done} />
      )}
      {dialog?.kind === 'run' && (
        <LaunchWorkflow item={dialog.item} close={close} />
      )}
      {dialog?.kind === 'delete' && (
        <Modal title="Delete workflow" close={close}>
          <ActionForm
            label="Delete workflow"
            submit={async () => {
              await request('/workflows/' + idPath(dialog.item.id), {
                method: 'DELETE',
              })
              done()
            }}
          >
            <p>
              Delete <strong>{dialog.item.id}</strong>? Existing executions and
              their outputs remain available.
            </p>
          </ActionForm>
        </Modal>
      )}
    </div>
  )
}
function Editor({
  item,
  close,
  done,
}: {
  item?: Workflow
  close: () => void
  done: () => void
}) {
  return (
    <Modal title={item ? 'Edit workflow' : 'New workflow'} close={close}>
      <ActionForm
        submit={async (fields) => {
          const value: unknown = JSON.parse(String(fields.get('spec')))
          if (!value || Array.isArray(value) || typeof value !== 'object')
            throw new Error('Enter a workflow spec object.')
          const spec = value as Record<string, unknown>
          if (item) {
            for (const key of Object.keys(item.spec)) {
              if (!Object.hasOwn(spec, key)) spec[key] = null
            }
            await request('/workflows/' + idPath(item.id), {
              method: 'PATCH',
              body: { resourceVersion: item.resourceVersion, spec },
            })
          } else {
            await request('/workflows', {
              method: 'POST',
              body: { id: String(fields.get('id')).trim(), spec },
            })
          }
          done()
        }}
      >
        <label>
          Workflow ID
          <input
            name="id"
            defaultValue={item?.id}
            disabled={!!item}
            required
            maxLength={253}
          />
        </label>
        <label>
          Workflow spec (JSON)
          <textarea
            name="spec"
            className="code-input"
            rows={14}
            spellCheck={false}
            required
            defaultValue={JSON.stringify(item?.spec ?? initialSpec, null, 2)}
          />
        </label>
      </ActionForm>
    </Modal>
  )
}
function ImportWorkflow({
  close,
  done,
}: {
  close: () => void
  done: () => void
}) {
  return (
    <Modal title="Import workflow" close={close}>
      <ActionForm
        label="Import"
        submit={async (fields) => {
          const file = fields.get('file')
          if (!(file instanceof File) || !file.size || file.size > 512 * 1024)
            throw new Error('Choose a workflow JSON file up to 512 KB.')
          const value: unknown = JSON.parse(await file.text())
          if (
            !value ||
            typeof value !== 'object' ||
            !('id' in value) ||
            !('spec' in value) ||
            typeof value.id !== 'string'
          ) {
            throw new Error('The file must contain a workflow ID and spec.')
          }
          await request('/workflows', {
            method: 'POST',
            body: { id: value.id, spec: value.spec },
          })
          done()
        }}
      >
        <label>
          Workflow JSON file
          <input
            type="file"
            name="file"
            accept=".json,application/json"
            required
          />
        </label>
      </ActionForm>
    </Modal>
  )
}
function exportWorkflow(item: Workflow) {
  const url = URL.createObjectURL(
    new Blob(
      [JSON.stringify({ id: item.id, spec: item.spec }, null, 2) + '\n'],
      { type: 'application/json' },
    ),
  )
  const link = document.createElement('a')
  link.href = url
  link.download = item.id + '.json'
  link.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
function LaunchWorkflow({
  item,
  close,
}: {
  item: Workflow
  close: () => void
}) {
  const external = item.spec.steps.some(
    (step) => step.providerRef && step.providerRef !== 'mock',
  )
  const needsPrompt = item.spec.steps.some((step) =>
    ['llm', 'image', 'video'].includes(step.type),
  )
  const inputs: Input[] = item.spec.inputs?.length
    ? item.spec.inputs
    : needsPrompt
      ? [{ name: 'prompt', type: 'string', required: true }]
      : []
  return (
    <Modal title={`Run ${item.id}`} close={close}>
      <ActionForm
        label="Start execution"
        submit={async (fields) => {
          const run = await request<Run>(
            '/workflows/' + idPath(item.id) + '/runs',
            {
              method: 'POST',
              body: executionBody(
                inputs,
                fields,
                external && fields.has('allow-paid'),
              ),
            },
          )
          close()
          location.hash = '#/executions/' + idPath(run.id)
        }}
      >
        {inputs.map((input) => (
          <label key={input.name}>
            {input.name}
            {input.description && (
              <span className="secondary-line">{input.description}</span>
            )}
            {input.type === 'boolean' ? (
              <select
                name={'input:' + input.name}
                required={input.required}
                defaultValue=""
              >
                <option value="">Select a value</option>
                <option value="true">True</option>
                <option value="false">False</option>
              </select>
            ) : (
              <input
                name={'input:' + input.name}
                type={input.type === 'number' ? 'number' : 'text'}
                inputMode={input.type === 'integer' ? 'numeric' : undefined}
                step={input.type === 'number' ? 'any' : undefined}
                required={input.required}
              />
            )}
          </label>
        ))}
        {external && (
          <label className="check">
            <input name="allow-paid" type="checkbox" required /> Approve
            external provider charges for this execution
          </label>
        )}
        {!inputs.length && !external && (
          <p>This workflow has no declared inputs.</p>
        )}
      </ActionForm>
    </Modal>
  )
}
