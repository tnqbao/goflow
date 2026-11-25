import { useEffect, useMemo, useState } from 'react'
import {
  Clapperboard,
  Download,
  Eye,
  FileCode2,
  Film,
  Image as ImageIcon,
  Layers,
  Play,
  RefreshCw,
  Search,
  Trash2,
} from 'lucide-react'
import { apiRoot, errorMessage, idPath, request, useResource } from './api'
import {
  categoryLabel,
  classifyArtifact,
  filterArtifacts,
  summarizeMediaCounts,
  type MediaCategory,
  type MediaFilter,
} from './mediaData'
import type { Artifact } from './types'
import {
  ActionForm,
  Alert,
  bytes,
  date,
  Empty,
  Loading,
  Modal,
} from './ui'

const categoryTone: Record<MediaCategory, string> = {
  final_video: 'green',
  clip: 'amber',
  image: 'blue',
  script: 'violet',
}

function artifactDownloadHref(id: string): string {
  return `${apiRoot}/artifacts/${idPath(id)}/download`
}

export function MediaLibrary({ initialQuery = '' }: { initialQuery?: string }) {
  const artifacts = useResource<Artifact[]>('/artifacts', true, 15000)
  const [filter, setFilter] = useState<MediaFilter>('all')
  const [query, setQuery] = useState(initialQuery)
  const [previewing, setPreviewing] = useState<Artifact>()
  const [deleting, setDeleting] = useState<Artifact>()
  const [notice, setNotice] = useState('')

  useEffect(() => {
    if (initialQuery) setQuery(initialQuery)
  }, [initialQuery])

  const counts = useMemo(
    () => summarizeMediaCounts(artifacts.value ?? []),
    [artifacts.value],
  )

  const visible = useMemo(
    () => filterArtifacts(artifacts.value ?? [], filter, query),
    [artifacts.value, filter, query],
  )

  const summaryTiles = [
    {
      title: 'Final videos',
      count: counts.final_video,
      tone: 'green',
      target: 'final_video' as MediaFilter,
      icon: Clapperboard,
    },
    {
      title: 'Generated clips',
      count: counts.clip,
      tone: 'amber',
      target: 'clip' as MediaFilter,
      icon: Film,
    },
    {
      title: 'Generated images',
      count: counts.image,
      tone: 'blue',
      target: 'image' as MediaFilter,
      icon: ImageIcon,
    },
    {
      title: 'Total artifacts',
      count: counts.all,
      tone: 'violet',
      target: 'all' as MediaFilter,
      icon: Layers,
    },
  ]

  const tabs: { key: MediaFilter; label: string; count: number }[] = [
    { key: 'all', label: 'All', count: counts.all },
    { key: 'final_video', label: 'Final videos', count: counts.final_video },
    { key: 'clip', label: 'Generated clips', count: counts.clip },
    { key: 'image', label: 'Generated images', count: counts.image },
    ...(counts.script > 0 || filter === 'script'
      ? [{ key: 'script' as MediaFilter, label: 'Scripts', count: counts.script }]
      : []),
  ]

  return (
    <div className="stack">
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}
      {artifacts.error && <Alert>{artifacts.error}</Alert>}

      <div className="metric-grid">
        {summaryTiles.map(({ title, count, tone, target, icon: Icon }) => (
          <button
            type="button"
            key={title}
            className={`metric panel media-summary-tile${filter === target ? ' is-selected' : ''}`}
            onClick={() => setFilter(target)}
          >
            <span className={`metric-icon tone-${tone}`}>
              <Icon size={20} />
            </span>
            <span>{title}</span>
            <strong>
              {artifacts.value ? count.toLocaleString() : '—'}
            </strong>
          </button>
        ))}
      </div>

      <section className="panel">
        <div className="panel-head media-toolbar">
          <div className="wf-segmented" role="tablist" aria-label="Media category">
            {tabs.map((tab) => (
              <button
                type="button"
                role="tab"
                key={tab.key}
                aria-selected={filter === tab.key}
                className={filter === tab.key ? 'is-active' : ''}
                onClick={() => setFilter(tab.key)}
              >
                {tab.label} ({tab.count})
              </button>
            ))}
          </div>

          <div className="inline-actions">
            <span className="input-with-icon media-search">
              <Search size={15} />
              <input
                type="search"
                aria-label="Filter media library"
                placeholder="Filter by file, run ID or type…"
                value={query}
                onChange={(event) => setQuery(event.target.value)}
              />
            </span>
            <button
              type="button"
              className="icon-button"
              aria-label="Refresh media library"
              onClick={() => {
                setNotice('')
                artifacts.refresh()
              }}
            >
              <RefreshCw size={17} />
            </button>
          </div>
        </div>

        {!artifacts.value ? (
          !artifacts.error && <Loading />
        ) : visible.length === 0 ? (
          <Empty>
            {artifacts.value.length === 0
              ? 'No generated media yet. Run a workflow to populate the Media Library.'
              : 'No artifacts match the active filter.'}
          </Empty>
        ) : (
          <div className="media-grid">
            {visible.map((item) => {
              const category = classifyArtifact(item)
              const tone = categoryTone[category]
              const href = artifactDownloadHref(item.id)
              const isVideo = category === 'final_video' || category === 'clip'
              const isImage = category === 'image'

              return (
                <article className="media-card" key={item.id}>
                  <button
                    type="button"
                    className="media-stage"
                    aria-label={`Preview ${item.name} from ${item.runId}`}
                    onClick={() => setPreviewing(item)}
                  >
                    {isImage ? (
                      <img
                        loading="lazy"
                        src={href}
                        alt={`${item.name} (${item.runId})`}
                      />
                    ) : isVideo ? (
                      <>
                        <video
                          preload="metadata"
                          muted
                          playsInline
                          src={`${href}#t=0.1`}
                        />
                        <span className="media-play-pill" aria-hidden="true">
                          <Play size={18} />
                        </span>
                      </>
                    ) : (
                      <span className="media-doc-icon" aria-hidden="true">
                        <FileCode2 size={34} />
                      </span>
                    )}
                  </button>

                  <div className="media-card-body">
                    <div className="media-card-meta-top">
                      <span className={`wf-node-badge tone-${tone}`}>
                        {categoryLabel(category)}
                      </span>
                      <span className="media-size">{bytes(item.sizeBytes)}</span>
                    </div>

                    <strong className="media-title" title={item.name}>
                      {item.name}
                    </strong>

                    <dl className="media-meta-list">
                      <div>
                        <dt>Run</dt>
                        <dd>
                          <a
                            className="row-link"
                            href={`#/executions/${idPath(item.runId)}`}
                          >
                            {item.runId}
                          </a>
                        </dd>
                      </div>
                      <div>
                        <dt>Format</dt>
                        <dd>{item.contentType}</dd>
                      </div>
                      <div>
                        <dt>Modified</dt>
                        <dd>{date(item.modifiedAt)}</dd>
                      </div>
                    </dl>
                  </div>

                  <div className="media-card-actions">
                    <button
                      type="button"
                      className="button secondary"
                      onClick={() => setPreviewing(item)}
                    >
                      <Eye size={15} /> Preview
                    </button>
                    <a
                      className="button secondary"
                      href={href}
                      download={item.name}
                    >
                      <Download size={15} /> Download
                    </a>
                    <button
                      type="button"
                      className="icon-button"
                      aria-label={`Delete ${item.name}`}
                      onClick={() => setDeleting(item)}
                    >
                      <Trash2 size={16} />
                    </button>
                  </div>
                </article>
              )
            })}
          </div>
        )}
      </section>

      {previewing && (
        <PreviewModal
          item={previewing}
          close={() => setPreviewing(undefined)}
          onRequestDelete={(target) => {
            setPreviewing(undefined)
            setDeleting(target)
          }}
        />
      )}

      {deleting && (
        <Modal
          title="Delete artifact"
          close={() => setDeleting(undefined)}
        >
          <ActionForm
            label="Delete artifact"
            submit={async () => {
              await request('/artifacts/' + idPath(deleting.id), {
                method: 'DELETE',
              })
              const deletedName = deleting.name
              setDeleting(undefined)
              setNotice(`Deleted artifact "${deletedName}".`)
              artifacts.refresh()
            }}
          >
            <p>
              Permanently delete <strong>{deleting.name}</strong> from execution{' '}
              <strong>{deleting.runId}</strong> ({bytes(deleting.sizeBytes)})?
            </p>
          </ActionForm>
        </Modal>
      )}
    </div>
  )
}

