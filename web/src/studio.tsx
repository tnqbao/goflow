import { useEffect, useMemo, useRef, useState } from 'react'
import {
  CheckCircle2,
  Clapperboard,
  Download,
  Film,
  Image as ImageIcon,
  KeyRound,
  Loader2,
  Play,
  Sparkles,
  Square,
  Upload,
  Wand2,
  X,
  XCircle,
} from 'lucide-react'
import { apiRoot, errorMessage, idPath, request, uploadFile, useResource } from './api'
import { orderedRuns } from './runs'
import {
  ASPECT_OPTIONS,
  buildStudioRunBody,
  buildStudioWorkflowSpec,
  DEFAULT_FAL_IMAGE_MODEL,
  DEFAULT_FAL_T2V_MODEL,
  DEFAULT_FAL_VIDEO_MODEL,
  DEFAULT_GEMINI_MODEL,
  detectProviderReadiness,
  STARTER_PROMPTS,
  STYLE_PRESETS,
  studioWorkflowId,
  type ReferenceMode,
  type StudioDraft,
  type StudioMode,
} from './studioPresets'
import type { AspectRatio, Artifact, Provider, Run, Workflow } from './types'
import { Alert, Badge, Modal } from './ui'

type UploadedReference = {
  key: string
  name: string
  contentType: string
  sizeBytes: number
  url: string
}

type QuickKeyTarget = 'fal' | 'gemini' | null

