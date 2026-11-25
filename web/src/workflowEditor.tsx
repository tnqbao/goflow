import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type DragEvent,
} from 'react'
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type Connection,
  type Edge,
  type Node,
  type NodeProps,
  type OnEdgesChange,
  type OnNodesChange,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import {
  ArrowLeft,
  CheckCircle2,
  Code2,
  Film,
  GitFork,
  Image as ImageIcon,
  LayoutGrid,
  LogIn,
  LogOut,
  Plus,
  Save,
  Sliders,
  Sparkles,
  Trash2,
} from 'lucide-react'
import { errorMessage, idPath, request, useResource } from './api'
import type {
  AspectRatio,
  FFmpegOperation,
  Input,
  InputType,
  Provider,
  StepType,
  Workflow,
  WorkflowOutput,
  WorkflowSpec,
  WorkflowStep,
} from './types'
import { Alert } from './ui'
import {
  ASPECT_RATIOS,
  buildPatchSpec,
  changeStepType,
  computeStepPositions,
  connectDependency,
  createDefaultStep,
  defaultWorkflowSpec,
  disconnectDependency,
  FFMPEG_OPERATIONS,
  INPUT_TYPES,
  isProviderStepType,
  normalizeWorkflowSpec,
  parseWorkflowSpecJSON,
  removeStep,
  renameStep,
  STEP_TYPES,
  validateWorkflowDraft,
  type Point,
} from './workflowGraph'

const DRAG_MIME = 'application/kubereel-step-type'

const stepMeta: Record<
  StepType,
  { label: string; hint: string; tone: string; icon: typeof Sparkles }
> = {
  input: {
    label: 'Input',
    hint: 'Workflow input stage',
    tone: 'slate',
    icon: LogIn,
  },
  llm: {
    label: 'LLM',
    hint: 'Script & prompt generation',
    tone: 'violet',
    icon: Sparkles,
  },
  image: {
    label: 'Image',
    hint: 'Reference & scene images',
    tone: 'blue',
    icon: ImageIcon,
  },
  video: {
    label: 'Video',
    hint: 'AI video clip generation',
    tone: 'amber',
    icon: Film,
  },
  ffmpeg: {
    label: 'FFmpeg',
    hint: 'Controlled media processing',
    tone: 'green',
    icon: Sliders,
  },
  output: {
    label: 'Output',
    hint: 'Final deliverable stage',
    tone: 'rose',
    icon: LogOut,
  },
}

const capabilityByStep: Partial<Record<StepType, string>> = {
  llm: 'text',
  image: 'image',
  video: 'video',
}

type StepNodeData = {
  step: WorkflowStep
  isOutput: boolean
}

function stepSummary(step: WorkflowStep): string {
  if (step.type === 'ffmpeg') {
    const op = step.ffmpeg?.operation ?? 'render_mp4'
    const detail =
      step.ffmpeg?.aspectRatio ??
      (step.ffmpeg?.width && step.ffmpeg?.height
        ? `${step.ffmpeg.width}×${step.ffmpeg.height}`
        : '')
    return detail ? `${op} · ${detail}` : op
  }
  if (step.providerRef) {
    return step.model ? `${step.providerRef} · ${step.model}` : step.providerRef
  }
  return stepMeta[step.type]?.hint ?? step.type
}

function StepCardNode({ data, selected }: NodeProps<Node<StepNodeData>>) {
  const meta = stepMeta[data.step.type] ?? stepMeta.video
  const Icon = meta.icon
  return (
    <div
      className={`wf-node wf-tone-${meta.tone}${selected ? ' is-selected' : ''}`}
    >
      <Handle
        type="target"
        position={Position.Left}
        className="wf-handle"
        aria-label={`Target ${data.step.name}`}
      />
      <div className="wf-node-head">
        <span className={`wf-node-badge tone-${meta.tone}`}>
          <Icon size={13} />
          {meta.label}
        </span>
        {data.isOutput && <span className="wf-output-pill">Output</span>}
      </div>
      <strong className="wf-node-title">{data.step.name}</strong>
      <span className="wf-node-sub">{stepSummary(data.step)}</span>
      <Handle
        type="source"
        position={Position.Right}
        className="wf-handle"
        aria-label={`Source ${data.step.name}`}
      />
    </div>
  )
}

const nodeTypes = { workflowStep: StepCardNode }

export function WorkflowEditor({
  item,
  onCancel,
  onSaved,
}: {
  item?: Workflow
  onCancel: () => void
  onSaved: () => void
}) {
  return (
    <ReactFlowProvider>
      <WorkflowEditorInner item={item} onCancel={onCancel} onSaved={onSaved} />
    </ReactFlowProvider>
  )
}

