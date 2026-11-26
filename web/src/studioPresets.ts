import type { AspectRatio, Provider, WorkflowSpec, WorkflowStep } from './types'

export type StudioMode = 'mock' | 'fal' | 'full-ai'
export type ReferenceMode = 'none' | 'upload' | 'auto-image'

export type StudioDraft = {
  prompt: string
  style: string
  referenceNotes: string
  aspectRatio: AspectRatio
  clips: 1 | 2
  mode: StudioMode
  referenceMode: ReferenceMode
  referenceImageKey: string
  geminiProviderId: string
  falProviderId: string
  llmModel: string
  imageModel: string
  videoModel: string
}

export type ProviderReadiness = {
  geminiProviders: Provider[]
  falProviders: Provider[]
  hasGemini: boolean
  hasFal: boolean
}

export const DEFAULT_GEMINI_MODEL = 'gemini-2.5-flash'
export const DEFAULT_FAL_IMAGE_MODEL = 'fal-ai/flux/schnell'
export const DEFAULT_FAL_VIDEO_MODEL = 'fal-ai/kling-video/v1.6/standard/image-to-video'
export const DEFAULT_FAL_T2V_MODEL = 'fal-ai/kling-video/v1.6/standard/text-to-video'

export const STARTER_PROMPTS = [
  {
    label: 'Cyberpunk Tokyo',
    prompt: 'Rain-soaked cyberpunk alley in Tokyo at midnight, glowing neon reflections on asphalt, slow cinematic dolly forward',
    style: 'Cinematic',
  },
  {
    label: 'Horror Shorts',
    prompt: 'Unknown glowing creatures moving silently between ancient misty pine trees at night, handheld camera',
    style: 'Found Footage',
  },
  {
    label: 'Luxury Product',
    prompt: 'Minimalist mechanical watch rotating slowly on dark volcanic stone, dramatic rim lighting, macro studio shot',
    style: 'Commercial',
  },
  {
    label: 'Cozy Anime',
    prompt: 'Warm sunlit Japanese coffee shop window during soft afternoon rain, steam rising from a ceramic latte cup',
    style: 'Anime',
  },
]

export const STYLE_PRESETS = [
  'Cinematic',
  'Found Footage',
  'Anime',
  'Commercial',
  'Documentary',
  '3D Render',
]

export const ASPECT_OPTIONS: { value: AspectRatio; label: string; hint: string }[] = [
  { value: '9:16', label: '9:16', hint: 'Shorts / TikTok' },
  { value: '16:9', label: '16:9', hint: 'YouTube / Wide' },
  { value: '1:1', label: '1:1', hint: 'Square' },
  { value: '4:5', label: '4:5', hint: 'Social Feed' },
]

export function detectProviderReadiness(providers: Provider[]): ProviderReadiness {
  const geminiProviders = providers.filter((p) => p.type === 'gemini' && p.enabled)
  const falProviders = providers.filter((p) => p.type === 'fal' && p.enabled)
  return {
    geminiProviders,
    falProviders,
    hasGemini: geminiProviders.length > 0,
    hasFal: falProviders.length > 0,
  }
}

export function studioWorkflowId(draft: StudioDraft): string {
  const ratioSlug = draft.aspectRatio.replace(':', 'x')
  const refSlug =
    draft.referenceMode === 'auto-image'
      ? 'refimg'
      : draft.referenceMode === 'upload'
        ? 'i2v'
        : 't2v'
  return `studio-${draft.mode}-${refSlug}-${draft.clips}c-${ratioSlug}`
}

