import { useEffect, useState } from 'react'

export const apiRoot = '/api/v1'
let csrfToken = ''

export function setSessionToken(token: string) {
  csrfToken = token
}

export async function request<T>(
  path: string,
  options: {
    method?: string
    body?: object | string
    signal?: AbortSignal
  } = {},
): Promise<T> {
  const method = options.method ?? 'GET'
  const headers = new Headers({ Accept: 'application/json' })
  if (method !== 'GET') headers.set('X-CSRF-Token', csrfToken)
  if (options.body !== undefined)
    headers.set('Content-Type', 'application/json')
  const response = await fetch(apiRoot + path, {
    method,
    headers,
    signal: options.signal,
    credentials: 'same-origin',
    body:
      typeof options.body === 'string'
        ? options.body
        : JSON.stringify(options.body),
  })
  if (response.status === 401) {
    location.assign(
      '/login?next=' + encodeURIComponent(location.pathname + location.hash),
    )
    throw new Error('Your session expired.')
  }
  if (!response.ok) {
    const data = (await response.json().catch(() => null)) as {
      message?: string
    } | null
    throw new Error(
      data?.message ?? `Request failed (HTTP ${response.status}).`,
    )
  }
  return response.status === 204 ? (undefined as T) : response.json()
}

export async function list<T>(
  path: string,
  signal?: AbortSignal,
): Promise<T[]> {
  const items: T[] = []
  const separator = path.includes('?') ? '&' : '?'
  const seen = new Set<string>()
  let cursor = ''
  do {
    const page: { items: T[]; continue?: string } = await request(
      path +
        separator +
        'limit=100' +
        (cursor ? '&continue=' + encodeURIComponent(cursor) : ''),
      { signal },
    )
    items.push(...page.items)
    cursor = page.continue ?? ''
    if (cursor && seen.has(cursor))
      throw new Error('The server repeated a pagination cursor.')
    seen.add(cursor)
  } while (cursor)
  return items
}

export function useResource<T>(path: string | null, paged = false, period = 0) {
  const [value, setValue] = useState<T>()
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    setValue(undefined)
    setError('')
  }, [path])
  useEffect(() => {
    if (path === null) return
    const controller = new AbortController()
    let timer: number | undefined
    let active = true
    const load = async () => {
      try {
        const result = paged
          ? ((await list(path, controller.signal)) as T)
          : await request<T>(path, { signal: controller.signal })
        if (active) {
          setValue(result)
          setError('')
        }
      } catch (err) {
        if (active) setError(errorMessage(err))
      } finally {
        if (active && period) timer = window.setTimeout(load, period)
      }
    }
    void load()
    return () => {
      active = false
      controller.abort()
      window.clearTimeout(timer)
    }
  }, [path, paged, period, revision])
  return { value, error, refresh: () => setRevision((n) => n + 1) }
}

export async function uploadFile<T>(path: string, file: File): Promise<T> {
  const form = new FormData()
  form.append('file', file)
  const headers = new Headers({ Accept: 'application/json' })
  headers.set('X-CSRF-Token', csrfToken)
  const response = await fetch(apiRoot + path, {
    method: 'POST',
    headers,
    credentials: 'same-origin',
    body: form,
  })
  if (response.status === 401) {
    location.assign(
      '/login?next=' + encodeURIComponent(location.pathname + location.hash),
    )
    throw new Error('Your session expired.')
  }
  if (!response.ok) {
    const data = (await response.json().catch(() => null)) as {
      message?: string
    } | null
    throw new Error(
      data?.message ?? `Upload failed (HTTP ${response.status}).`,
    )
  }
  return response.json()
}

export const idPath = encodeURIComponent
export const errorMessage = (err: unknown) =>
  err instanceof Error ? err.message : 'The request failed.'
