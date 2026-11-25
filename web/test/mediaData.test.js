import assert from 'node:assert/strict'
import test from 'node:test'
import {
  categoryLabel,
  classifyArtifact,
  filterArtifacts,
  summarizeMediaCounts,
} from '../src/mediaData.ts'

const sampleArtifacts = [
  {
    id: 'run-1.aaa',
    runId: 'horror-run-1',
    name: 'script.json',
    contentType: 'application/json',
    sizeBytes: 512,
    modifiedAt: '2026-11-26T01:00:00Z',
  },
  {
    id: 'run-1.bbb',
    runId: 'horror-run-1',
    name: 'scene.svg',
    contentType: 'image/svg+xml',
    sizeBytes: 1024,
    modifiedAt: '2026-11-26T01:01:00Z',
  },
  {
    id: 'run-1.ccc',
    runId: 'horror-run-1',
    name: 'image.jpg',
    contentType: 'image/jpeg',
    sizeBytes: 24576,
    modifiedAt: '2026-11-26T01:02:00Z',
  },
  {
    id: 'run-1.ddd',
    runId: 'horror-run-1',
    name: 'clip.mp4',
    contentType: 'video/mp4',
    sizeBytes: 120000,
    modifiedAt: '2026-11-26T01:03:00Z',
  },
  {
    id: 'run-2.eee',
    runId: 'teaser-run-2',
    name: 'output.mp4',
    contentType: 'video/mp4',
    sizeBytes: 480000,
    modifiedAt: '2026-11-26T01:05:00Z',
  },
]

test('classifyArtifact separates final videos, generated clips, images, and scripts', () => {
  assert.equal(classifyArtifact(sampleArtifacts[0]), 'script')
  assert.equal(classifyArtifact(sampleArtifacts[1]), 'image')
  assert.equal(classifyArtifact(sampleArtifacts[2]), 'image')
  assert.equal(classifyArtifact(sampleArtifacts[3]), 'clip')
  assert.equal(classifyArtifact(sampleArtifacts[4]), 'final_video')
  assert.equal(categoryLabel('final_video'), 'Final video')
})

test('summarizeMediaCounts and filterArtifacts order newest-first and filter by category and query', () => {
  assert.deepEqual(summarizeMediaCounts(sampleArtifacts), {
    all: 5,
    final_video: 1,
    clip: 1,
    image: 2,
    script: 1,
  })

  const allSorted = filterArtifacts(sampleArtifacts, 'all', '')
  assert.equal(allSorted[0].name, 'output.mp4')
  assert.equal(allSorted[4].name, 'script.json')

  const images = filterArtifacts(sampleArtifacts, 'image', '')
  assert.deepEqual(
    images.map((item) => item.name),
    ['image.jpg', 'scene.svg'],
  )

  const clips = filterArtifacts(sampleArtifacts, 'clip', '')
  assert.deepEqual(
    clips.map((item) => item.name),
    ['clip.mp4'],
  )

  const finals = filterArtifacts(sampleArtifacts, 'final_video', '')
  assert.deepEqual(
    finals.map((item) => item.name),
    ['output.mp4'],
  )

  const byRun = filterArtifacts(sampleArtifacts, 'all', 'teaser-run-2')
  assert.deepEqual(
    byRun.map((item) => item.id),
    ['run-2.eee'],
  )
})
