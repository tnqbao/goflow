import { useState } from 'react'
import { Download, ListTree, RefreshCw } from 'lucide-react'
import { apiRoot, errorMessage, idPath, request, useResource } from './api'
import type { Artifact, Run } from './types'
import {
  ActionForm,
  Alert,
  Badge,
  bytes,
  date,
  Empty,
  Loading,
  Modal,
} from './ui'

export function orderedRuns(runs: Run[]) {
  return [...runs].sort((a, b) => b.createdAt.localeCompare(a.createdAt))
}
export function RunTable({
  runs,
  activeLink = false,
}: {
  runs: Run[]
  activeLink?: boolean
}) {
  if (!runs.length) return <Empty>No executions yet.</Empty>
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>
            <th>Workflow</th>
            <th>Status</th>
            <th>Started</th>
            <th>Completed</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <tr key={run.id}>
              <td>
                <a className="row-link" href={`#/executions/${idPath(run.id)}`}>
                  {run.workflowId}
                </a>
                <span className="secondary-line">{run.id}</span>
              </td>
              <td>
                <Badge phase={run.phase} />
              </td>
              <td>{date(run.startedAt)}</td>
              <td>{date(run.completedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {activeLink && (
        <div className="panel-footer">
          <a href="#/executions">View all executions</a>
        </div>
      )}
    </div>
  )
}

export function Executions({ id }: { id?: string }) {
  return id ? <RunDetails key={id} id={id} /> : <RunsList />
}
function RunsList() {
  const runs = useResource<Run[]>('/runs', true, 8000)
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>Execution history</h2>
        <button
          className="icon-button"
          aria-label="Refresh executions"
          onClick={runs.refresh}
        >
          <RefreshCw size={17} />
        </button>
      </div>
      {runs.error && <Alert>{runs.error}</Alert>}
      {runs.value ? (
        <RunTable runs={orderedRuns(runs.value)} />
      ) : (
        !runs.error && <Loading />
      )}
    </section>
  )
}
function RunDetails({ id }: { id: string }) {
  const path = '/runs/' + idPath(id)
  const run = useResource<Run>(path, false, 4000)
  const artifacts = useResource<Artifact[]>(
    '/artifacts?runId=' + idPath(id),
    true,
    8000,
  )
  const [step, setStep] = useState('')
  const log = useResource<{ text: string }>(
    step ? path + '/logs?step=' + idPath(step) : null,
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [retryOpen, setRetryOpen] = useState(false)
  const cancel = async () => {
    if (!confirm('Cancel this execution?')) return
    setBusy(true)
    setError('')
    try {
      await request(path + '/cancel', { method: 'POST' })
      run.refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }
  if (!run.value) return run.error ? <Alert>{run.error}</Alert> : <Loading />
  const item = run.value
  const finished = item.steps.filter((entry) =>
    ['Succeeded', 'Failed', 'Cancelled', 'RequiresReview'].includes(
      entry.phase,
    ),
  ).length
  const cancellable =
    !item.cancelRequested && ['Pending', 'Running'].includes(item.phase)
  const retryable = ['Failed', 'Cancelled'].includes(item.phase)
  return (
    <div className="stack">
      <div className="inline-actions">
        <a className="button secondary" href="#/executions">
          Execution history
        </a>
      </div>
      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{item.workflowId}</h2>
            <p className="secondary-line">{item.id}</p>
          </div>
          <Badge phase={item.phase} />
        </div>
        <div className="panel-body stack">
          {run.error && <Alert>{run.error}</Alert>}
          {item.error && <Alert>{item.error}</Alert>}
          {error && <Alert>{error}</Alert>}
          <dl className="details-grid">
            <div>
              <dt>Queued</dt>
              <dd>{date(item.createdAt)}</dd>
            </div>
            <div>
              <dt>Started</dt>
              <dd>{date(item.startedAt)}</dd>
            </div>
            <div>
              <dt>Completed</dt>
              <dd>{date(item.completedAt)}</dd>
            </div>
          </dl>
          <div>
            <p className="progress-label">
              {finished} of {item.steps.length} steps finished
            </p>
            <progress value={finished} max={item.steps.length || 1}>
              {finished}
            </progress>
          </div>
          <div className="inline-actions">
            {cancellable && (
              <button
                className="button danger"
                onClick={() => void cancel()}
                disabled={busy}
              >
                Cancel execution
              </button>
            )}
            {retryable && (
              <button
                className="button secondary"
                onClick={() => setRetryOpen(true)}
              >
                Retry
              </button>
            )}
          </div>
        </div>
      </section>
      <section className="panel">
        <div className="panel-head">
          <h2>Steps</h2>
          <ListTree size={18} />
        </div>
        {item.steps.length ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Step</th>
                  <th>Status</th>
                  <th>Attempts</th>
                  <th>Started</th>
                  <th>Completed</th>
                  <th>Logs</th>
                </tr>
              </thead>
              <tbody>
                {item.steps.map((entry) => (
                  <tr key={entry.name}>
                    <td>
                      {entry.name}
                      {entry.error && (
                        <span className="secondary-line">{entry.error}</span>
                      )}
                    </td>
                    <td>
                      <Badge phase={entry.phase} />
                    </td>
                    <td>{entry.attempts}</td>
                    <td>{date(entry.startedAt)}</td>
                    <td>{date(entry.completedAt)}</td>
                    <td>
                      <button
                        type="button"
                        className="text-button"
                        disabled={!entry.attempts}
                        onClick={() => {
                          setStep(entry.name)
                          log.refresh()
                        }}
                      >
                        View logs
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Empty>The controller hasn’t recorded any steps yet.</Empty>
        )}
      </section>
      {step && (
        <section className="panel">
          <div className="panel-head">
            <h2>Logs · {step}</h2>
            <button
              className="icon-button"
              aria-label="Refresh logs"
              onClick={log.refresh}
            >
              <RefreshCw size={17} />
            </button>
          </div>
          {log.error ? (
            <Alert>{log.error}</Alert>
          ) : log.value ? (
            <pre className="logs">
              {log.value.text || 'The worker has not written any log output.'}
            </pre>
          ) : (
            <Loading />
          )}
        </section>
      )}
      {retryOpen && (
        <Modal title="Retry execution" close={() => setRetryOpen(false)}>
          <ActionForm
            label="Retry execution"
            submit={async (fields) => {
              const retried = await request<Run>(path + '/retry', {
                method: 'POST',
                body: { allowPaid: fields.has('allow-paid') },
              })
              setRetryOpen(false)
              location.hash = '#/executions/' + idPath(retried.id)
            }}
          >
            <p>
              Start a new execution using the original workflow definition and
              inputs.
            </p>
            <label className="check">
              <input name="allow-paid" type="checkbox" /> Approve charges if
              this execution uses external providers
            </label>
          </ActionForm>
        </Modal>
      )}
      <section className="panel">
        <div className="panel-head">
          <h2>Run outputs</h2>
          {artifacts.value?.length ? (
            <a className="text-button" href={`#/media/${idPath(item.id)}`}>
              Open in Media Library
            </a>
          ) : null}
        </div>
        {artifacts.error && <Alert>{artifacts.error}</Alert>}
        {artifacts.value?.length ? (
          <ul className="panel-body output-list">
            {artifacts.value.map((artifact) => (
              <li key={artifact.id}>
                <span>
                  {artifact.name}
                  <span className="secondary-line">
                    {bytes(artifact.sizeBytes)}
                  </span>
                </span>
                <a
                  className="button secondary"
                  href={
                    apiRoot + '/artifacts/' + idPath(artifact.id) + '/download'
                  }
                >
                  <Download size={16} /> Download
                </a>
              </li>
            ))}
          </ul>
        ) : artifacts.value ? (
          <Empty>No published outputs.</Empty>
        ) : (
          !artifacts.error && <Loading />
        )}
      </section>
    </div>
  )
}