function PreviewModal({
  item,
  close,
  onRequestDelete,
}: {
  item: Artifact
  close: () => void
  onRequestDelete: (target: Artifact) => void
}) {
  const details = useResource<Artifact>('/artifacts/' + idPath(item.id))
  const active = details.value ?? item
  const category = classifyArtifact(active)
  const href = artifactDownloadHref(active.id)
  const isVideo = category === 'final_video' || category === 'clip'
  const isImage = category === 'image'

  const [textPreview, setTextPreview] = useState<string>()
  const [textError, setTextError] = useState('')

  useEffect(() => {
    if (isVideo || isImage || active.sizeBytes > 512 * 1024) return
    const controller = new AbortController()
    let mounted = true
    const loadText = async () => {
      try {
        const resp = await fetch(href, {
          credentials: 'same-origin',
          signal: controller.signal,
        })
        if (!resp.ok) throw new Error(`Preview failed (HTTP ${resp.status}).`)
        const raw = await resp.text()
        let formatted = raw
        try {
          formatted = JSON.stringify(JSON.parse(raw), null, 2)
        } catch {
          // Keep plain text when not valid JSON.
        }
        if (mounted) {
          setTextPreview(formatted)
          setTextError('')
        }
      } catch (err) {
        if (mounted && !controller.signal.aborted) {
          setTextError(errorMessage(err))
        }
      }
    }
    void loadText()
    return () => {
      mounted = false
      controller.abort()
    }
  }, [active.id, active.sizeBytes, href, isImage, isVideo])

  return (
    <Modal title={`Preview · ${active.name}`} close={close}>
      <div className="action-form media-preview-modal">
        {details.error && <Alert>{details.error}</Alert>}
        {textError && <Alert>{textError}</Alert>}

        <div className="media-preview-box">
          {isVideo ? (
            <video
              controls
              autoPlay
              preload="metadata"
              src={href}
              className="media-modal-player"
            />
          ) : isImage ? (
            <img
              src={href}
              alt={`${active.name} (${active.runId})`}
              className="media-modal-image"
            />
          ) : textPreview !== undefined ? (
            <pre className="logs">{textPreview}</pre>
          ) : (
            <p className="secondary-line">
              Binary or large artifact preview unavailable; download the file to
              inspect it locally.
            </p>
          )}
        </div>

        <dl className="details-grid">
          <div>
            <dt>Category</dt>
            <dd>{categoryLabel(category)}</dd>
          </div>
          <div>
            <dt>File name</dt>
            <dd>{active.name}</dd>
          </div>
          <div>
            <dt>Content type</dt>
            <dd>{active.contentType}</dd>
          </div>
          <div>
            <dt>File size</dt>
            <dd>
              {bytes(active.sizeBytes)} ({active.sizeBytes.toLocaleString()} B)
            </dd>
          </div>
          <div>
            <dt>Modified</dt>
            <dd>{date(active.modifiedAt)}</dd>
          </div>
          <div>
            <dt>Execution run</dt>
            <dd>
              <a
                className="row-link"
                href={`#/executions/${idPath(active.runId)}`}
                onClick={close}
              >
                {active.runId}
              </a>
            </dd>
          </div>
        </dl>

        <div className="media-id-row">
          <small>Artifact ID: {active.id}</small>
        </div>

        <div className="media-modal-footer">
          <button
            type="button"
            className="button danger"
            onClick={() => onRequestDelete(active)}
          >
            <Trash2 size={15} /> Delete
          </button>
          <a
            className="button primary"
            href={href}
            download={active.name}
          >
            <Download size={15} /> Download
          </a>
        </div>
      </div>
    </Modal>
  )
}