export function Studio() {
  const providersRes = useResource<Provider[]>('/providers', true)
  const workflowsRes = useResource<Workflow[]>('/workflows', true)
  const runsRes = useResource<Run[]>('/runs', true, 10000)
  const artifactsRes = useResource<Artifact[]>('/artifacts', true, 12000)

  const readiness = useMemo(
    () => detectProviderReadiness(providersRes.value ?? []),
    [providersRes.value],
  )

  const [prompt, setPrompt] = useState('')
  const [style, setStyle] = useState('Cinematic')
  const [referenceNotes, setReferenceNotes] = useState('')
  const [aspectRatio, setAspectRatio] = useState<AspectRatio>('9:16')
  const [clips, setClips] = useState<1 | 2>(1)
  const [mode, setMode] = useState<StudioMode>('mock')
  const [referenceMode, setReferenceMode] = useState<ReferenceMode>('none')
  const [uploadedRef, setUploadedRef] = useState<UploadedReference | null>(null)
  const [uploadingRef, setUploadingRef] = useState(false)
  const [paidApproved, setPaidApproved] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [activeRunId, setActiveRunId] = useState<string | null>(null)
  const [keyModal, setKeyModal] = useState<QuickKeyTarget>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const activeRunRes = useResource<Run>(
    activeRunId ? '/runs/' + idPath(activeRunId) : null,
    false,
    activeRunId ? 2500 : 0,
  )
  const activeArtifactsRes = useResource<Artifact[]>(
    activeRunRes.value?.phase === 'Succeeded' && activeRunId
      ? '/artifacts?runId=' + idPath(activeRunId)
      : null,
    true,
  )

  const activeRun = activeRunRes.value
  const isRunInFlight =
    !!activeRun && ['Pending', 'Running', 'Cancelling'].includes(activeRun.phase)

  const activeVideoArtifact = useMemo(
    () =>
      activeArtifactsRes.value?.find((a) => a.contentType.startsWith('video/')) ??
      null,
    [activeArtifactsRes.value],
  )

  const recentVideos = useMemo(
    () =>
      (artifactsRes.value ?? [])
        .filter((a) => a.contentType.startsWith('video/'))
        .slice(0, 6),
    [artifactsRes.value],
  )

  const recentRuns = useMemo(
    () => orderedRuns(runsRes.value ?? []).slice(0, 4),
    [runsRes.value],
  )

  useEffect(() => {
    if (activeRun?.phase === 'Succeeded') {
      artifactsRes.refresh()
      runsRes.refresh()
    }
  }, [activeRun?.phase])

  const selectedFal = readiness.falProviders[0]
  const selectedGemini = readiness.geminiProviders[0]

  const resolveFalVideoModel = () => {
    const isI2V = referenceMode === 'upload' || referenceMode === 'auto-image'
    const fallback = isI2V ? DEFAULT_FAL_VIDEO_MODEL : DEFAULT_FAL_T2V_MODEL
    if (!selectedFal?.models?.length) return fallback
    const match = selectedFal.models.find((m) =>
      isI2V ? m.includes('image-to-video') : m.includes('text-to-video'),
    )
    return match ?? selectedFal.models[0] ?? fallback
  }

  const resolveFalImageModel = () => {
    if (!selectedFal?.models?.length) return DEFAULT_FAL_IMAGE_MODEL
    const match = selectedFal.models.find(
      (m) => m.includes('flux') || m.includes('image') && !m.includes('video'),
    )
    return match ?? DEFAULT_FAL_IMAGE_MODEL
  }

  const handleUploadReference = async (file: File | undefined) => {
    if (!file) return
    setUploadingRef(true)
    setError('')
    try {
      const ref = await uploadFile<UploadedReference>('/artifacts/references', file)
      setUploadedRef(ref)
      setReferenceMode('upload')
      setNotice(`Reference image uploaded (${file.name}).`)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setUploadingRef(false)
      if (fileInputRef.current) fileInputRef.current.value = ''
    }
  }

  const handleGenerate = async () => {
    setError('')
    setNotice('')

    if (mode !== 'mock' && !readiness.hasFal) {
      setKeyModal('fal')
      return
    }
    if (mode === 'full-ai' && !readiness.hasGemini) {
      setKeyModal('gemini')
      return
    }
    if (mode !== 'mock' && !paidApproved) {
      setError('Confirm paid API usage before running with external AI providers.')
      return
    }

    const draft: StudioDraft = {
      prompt,
      style,
      referenceNotes,
      aspectRatio,
      clips,
      mode,
      referenceMode,
      referenceImageKey: uploadedRef?.key ?? '',
      geminiProviderId: selectedGemini?.id ?? 'gemini',
      falProviderId: selectedFal?.id ?? 'fal',
      llmModel: selectedGemini?.models?.[0] ?? DEFAULT_GEMINI_MODEL,
      imageModel: resolveFalImageModel(),
      videoModel: resolveFalVideoModel(),
    }

    let runBody: string
    try {
      runBody = buildStudioRunBody(draft)
    } catch (err) {
      setError(errorMessage(err))
      return
    }

    setSubmitting(true)
    try {
      const wfId = studioWorkflowId(draft)
      const spec = buildStudioWorkflowSpec(draft)
      const existing = workflowsRes.value?.find((w) => w.id === wfId)

      if (existing) {
        await request('/workflows/' + idPath(wfId), {
          method: 'PATCH',
          body: { resourceVersion: existing.resourceVersion, spec },
        })
      } else {
        await request('/workflows', {
          method: 'POST',
          body: { id: wfId, spec },
        })
      }

      const run = await request<Run>('/workflows/' + idPath(wfId) + '/runs', {
        method: 'POST',
        body: runBody,
      })
      setActiveRunId(run.id)
      workflowsRes.refresh()
      runsRes.refresh()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSubmitting(false)
    }
  }

  const handleCancelActive = async () => {
    if (!activeRunId) return
    try {
      await request('/runs/' + idPath(activeRunId) + '/cancel', { method: 'POST' })
      activeRunRes.refresh()
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  return (
    <div className="studio-root">
      {/* Quick API Key Status & 1-Click Connect Bar */}
      <section className="studio-keybar" aria-label="AI Provider connections">
        <div className="studio-keybar-left">
          <span className="studio-pill studio-pill-ok">
            <CheckCircle2 size={14} /> Mock Test Ready ($0)
          </span>
          <button
            type="button"
            className={`studio-pill ${readiness.hasFal ? 'studio-pill-ok' : 'studio-pill-action'}`}
            onClick={() => setKeyModal('fal')}
          >
            <KeyRound size={14} />
            {readiness.hasFal
              ? `fal.ai Connected (${selectedFal?.id})`
              : 'Connect fal.ai Key (Image/Video)'}
          </button>
          <button
            type="button"
            className={`studio-pill ${readiness.hasGemini ? 'studio-pill-ok' : 'studio-pill-action'}`}
            onClick={() => setKeyModal('gemini')}
          >
            <Sparkles size={14} />
            {readiness.hasGemini
              ? `Gemini Connected (${selectedGemini?.id})`
              : 'Connect Gemini Key (Script AI)'}
          </button>
        </div>
        <a className="studio-link-subtle" href="#/workflows">
          Advanced DAG Editor →
        </a>
      </section>

      {error && <Alert>{error}</Alert>}
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}

      <div className="studio-grid">
        {/* LEFT COLUMN: 1-Click Video Composer */}
        <section className="panel studio-composer">
          <div className="studio-section">
            <div className="studio-label-row">
              <label htmlFor="studio-prompt">What video do you want to create?</label>
              <span className="studio-hint">Describe scene, subject, mood & motion</span>
            </div>
            <textarea
              id="studio-prompt"
              className="studio-prompt-input"
              rows={4}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="A cinematic drone shot gliding through misty emerald mountains at sunrise, golden light rays..."
            />
            <div className="studio-chip-row">
              {STARTER_PROMPTS.map((item) => (
                <button
                  key={item.label}
                  type="button"
                  className="studio-chip"
                  onClick={() => {
                    setPrompt(item.prompt)
                    setStyle(item.style)
                  }}
                >
                  <Wand2 size={12} /> {item.label}
                </button>
              ))}
            </div>
          </div>

          {/* Reference Section: Image + Style + Notes */}
          <div className="studio-section">
            <div className="studio-label-row">
              <span>Reference & Style</span>
              <span className="studio-hint">Upload image, auto-generate frame, or pick style</span>
            </div>

            <div className="studio-segmented" role="group" aria-label="Reference mode">
              <button
                type="button"
                className={referenceMode === 'none' ? 'is-active' : ''}
                onClick={() => setReferenceMode('none')}
              >
                Text only
              </button>
              <button
                type="button"
                className={referenceMode === 'upload' ? 'is-active' : ''}
                onClick={() => setReferenceMode('upload')}
              >
                <Upload size={14} /> Upload Reference
              </button>
              <button
                type="button"
                className={referenceMode === 'auto-image' ? 'is-active' : ''}
                onClick={() => setReferenceMode('auto-image')}
              >
                <ImageIcon size={14} /> Auto Reference Frame
              </button>
            </div>

            {referenceMode === 'upload' && (
              <div className="studio-dropzone">
                <input
                  ref={fileInputRef}
                  type="file"
                  accept="image/png,image/jpeg,image/webp"
                  hidden
                  onChange={(e) => void handleUploadReference(e.target.files?.[0])}
                />
                {uploadedRef ? (
                  <div className="studio-ref-preview">
                    <img src={uploadedRef.url} alt={uploadedRef.name} />
                    <div className="studio-ref-meta">
                      <strong>Reference image ready</strong>
                      <span>
                        {uploadedRef.contentType.replace('image/', '').toUpperCase()} ·{' '}
                        {Math.max(1, Math.round(uploadedRef.sizeBytes / 1024))} KB
                      </span>
                      <div className="inline-actions">
                        <button
                          type="button"
                          className="button secondary"
                          onClick={() => fileInputRef.current?.click()}
                          disabled={uploadingRef}
                        >
                          Replace
                        </button>
                        <button
                          type="button"
                          className="icon-button"
                          aria-label="Remove reference image"
                          onClick={() => setUploadedRef(null)}
                        >
                          <X size={16} />
                        </button>
                      </div>
                    </div>
                  </div>
                ) : (
                  <button
                    type="button"
                    className="studio-dropzone-btn"
                    disabled={uploadingRef}
                    onClick={() => fileInputRef.current?.click()}
                  >
                    {uploadingRef ? (
                      <>
                        <Loader2 size={18} className="spin" /> Uploading reference image…
                      </>
                    ) : (
                      <>
                        <Upload size={18} />
                        <span>Click to upload PNG, JPG or WebP reference image (max 10 MB)</span>
                      </>
                    )}
                  </button>
                )}
              </div>
            )}

            {referenceMode === 'auto-image' && (
              <p className="studio-note-box">
                AI will generate a reference keyframe image first, then animate it into video clips
                automatically.
              </p>
            )}

            <div className="studio-chip-row">
              {STYLE_PRESETS.map((preset) => (
                <button
                  key={preset}
                  type="button"
                  className={`studio-chip ${style === preset ? 'is-selected' : ''}`}
                  onClick={() => setStyle(preset)}
                >
                  {preset}
                </button>
              ))}
            </div>

            <input
              type="text"
              className="studio-notes-input"
              value={referenceNotes}
              onChange={(e) => setReferenceNotes(e.target.value)}
              placeholder="Optional reference notes: camera motion, lighting, character look..."
            />
          </div>

          {/* Aspect Ratio & Engine Bar */}
          <div className="studio-section">
            <div className="studio-label-row">
              <span>Aspect Ratio</span>
            </div>
            <div className="studio-ratio-grid">
              {ASPECT_OPTIONS.map((opt) => (
                <button
                  key={opt.value}
                  type="button"
                  className={`studio-ratio-card ${aspectRatio === opt.value ? 'is-active' : ''}`}
                  onClick={() => setAspectRatio(opt.value)}
                >
                  <span className={`ratio-box ratio-${opt.value.replace(':', 'x')}`} />
                  <strong>{opt.label}</strong>
                  <small>{opt.hint}</small>
                </button>
              ))}
            </div>
          </div>

          <div className="studio-section studio-inline-controls">
            <div>
              <span className="studio-mini-label">Engine</span>
              <div className="studio-segmented" role="group" aria-label="Generation engine">
                <button
                  type="button"
                  className={mode === 'mock' ? 'is-active' : ''}
                  onClick={() => setMode('mock')}
                >
                  Mock ($0)
                </button>
                <button
                  type="button"
                  className={mode === 'fal' ? 'is-active' : ''}
                  onClick={() => setMode('fal')}
                >
                  fal.ai Video
                </button>
                <button
                  type="button"
                  className={mode === 'full-ai' ? 'is-active' : ''}
                  onClick={() => setMode('full-ai')}
                >
                  Gemini + fal.ai
                </button>
              </div>
            </div>

            <div>
              <span className="studio-mini-label">Scenes</span>
              <div className="studio-segmented" role="group" aria-label="Scene count">
                <button
                  type="button"
                  className={clips === 1 ? 'is-active' : ''}
                  onClick={() => setClips(1)}
                >
                  1 Clip
                </button>
                <button
                  type="button"
                  className={clips === 2 ? 'is-active' : ''}
                  onClick={() => setClips(2)}
                >
                  2 Clips
                </button>
              </div>
            </div>
          </div>

          {mode !== 'mock' && (
            <label className="studio-paid-check">
              <input
                type="checkbox"
                checked={paidApproved}
                onChange={(e) => setPaidApproved(e.target.checked)}
              />
              <span>
                Approve billable external API call using my connected{' '}
                {mode === 'full-ai' ? 'Gemini & fal.ai' : 'fal.ai'} key
              </span>
            </label>
          )}

          <button
            type="button"
            className="button studio-generate-btn"
            disabled={submitting || !prompt.trim()}
            onClick={() => void handleGenerate()}
          >
            {submitting ? (
              <>
                <Loader2 size={18} className="spin" /> Launching pipeline…
              </>
            ) : (
              <>
                <Play size={18} /> Generate Video
              </>
            )}
          </button>
        </section>

        {/* RIGHT COLUMN: Live Preview Stage & Output Player */}
        <section className="panel studio-stage">
          <div className="panel-head">
            <h2>
              <Clapperboard size={18} /> Live Output Stage
            </h2>
            {activeRun && <Badge phase={activeRun.phase} />}
          </div>

          {!activeRun ? (
            <div className="studio-stage-empty">
              <div className={`studio-frame-preview ratio-${aspectRatio.replace(':', 'x')}`}>
                <Film size={32} />
                <span>{aspectRatio}</span>
              </div>
              <h3>Ready to produce</h3>
              <p>
                Enter an idea on the left, optionally attach a reference image, and click{' '}
                <strong>Generate Video</strong>. KubeReel handles scripting, video generation, and
                FFmpeg rendering automatically.
              </p>
              {recentRuns.length > 0 && (
                <div className="studio-recent-runs">
                  <span className="studio-mini-label">Jump to recent run</span>
                  <div className="studio-chip-row">
                    {recentRuns.map((run) => (
                      <button
                        key={run.id}
                        type="button"
                        className="studio-chip"
                        onClick={() => setActiveRunId(run.id)}
                      >
                        {run.id} · {run.phase}
                      </button>
                    ))}
                  </div>
                </div>
              )}
            </div>
          ) : (
            <div className="studio-stage-active">
              {activeVideoArtifact ? (
                <div className="studio-video-wrap">
                  <video
                    key={activeVideoArtifact.id}
                    src={`${apiRoot}/artifacts/${idPath(activeVideoArtifact.id)}/download`}
                    controls
                    autoPlay
                    muted
                    loop
                    playsInline
                  />
                  <div className="studio-video-actions">
                    <a
                      className="button primary"
                      href={`${apiRoot}/artifacts/${idPath(activeVideoArtifact.id)}/download`}
                      download={activeVideoArtifact.name}
                    >
                      <Download size={16} /> Download MP4
                    </a>
                    <a
                      className="button secondary"
                      href={`#/media/${encodeURIComponent(activeRun.id)}`}
                    >
                      Open in Media Library
                    </a>
                    <a
                      className="button secondary"
                      href={`#/executions/${encodeURIComponent(activeRun.id)}`}
                    >
                      Inspect logs
                    </a>
                  </div>
                </div>
              ) : (
                <div className="studio-progress-box">
                  <div className="studio-progress-head">
                    <div>
                      <strong>Run {activeRun.id}</strong>
                      <span className="secondary-line">
                        {isRunInFlight
                          ? 'Executing Kubernetes worker & FFmpeg render jobs…'
                          : activeRun.error || `Finished with status ${activeRun.phase}`}
                      </span>
                    </div>
                    {isRunInFlight && (
                      <button
                        type="button"
                        className="button danger"
                        onClick={() => void handleCancelActive()}
                      >
                        <Square size={14} /> Cancel
                      </button>
                    )}
                  </div>

                  <ol className="studio-stepper">
                    {activeRun.steps.map((step) => (
                      <li key={step.name} className={`step-${step.phase.toLowerCase()}`}>
                        <span className="step-dot">
                          {step.phase === 'Succeeded' ? (
                            <CheckCircle2 size={16} />
                          ) : step.phase === 'Failed' ? (
                            <XCircle size={16} />
                          ) : step.phase === 'Running' ? (
                            <Loader2 size={16} className="spin" />
                          ) : (
                            <span className="dot-idle" />
                          )}
                        </span>
                        <div className="step-info">
                          <strong>{step.name}</strong>
                          <span>{step.error ? step.error : step.phase}</span>
                        </div>
                      </li>
                    ))}
                  </ol>
                </div>
              )}
            </div>
          )}

          {recentVideos.length > 0 && (
            <div className="studio-gallery-strip">
              <div className="studio-label-row">
                <span>Recent Generated Videos</span>
                <a className="studio-link-subtle" href="#/media">
                  View all →
                </a>
              </div>
              <div className="studio-thumb-grid">
                {recentVideos.map((vid) => (
                  <button
                    key={vid.id}
                    type="button"
                    className="studio-thumb-card"
                    onClick={() => setActiveRunId(vid.runId)}
                  >
                    <video
                      src={`${apiRoot}/artifacts/${idPath(vid.id)}/download#t=0.1`}
                      muted
                      preload="metadata"
                      playsInline
                    />
                    <span>{vid.name}</span>
                  </button>
                ))}
              </div>
            </div>
          )}
        </section>
      </div>

      {keyModal && (
        <QuickKeyModal
          kind={keyModal}
          existing={keyModal === 'fal' ? selectedFal : selectedGemini}
          close={() => setKeyModal(null)}
          done={(msg) => {
            setKeyModal(null)
            setNotice(msg)
            providersRes.refresh()
          }}
        />
      )}
    </div>
  )
}

function QuickKeyModal({
  kind,
  existing,
  close,
  done,
}: {
  kind: 'fal' | 'gemini'
  existing?: Provider
  close: () => void
  done: (message: string) => void
}) {
  const [apiKey, setApiKey] = useState('')
  const [modelsText, setModelsText] = useState(
    existing?.models?.join(', ') ||
      (kind === 'fal'
        ? `${DEFAULT_FAL_IMAGE_MODEL}, ${DEFAULT_FAL_VIDEO_MODEL}, ${DEFAULT_FAL_T2V_MODEL}`
        : DEFAULT_GEMINI_MODEL),
  )
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!apiKey.trim() && !existing) {
      setErr('Paste your API key to connect.')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const models = modelsText
        .split(/[,\n]/)
        .map((m) => m.trim())
        .filter(Boolean)
      const providerId = existing?.id ?? kind
      const capabilities = kind === 'fal' ? ['image', 'video'] : ['text']

      if (existing) {
        await request('/providers/' + idPath(providerId), {
          method: 'PATCH',
          body: {
            enabled: true,
            capabilities,
            models,
            ...(apiKey.trim() ? { apiKey: apiKey.trim() } : {}),
          },
        })
      } else {
        await request('/providers', {
          method: 'POST',
          body: {
            id: providerId,
            type: kind,
            enabled: true,
            capabilities,
            models,
            apiKey: apiKey.trim(),
          },
        })
      }
      done(
        `${kind === 'fal' ? 'fal.ai' : 'Google Gemini'} API key saved securely in Kubernetes Secret.`,
      )
    } catch (submitErr) {
      setErr(errorMessage(submitErr))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={
        kind === 'fal' ? 'Connect fal.ai API Key' : 'Connect Google Gemini API Key'
      }
      close={close}
    >
      <form className="action-form" onSubmit={(e) => void handleSave(e)}>
        {err && <Alert>{err}</Alert>}
        <label>
          API Key
          <input
            type="password"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            placeholder={existing ? 'Leave blank to keep current key' : 'Paste your API key'}
            autoFocus
          />
          <small>Stored in a namespace Kubernetes Secret. Never returned to the browser.</small>
        </label>
        <label>
          Default Models
          <textarea
            rows={2}
            value={modelsText}
            onChange={(e) => setModelsText(e.target.value)}
          />
        </label>
        <div className="form-actions">
          <button type="button" className="button secondary" onClick={close}>
            Cancel
          </button>
          <button type="submit" className="button primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save & Connect'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