function WorkflowEditorInner({
  item,
  onCancel,
  onSaved,
}: {
  item?: Workflow
  onCancel: () => void
  onSaved: () => void
}) {
  const initial = useMemo(
    () => structuredClone(item?.spec ?? defaultWorkflowSpec()),
    [item],
  )
  const [workflowId, setWorkflowId] = useState(item?.id ?? '')
  const [spec, setSpec] = useState<WorkflowSpec>(initial)
  const [positions, setPositions] = useState<Record<string, Point>>(() =>
    computeStepPositions(initial.steps),
  )
  const [measured, setMeasured] = useState<
    Record<string, { width: number; height: number }>
  >({})
  const [selectedStepName, setSelectedStepName] = useState<string | undefined>(
    initial.steps[0]?.name,
  )
  const [inspectorTab, setInspectorTab] = useState<'node' | 'workflow'>('node')
  const [viewMode, setViewMode] = useState<'visual' | 'json'>('visual')
  const [jsonDraft, setJsonDraft] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [saving, setSaving] = useState(false)

  const providersData = useResource<Provider[]>('/providers', true)
  const { screenToFlowPosition, fitView } = useReactFlow()

  const outputSources = useMemo(
    () => new Set((spec.outputs ?? []).map((output) => output.from)),
    [spec.outputs],
  )

  const nodes = useMemo<Node<StepNodeData>[]>(
    () =>
      spec.steps.map((step, idx) => ({
        id: step.name,
        type: 'workflowStep',
        position: positions[step.name] ?? { x: 48 + idx * 240, y: 80 },
        measured: measured[step.name],
        selected: step.name === selectedStepName,
        data: { step, isOutput: outputSources.has(step.name) },
      })),
    [spec.steps, positions, measured, selectedStepName, outputSources],
  )

  const edges = useMemo<Edge[]>(() => {
    const stepNames = new Set(spec.steps.map((s) => s.name))
    const list: Edge[] = []
    for (const step of spec.steps) {
      for (const dep of step.dependsOn ?? []) {
        if (!stepNames.has(dep)) continue
        list.push({
          id: `${dep}->${step.name}`,
          source: dep,
          target: step.name,
          animated: false,
          markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
          style: { stroke: '#6c52dc', strokeWidth: 2 },
        })
      }
    }
    return list
  }, [spec.steps])

  const validationIssues = useMemo(
    () => validateWorkflowDraft(item ? item.id : workflowId, spec, true),
    [item, workflowId, spec],
  )

  const selectedStep = useMemo(
    () => spec.steps.find((step) => step.name === selectedStepName),
    [spec.steps, selectedStepName],
  )

  useEffect(() => {
    if (selectedStepName && !spec.steps.some((s) => s.name === selectedStepName)) {
      setSelectedStepName(spec.steps[0]?.name)
    }
  }, [spec.steps, selectedStepName])

  const addNodeAt = useCallback(
    (type: StepType, point?: Point) => {
      setError('')
      setNotice('')
      const created = createDefaultStep(type, spec.steps)
      const nextSteps = [...spec.steps, created]
      const fallbackPos: Point = point ?? {
        x: 60 + (spec.steps.length % 4) * 260,
        y: 80 + Math.floor(spec.steps.length / 4) * 150,
      }
      setSpec((prev) => ({ ...prev, steps: nextSteps }))
      setPositions((prev) => ({ ...prev, [created.name]: fallbackPos }))
      setSelectedStepName(created.name)
      setInspectorTab('node')
    },
    [spec.steps],
  )

  const onDragOver = useCallback((event: DragEvent<HTMLDivElement>) => {
    event.preventDefault()
    event.dataTransfer.dropEffect = 'move'
  }, [])

  const onDrop = useCallback(
    (event: DragEvent<HTMLDivElement>) => {
      event.preventDefault()
      const rawType = (event.dataTransfer.getData(DRAG_MIME) ||
        event.dataTransfer.getData('text/plain')) as StepType
      if (!STEP_TYPES.includes(rawType)) return
      const coords = screenToFlowPosition({
        x: event.clientX,
        y: event.clientY,
      })
      addNodeAt(rawType, {
        x: Math.round(coords.x),
        y: Math.round(coords.y),
      })
    },
    [addNodeAt, screenToFlowPosition],
  )

  const onNodesChange = useCallback<OnNodesChange<Node<StepNodeData>>>(
    (changes) => {
      for (const change of changes) {
        if (change.type === 'position' && change.position) {
          const pos = change.position
          setPositions((prev) => ({ ...prev, [change.id]: pos }))
        } else if (change.type === 'dimensions' && change.dimensions) {
          const dims = change.dimensions
          setMeasured((prev) => {
            const cur = prev[change.id]
            if (cur?.width === dims.width && cur?.height === dims.height) {
              return prev
            }
            return { ...prev, [change.id]: dims }
          })
        } else if (change.type === 'select' && change.selected) {
          setSelectedStepName(change.id)
          setInspectorTab('node')
        } else if (change.type === 'remove') {
          setSpec((prev) => {
            const result = removeStep(prev, change.id, positions)
            setPositions(result.positions)
            return result.spec
          })
        }
      }
    },
    [positions],
  )

  const onEdgesChange = useCallback<OnEdgesChange<Edge>>((changes) => {
    for (const change of changes) {
      if (change.type === 'remove') {
        const [source, target] = change.id.split('->')
        if (source && target) {
          setSpec((prev) => ({
            ...prev,
            steps: disconnectDependency(prev.steps, source, target),
          }))
        }
      }
    }
  }, [])

  const onConnect = useCallback((connection: Connection) => {
    if (!connection.source || !connection.target) return
    setSpec((prev) => {
      const result = connectDependency(
        prev.steps,
        connection.source,
        connection.target,
      )
      if (result.error) setError(result.error)
      else setError('')
      return result.steps === prev.steps
        ? prev
        : { ...prev, steps: result.steps }
    })
  }, [])

  const autoLayout = () => {
    setPositions(computeStepPositions(spec.steps))
    requestAnimationFrame(() => {
      void fitView({ padding: 0.18, duration: 220 })
    })
  }

  const switchMode = (nextMode: 'visual' | 'json') => {
    if (nextMode === viewMode) return
    setError('')
    setNotice('')
    if (nextMode === 'json') {
      setJsonDraft(JSON.stringify(normalizeWorkflowSpec(spec), null, 2))
      setViewMode('json')
      return
    }
    try {
      const parsed = parseWorkflowSpecJSON(jsonDraft)
      setSpec(parsed)
      setPositions((prev) => computeStepPositions(parsed.steps, prev))
      setViewMode('visual')
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  const updateSelectedStep = (updater: (current: WorkflowStep) => WorkflowStep) => {
    if (!selectedStepName) return
    setError('')
    setNotice('')
    setSpec((prev) => ({
      ...prev,
      steps: prev.steps.map((step) =>
        step.name === selectedStepName ? updater(step) : step,
      ),
    }))
  }

  const handleSave = async () => {
    setError('')
    setNotice('')
    let candidateSpec = spec
    if (viewMode === 'json') {
      try {
        candidateSpec = parseWorkflowSpecJSON(jsonDraft)
        setSpec(candidateSpec)
        setPositions((prev) => computeStepPositions(candidateSpec.steps, prev))
      } catch (err) {
        setError(errorMessage(err))
        return
      }
    }

    const effectiveId = (item ? item.id : workflowId).trim()
    const normalized = normalizeWorkflowSpec(candidateSpec)
    const issues = validateWorkflowDraft(effectiveId, normalized, true)
    if (issues.length > 0) {
      setError(issues[0])
      return
    }

    setSaving(true)
    try {
      if (item) {
        await request('/workflows/' + idPath(item.id), {
          method: 'PATCH',
          body: {
            resourceVersion: item.resourceVersion,
            spec: buildPatchSpec(item.spec, normalized),
          },
        })
      } else {
        await request('/workflows', {
          method: 'POST',
          body: { id: effectiveId, spec: normalized },
        })
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="stack">
      <div className="wf-toolbar panel">
        <div className="inline-actions">
          <button type="button" className="button secondary" onClick={onCancel}>
            <ArrowLeft size={16} /> Back
          </button>
          {item ? (
            <div className="wf-id-readonly">
              <GitFork size={16} />
              <strong>{item.id}</strong>
            </div>
          ) : (
            <input
              aria-label="Workflow ID"
              className="wf-id-input"
              placeholder="workflow-id (e.g. horror-shorts)"
              value={workflowId}
              maxLength={253}
              onChange={(event) => {
                setWorkflowId(event.target.value)
                setError('')
              }}
            />
          )}
        </div>

        <div className="inline-actions">
          <div className="wf-segmented" role="tablist" aria-label="Editor mode">
            <button
              type="button"
              role="tab"
              aria-selected={viewMode === 'visual'}
              className={viewMode === 'visual' ? 'is-active' : ''}
              onClick={() => switchMode('visual')}
            >
              <GitFork size={15} /> Visual
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={viewMode === 'json'}
              className={viewMode === 'json' ? 'is-active' : ''}
              onClick={() => switchMode('json')}
            >
              <Code2 size={15} /> JSON
            </button>
          </div>

          {viewMode === 'visual' && (
            <button
              type="button"
              className="button secondary"
              onClick={autoLayout}
            >
              <LayoutGrid size={15} /> Auto-layout
            </button>
          )}

          <button
            type="button"
            className="button secondary"
            onClick={() => {
              if (validationIssues.length > 0) {
                setError(validationIssues.join(' '))
                setNotice('')
              } else {
                setError('')
                setNotice('Workflow DAG and step configurations are valid.')
              }
            }}
          >
            <CheckCircle2 size={15} />
            Validate
            {validationIssues.length > 0
              ? ` (${validationIssues.length})`
              : ''}
          </button>

          <button
            type="button"
            className="button primary"
            disabled={saving}
            onClick={() => void handleSave()}
          >
            <Save size={16} />
            {saving ? 'Saving…' : item ? 'Save changes' : 'Create workflow'}
          </button>
        </div>
      </div>

      {error && <Alert>{error}</Alert>}
      {notice && (
        <p role="status" className="notice">
          {notice}
        </p>
      )}

      {viewMode === 'json' ? (
        <section className="panel">
          <div className="panel-head">
            <h2>Workflow specification (JSON)</h2>
          </div>
          <div className="panel-body stack">
            <label>
              Spec JSON
              <textarea
                className="code-input"
                rows={22}
                spellCheck={false}
                value={jsonDraft}
                onChange={(event) => setJsonDraft(event.target.value)}
              />
            </label>
          </div>
        </section>
      ) : (
        <div className="wf-workbench">
          <aside className="panel wf-palette" aria-label="Node palette">
            <div className="panel-head">
              <h2>Nodes</h2>
            </div>
            <div className="wf-palette-list">
              <p className="secondary-line">
                Drag a node onto the canvas or click + to add a step.
              </p>
              {STEP_TYPES.map((type) => {
                const meta = stepMeta[type]
                const Icon = meta.icon
                return (
                  <div
                    key={type}
                    draggable
                    onDragStart={(event) => {
                      event.dataTransfer.setData(DRAG_MIME, type)
                      event.dataTransfer.setData('text/plain', type)
                      event.dataTransfer.effectAllowed = 'move'
                    }}
                    className="wf-palette-item"
                  >
                    <span className={`wf-palette-icon tone-${meta.tone}`}>
                      <Icon size={16} />
                    </span>
                    <span className="wf-palette-copy">
                      <strong>{meta.label}</strong>
                      <small>{meta.hint}</small>
                    </span>
                    <button
                      type="button"
                      className="icon-button"
                      aria-label={`Add ${meta.label} node`}
                      onClick={() => addNodeAt(type)}
                    >
                      <Plus size={16} />
                    </button>
                  </div>
                )
              })}
            </div>
          </aside>

          <section className="panel wf-canvas-panel" aria-label="Workflow DAG canvas">
            <div
              className="wf-canvas"
              onDragOver={onDragOver}
              onDrop={onDrop}
            >
              <ReactFlow
                nodes={nodes}
                edges={edges}
                nodeTypes={nodeTypes}
                onNodesChange={onNodesChange}
                onEdgesChange={onEdgesChange}
                onConnect={onConnect}
                onNodeClick={(_, node) => {
                  setSelectedStepName(node.id)
                  setInspectorTab('node')
                }}
                onEdgeClick={(_, edge) => {
                  setSpec((prev) => ({
                    ...prev,
                    steps: disconnectDependency(
                      prev.steps,
                      edge.source,
                      edge.target,
                    ),
                  }))
                  setNotice(
                    `Removed dependency "${edge.source}" → "${edge.target}".`,
                  )
                }}
                fitView
                fitViewOptions={{ padding: 0.18 }}
                deleteKeyCode={['Backspace', 'Delete']}
              >
                <Background gap={20} size={1} color="#dfe3f0" />
                <Controls showInteractive={false} />
              </ReactFlow>
            </div>
            <div className="wf-canvas-hint">
              <span>
                Drag from a right handle to a left handle to connect step
                dependencies. Click an edge to disconnect.
              </span>
              <span
                className={`badge ${validationIssues.length === 0 ? 'badge-succeeded' : 'badge-failed'}`}
              >
                {validationIssues.length === 0
                  ? `${spec.steps.length} step(s) · Valid DAG`
                  : `${validationIssues.length} validation issue(s)`}
              </span>
            </div>
          </section>

          <aside className="panel wf-inspector" aria-label="Configuration inspector">
            <div className="wf-inspector-tabs" role="tablist">
              <button
                type="button"
                role="tab"
                aria-selected={inspectorTab === 'node'}
                className={inspectorTab === 'node' ? 'is-active' : ''}
                onClick={() => setInspectorTab('node')}
              >
                Node config
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={inspectorTab === 'workflow'}
                className={inspectorTab === 'workflow' ? 'is-active' : ''}
                onClick={() => setInspectorTab('workflow')}
              >
                Workflow I/O
              </button>
            </div>

            <div className="wf-inspector-body">
              {inspectorTab === 'node' ? (
                selectedStep ? (
                  <NodeInspector
                    key={selectedStep.name}
                    step={selectedStep}
                    allSteps={spec.steps}
                    outputs={spec.outputs ?? []}
                    providers={providersData.value ?? []}
                    onRename={(nextName) => {
                      const res = renameStep(
                        spec,
                        selectedStep.name,
                        nextName,
                        positions,
                      )
                      if (res.error) {
                        setError(res.error)
                        return
                      }
                      setError('')
                      setSpec(res.spec)
                      setPositions(res.positions)
                      setSelectedStepName(nextName.trim())
                    }}
                    onChange={updateSelectedStep}
                    onToggleDependency={(depName, checked) => {
                      if (checked) {
                        const res = connectDependency(
                          spec.steps,
                          depName,
                          selectedStep.name,
                        )
                        if (res.error) setError(res.error)
                        else {
                          setError('')
                          setSpec((prev) => ({ ...prev, steps: res.steps }))
                        }
                      } else {
                        setSpec((prev) => ({
                          ...prev,
                          steps: disconnectDependency(
                            prev.steps,
                            depName,
                            selectedStep.name,
                          ),
                        }))
                      }
                    }}
                    onToggleOutput={(enabled) => {
                      setSpec((prev) => {
                        const current = prev.outputs ?? []
                        if (!enabled) {
                          return {
                            ...prev,
                            outputs: current.filter(
                              (out) => out.from !== selectedStep.name,
                            ),
                          }
                        }
                        const taken = new Set(current.map((o) => o.name))
                        const candidateName = taken.has(selectedStep.name)
                          ? `${selectedStep.name}-out`
                          : selectedStep.name
                        return {
                          ...prev,
                          outputs: [
                            ...current,
                            { name: candidateName, from: selectedStep.name },
                          ],
                        }
                      })
                    }}
                    onDelete={() => {
                      const res = removeStep(spec, selectedStep.name, positions)
                      setSpec(res.spec)
                      setPositions(res.positions)
                      setSelectedStepName(res.spec.steps[0]?.name)
                    }}
                  />
                ) : (
                  <p className="secondary-line">
                    Select a node on the canvas or add one from the left palette
                    to configure its parameters.
                  </p>
                )
              ) : (
                <WorkflowIOInspector
                  spec={spec}
                  onChange={(next) => {
                    setError('')
                    setSpec(next)
                  }}
                />
              )}
            </div>
          </aside>
        </div>
      )}
    </div>
  )
}

function NodeInspector({
  step,
  allSteps,
  outputs,
  providers,
  onRename,
  onChange,
  onToggleDependency,
  onToggleOutput,
  onDelete,
}: {
  step: WorkflowStep
  allSteps: WorkflowStep[]
  outputs: WorkflowOutput[]
  providers: Provider[]
  onRename: (nextName: string) => void
  onChange: (updater: (current: WorkflowStep) => WorkflowStep) => void
  onToggleDependency: (depName: string, checked: boolean) => void
  onToggleOutput: (enabled: boolean) => void
  onDelete: () => void
}) {
  const [nameDraft, setNameDraft] = useState(step.name)

  const requiredCap = capabilityByStep[step.type]
  const providerOptions = useMemo(() => {
    const ids = new Set<string>(['mock'])
    for (const provider of providers) {
      if (!requiredCap || provider.capabilities.includes(requiredCap)) {
        ids.add(provider.id)
      }
    }
    if (step.providerRef) ids.add(step.providerRef)
    return Array.from(ids)
  }, [providers, requiredCap, step.providerRef])

  const selectedProvider = providers.find((p) => p.id === step.providerRef)
  const isPublishedOutput = outputs.some((out) => out.from === step.name)
  const otherSteps = allSteps.filter((candidate) => candidate.name !== step.name)

  return (
    <div className="stack">
      <div className="wf-inspector-row">
        <label style={{ flex: 1 }}>
          Step name
          <input
            value={nameDraft}
            maxLength={63}
            onChange={(event) => setNameDraft(event.target.value)}
            onBlur={() => {
              if (nameDraft.trim() && nameDraft.trim() !== step.name) {
                onRename(nameDraft)
              } else {
                setNameDraft(step.name)
              }
            }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault()
                if (nameDraft.trim() && nameDraft.trim() !== step.name) {
                  onRename(nameDraft)
                }
              }
            }}
          />
        </label>
        <button
          type="button"
          className="button danger wf-delete-step"
          aria-label={`Delete step ${step.name}`}
          onClick={onDelete}
        >
          <Trash2 size={15} />
        </button>
      </div>

      <label>
        Step type
        <select
          value={step.type}
          onChange={(event) =>
            onChange((current) =>
              changeStepType(current, event.target.value as StepType),
            )
          }
        >
          {STEP_TYPES.map((type) => (
            <option key={type} value={type}>
              {stepMeta[type].label} ({type})
            </option>
          ))}
        </select>
      </label>

      {isProviderStepType(step.type) && (
        <>
          <label>
            Provider
            <select
              value={step.providerRef ?? 'mock'}
              onChange={(event) =>
                onChange((current) => ({
                  ...current,
                  providerRef: event.target.value || undefined,
                  ...(event.target.value ? {} : { model: undefined }),
                }))
              }
            >
              {providerOptions.map((id) => (
                <option key={id} value={id}>
                  {id}
                </option>
              ))}
            </select>
          </label>

          <label>
            Model <small>(optional)</small>
            <input
              list={`models-${step.name}`}
              placeholder={
                step.providerRef === 'mock'
                  ? 'Built-in mock generator'
                  : 'e.g. fal-ai/flux/dev'
              }
              value={step.model ?? ''}
              disabled={!step.providerRef}
              maxLength={253}
              onChange={(event) =>
                onChange((current) => ({
                  ...current,
                  model: event.target.value || undefined,
                }))
              }
            />
            {selectedProvider?.models?.length ? (
              <datalist id={`models-${step.name}`}>
                {selectedProvider.models.map((modelId) => (
                  <option key={modelId} value={modelId} />
                ))}
              </datalist>
            ) : null}
          </label>
        </>
      )}

      {step.type === 'ffmpeg' && (
        <FFmpegInspectorFields
          ffmpeg={
            step.ffmpeg ?? { operation: 'render_mp4', aspectRatio: '16:9' }
          }
          onUpdate={(nextFFmpeg) =>
            onChange((current) => ({ ...current, ffmpeg: nextFFmpeg }))
          }
        />
      )}

      <label className="check">
        <input
          type="checkbox"
          checked={isPublishedOutput}
          onChange={(event) => onToggleOutput(event.target.checked)}
        />
        Expose this step in workflow outputs
      </label>

      {otherSteps.length > 0 && (
        <div className="check-group">
          <span>Upstream dependencies (dependsOn)</span>
          <div className="wf-dep-list">
            {otherSteps.map((candidate) => {
              const checked =
                step.dependsOn?.includes(candidate.name) ?? false
              return (
                <label className="check" key={candidate.name}>
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={(event) =>
                      onToggleDependency(candidate.name, event.target.checked)
                    }
                  />
                  <span>
                    {candidate.name}{' '}
                    <small>({stepMeta[candidate.type].label})</small>
                  </span>
                </label>
              )
            })}
          </div>
        </div>
      )}

      <div className="form-grid">
        <label>
          Timeout (sec)
          <input
            type="number"
            min={1}
            max={86400}
            placeholder="Default"
            value={step.timeoutSeconds ?? ''}
            onChange={(event) => {
              const val = event.target.value
              onChange((current) => ({
                ...current,
                timeoutSeconds: val ? Number(val) : undefined,
              }))
            }}
          />
        </label>
        <label>
          Max attempts
          <input
            type="number"
            min={1}
            max={100}
            placeholder="1"
            value={step.retry?.maxAttempts ?? ''}
            onChange={(event) => {
              const val = event.target.value
              onChange((current) => ({
                ...current,
                retry: val ? { maxAttempts: Number(val) } : undefined,
              }))
            }}
          />
        </label>
      </div>

      <div className="form-grid">
        <label>
          CPU request
          <input
            placeholder="e.g. 100m"
            maxLength={32}
            value={step.resources?.requests?.cpu ?? ''}
            onChange={(event) =>
              onChange((current) => ({
                ...current,
                resources: {
                  ...current.resources,
                  requests: {
                    ...current.resources?.requests,
                    cpu: event.target.value,
                  },
                },
              }))
            }
          />
        </label>
        <label>
          Memory request
          <input
            placeholder="e.g. 256Mi"
            maxLength={32}
            value={step.resources?.requests?.memory ?? ''}
            onChange={(event) =>
              onChange((current) => ({
                ...current,
                resources: {
                  ...current.resources,
                  requests: {
                    ...current.resources?.requests,
                    memory: event.target.value,
                  },
                },
              }))
            }
          />
        </label>
      </div>
    </div>
  )
}

function FFmpegInspectorFields({
  ffmpeg,
  onUpdate,
}: {
  ffmpeg: NonNullable<WorkflowStep['ffmpeg']>
  onUpdate: (next: NonNullable<WorkflowStep['ffmpeg']>) => void
}) {
  const op = ffmpeg.operation
  return (
    <div className="stack">
      <label>
        FFmpeg operation
        <select
          value={op}
          onChange={(event) =>
            onUpdate({
              ...ffmpeg,
              operation: event.target.value as FFmpegOperation,
            })
          }
        >
          {FFMPEG_OPERATIONS.map((item) => (
            <option key={item} value={item}>
              {item}
            </option>
          ))}
        </select>
      </label>

      {(op === 'render_mp4' || op === 'aspect_ratio') && (
        <label>
          Aspect ratio
          <select
            value={ffmpeg.aspectRatio ?? ''}
            onChange={(event) =>
              onUpdate({
                ...ffmpeg,
                aspectRatio: (event.target.value || undefined) as
                  | AspectRatio
                  | undefined,
              })
            }
          >
            <option value="">Keep source / unset</option>
            {ASPECT_RATIOS.map((ratio) => (
              <option key={ratio} value={ratio}>
                {ratio}
              </option>
            ))}
          </select>
        </label>
      )}

      {(op === 'resize' || op === 'render_mp4') && (
        <div className="form-grid">
          <label>
            Width (px)
            <input
              type="number"
              min={16}
              max={3840}
              placeholder="e.g. 1280"
              value={ffmpeg.width ?? ''}
              onChange={(event) =>
                onUpdate({
                  ...ffmpeg,
                  width: event.target.value
                    ? Number(event.target.value)
                    : undefined,
                })
              }
            />
          </label>
          <label>
            Height (px)
            <input
              type="number"
              min={16}
              max={3840}
              placeholder="e.g. 720"
              value={ffmpeg.height ?? ''}
              onChange={(event) =>
                onUpdate({
                  ...ffmpeg,
                  height: event.target.value
                    ? Number(event.target.value)
                    : undefined,
                })
              }
            />
          </label>
        </div>
      )}

      {op === 'trim' && (
        <div className="form-grid">
          <label>
            Start (sec)
            <input
              type="number"
              min={0}
              max={86400}
              step="any"
              value={ffmpeg.startSeconds ?? ''}
              onChange={(event) =>
                onUpdate({
                  ...ffmpeg,
                  startSeconds: event.target.value
                    ? Number(event.target.value)
                    : undefined,
                })
              }
            />
          </label>
          <label>
            Duration (sec)
            <input
              type="number"
              min={0.1}
              max={86400}
              step="any"
              value={ffmpeg.durationSeconds ?? ''}
              onChange={(event) =>
                onUpdate({
                  ...ffmpeg,
                  durationSeconds: event.target.value
                    ? Number(event.target.value)
                    : undefined,
                })
              }
            />
          </label>
        </div>
      )}

      {(op === 'render_mp4' || op === 'normalize') && (
        <label>
          Frame rate (FPS)
          <input
            type="number"
            min={1}
            max={60}
            placeholder="24"
            value={ffmpeg.fps ?? ''}
            onChange={(event) =>
              onUpdate({
                ...ffmpeg,
                fps: event.target.value
                  ? Number(event.target.value)
                  : undefined,
              })
            }
          />
        </label>
      )}

      {(op === 'add_audio' || op === 'render_mp4') && (
        <label>
          Audio input {op === 'add_audio' ? '' : <small>(optional)</small>}
          <input
            placeholder="Step name or relative media path"
            maxLength={253}
            value={ffmpeg.audioInput ?? ''}
            onChange={(event) =>
              onUpdate({
                ...ffmpeg,
                audioInput: event.target.value || undefined,
              })
            }
          />
        </label>
      )}

      <label>
        Explicit media inputs <small>(comma separated, optional if wired)</small>
        <input
          placeholder="Defaults to connected upstream video steps"
          value={(ffmpeg.inputs ?? []).join(', ')}
          onChange={(event) => {
            const items = event.target.value
              .split(',')
              .map((s) => s.trim())
              .filter(Boolean)
            onUpdate({
              ...ffmpeg,
              inputs: items.length ? items : undefined,
            })
          }}
        />
      </label>
    </div>
  )
}

function WorkflowIOInspector({
  spec,
  onChange,
}: {
  spec: WorkflowSpec
  onChange: (next: WorkflowSpec) => void
}) {
  const inputs = spec.inputs ?? []
  const outputs = spec.outputs ?? []

  const updateInput = (index: number, patch: Partial<Input>) => {
    const next = inputs.map((entry, idx) =>
      idx === index ? { ...entry, ...patch } : entry,
    )
    onChange({ ...spec, inputs: next })
  }

  const updateOutput = (index: number, patch: Partial<WorkflowOutput>) => {
    const next = outputs.map((entry, idx) =>
      idx === index ? { ...entry, ...patch } : entry,
    )
    onChange({ ...spec, outputs: next })
  }

  return (
    <div className="stack">
      <label>
        Description
        <textarea
          rows={3}
          maxLength={4096}
          placeholder="Describe what this workflow produces…"
          value={spec.description ?? ''}
          onChange={(event) =>
            onChange({ ...spec, description: event.target.value })
          }
        />
      </label>

      <div className="wf-io-section">
        <div className="wf-io-head">
          <strong>Inputs ({inputs.length})</strong>
          <button
            type="button"
            className="button secondary"
            onClick={() => {
              const nextIdx = inputs.length + 1
              onChange({
                ...spec,
                inputs: [
                  ...inputs,
                  {
                    name: `param_${nextIdx}`,
                    type: 'string',
                    required: false,
                  },
                ],
              })
            }}
          >
            <Plus size={14} /> Add input
          </button>
        </div>
        {inputs.map((input, idx) => (
          <div className="wf-io-card" key={idx}>
            <div className="wf-inspector-row">
              <input
                aria-label="Input name"
                placeholder="name"
                maxLength={63}
                value={input.name}
                onChange={(event) =>
                  updateInput(idx, { name: event.target.value })
                }
              />
              <select
                aria-label="Input type"
                value={input.type}
                onChange={(event) =>
                  updateInput(idx, { type: event.target.value as InputType })
                }
              >
                {INPUT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className="icon-button"
                aria-label={`Remove input ${input.name}`}
                onClick={() =>
                  onChange({
                    ...spec,
                    inputs: inputs.filter((_, i) => i !== idx),
                  })
                }
              >
                <Trash2 size={15} />
              </button>
            </div>
            <label className="check">
              <input
                type="checkbox"
                checked={!!input.required}
                onChange={(event) =>
                  updateInput(idx, { required: event.target.checked })
                }
              />
              Required parameter
            </label>
          </div>
        ))}
      </div>

      <div className="wf-io-section">
        <div className="wf-io-head">
          <strong>Outputs ({outputs.length})</strong>
          <button
            type="button"
            className="button secondary"
            disabled={spec.steps.length === 0}
            onClick={() => {
              const fallbackStep =
                spec.steps[spec.steps.length - 1]?.name ?? ''
              onChange({
                ...spec,
                outputs: [
                  ...outputs,
                  { name: `output-${outputs.length + 1}`, from: fallbackStep },
                ],
              })
            }}
          >
            <Plus size={14} /> Add output
          </button>
        </div>
        {outputs.map((output, idx) => (
          <div className="wf-io-card" key={idx}>
            <div className="wf-inspector-row">
              <input
                aria-label="Output name"
                placeholder="output name"
                maxLength={63}
                value={output.name}
                onChange={(event) =>
                  updateOutput(idx, { name: event.target.value })
                }
              />
              <select
                aria-label="Source step"
                value={output.from}
                onChange={(event) =>
                  updateOutput(idx, { from: event.target.value })
                }
              >
                {spec.steps.map((s) => (
                  <option key={s.name} value={s.name}>
                    {s.name}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className="icon-button"
                aria-label={`Remove output ${output.name}`}
                onClick={() =>
                  onChange({
                    ...spec,
                    outputs: outputs.filter((_, i) => i !== idx),
                  })
                }
              >
                <Trash2 size={15} />
              </button>
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}
