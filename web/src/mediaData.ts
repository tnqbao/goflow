import type { Artifact } from './types.ts'

export type MediaCategory = 'final_video' | 'clip' | 'image' | 'script'

export type MediaFilter = 'all' | MediaCategory

export type MediaSummaryCounts = {
  all: number
  final_video: number
  clip: number
  image: number
  script: number
}

export function classifyArtifact(artifact: Pick<Artifact, 'name' | 'contentType'>): MediaCategory {
  const type = artifact.contentType.toLowerCase()
  const name = artifact.name.toLowerCase()

  if (type.startsWith('video/')) {
    return name === 'output.mp4' ? 'final_video' : 'clip'
  }
  if (type.startsWith('image/')) {
    return 'image'
  }
  return 'script'
}

export function categoryLabel(category: MediaCategory): string {
  switch (category) {
    case 'final_video':
      return 'Final video'
    case 'clip':
      return 'Generated clip'
    case 'image':
      return 'Generated image'
    case 'script':
      return 'Script / JSON'
  }
}

export function sortArtifactsNewestFirst(artifacts: Artifact[]): Artifact[] {
  return [...artifacts].sort((a, b) => {
    const byTime = b.modifiedAt.localeCompare(a.modifiedAt)
    return byTime !== 0 ? byTime : a.id.localeCompare(b.id)
  })
}

export function summarizeMediaCounts(artifacts: Artifact[]): MediaSummaryCounts {
  const counts: MediaSummaryCounts = {
    all: artifacts.length,
    final_video: 0,
    clip: 0,
    image: 0,
    script: 0,
  }
  for (const item of artifacts) {
    counts[classifyArtifact(item)]++
  }
  return counts
}

export function filterArtifacts(
  artifacts: Artifact[],
  filter: MediaFilter,
  query: string,
): Artifact[] {
  const needle = query.trim().toLowerCase()
  return sortArtifactsNewestFirst(artifacts).filter((item) => {
    if (filter !== 'all' && classifyArtifact(item) !== filter) {
      return false
    }
    if (!needle) return true
    return (
      item.name.toLowerCase().includes(needle) ||
      item.runId.toLowerCase().includes(needle) ||
      item.contentType.toLowerCase().includes(needle)
    )
  })
}