export function buildStudioWorkflowSpec(draft: StudioDraft): WorkflowSpec {
  const isMock = draft.mode === 'mock'
  const useGemini = draft.mode === 'full-ai'
  const videoProvider = isMock ? 'mock' : draft.falProviderId || 'fal'
  const imageProvider = isMock ? 'mock' : draft.falProviderId || 'fal'
  const llmProvider = useGemini ? draft.geminiProviderId || 'gemini' : 'mock'

  const steps: WorkflowStep[] = []
  let upstreamForMedia: string | undefined

  if (isMock || useGemini) {
    const scriptStep: WorkflowStep = {
      name: 'script',
      type: 'llm',
      providerRef: llmProvider,
      timeoutSeconds: 120,
    }
    if (useGemini && draft.llmModel.trim()) {
      scriptStep.model = draft.llmModel.trim()
    }
    steps.push(scriptStep)
    upstreamForMedia = 'script'
  }

  const videoNames: string[] = []
  const sceneIndices = draft.clips === 2 ? [1, 2] : [1]

  for (const idx of sceneIndices) {
    let videoDep = upstreamForMedia

    if (draft.referenceMode === 'auto-image') {
      const imageName = draft.clips === 2 ? `scene-${idx}-image` : 'reference-frame'
      const imgStep: WorkflowStep = {
        name: imageName,
        type: 'image',
        providerRef: imageProvider,
        timeoutSeconds: 180,
      }
      if (upstreamForMedia) imgStep.dependsOn = [upstreamForMedia]
      if (!isMock && draft.imageModel.trim()) imgStep.model = draft.imageModel.trim()
      steps.push(imgStep)
      videoDep = imageName
    }

    const videoName = draft.clips === 2 ? `scene-${idx}-video` : 'generate-video'
    const videoStep: WorkflowStep = {
      name: videoName,
      type: 'video',
      providerRef: videoProvider,
      timeoutSeconds: 600,
    }
    if (videoDep) videoStep.dependsOn = [videoDep]
    if (!isMock && draft.videoModel.trim()) videoStep.model = draft.videoModel.trim()
    steps.push(videoStep)
    videoNames.push(videoName)
  }

  steps.push({
    name: 'render',
    type: 'ffmpeg',
    dependsOn: videoNames,
    timeoutSeconds: 240,
    ffmpeg: {
      operation: 'render_mp4',
      aspectRatio: draft.aspectRatio,
      fps: 24,
    },
  })

  return {
    description: `1-Click Studio preset (${draft.mode}, ${draft.aspectRatio}, ${draft.clips} clip${draft.clips > 1 ? 's' : ''})`,
    inputs: [
      { name: 'topic', type: 'string', required: true, description: 'Main video idea or prompt' },
      { name: 'style', type: 'string', description: 'Visual style preset' },
      { name: 'aspect_ratio', type: 'string', description: 'Output aspect ratio' },
      { name: 'reference_notes', type: 'string', description: 'Optional camera, lighting or character notes' },
      { name: 'reference_image', type: 'string', description: 'Optional uploaded reference image key' },
    ],
    steps,
    outputs: [{ name: 'final-video', from: 'render' }],
  }
}

export function buildStudioRunBody(draft: StudioDraft): string {
  const topic = draft.prompt.trim()
  if (!topic) {
    throw new Error('Enter a prompt or idea for your video.')
  }
  if (draft.mode !== 'mock' && draft.referenceMode === 'upload' && !draft.referenceImageKey) {
    throw new Error('Upload a reference image first or switch reference mode.')
  }

  const inputs: Record<string, { stringValue: string }> = {
    topic: { stringValue: topic },
    aspect_ratio: { stringValue: draft.aspectRatio },
  }
  if (draft.style.trim()) {
    inputs.style = { stringValue: draft.style.trim() }
  }
  if (draft.referenceNotes.trim()) {
    inputs.reference_notes = { stringValue: draft.referenceNotes.trim() }
  }
  if (draft.referenceMode === 'upload' && draft.referenceImageKey.trim()) {
    inputs.reference_image = { stringValue: draft.referenceImageKey.trim() }
  }

  return JSON.stringify({
    inputs,
    allowPaid: draft.mode !== 'mock',
  })
}
