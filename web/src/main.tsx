import { useEffect, useState } from 'react'
import { createRoot } from 'react-dom/client'
import {
  Activity,
  Boxes,
  CircleCheck,
  CircleX,
  Clapperboard,
  Film,
  Images,
  LayoutDashboard,
  LogOut,
  Play,
  Settings as SettingsIcon,
  Sparkles,
  Workflow as WorkflowIcon,
} from 'lucide-react'
import { errorMessage, request, setSessionToken, useResource } from './api'
import { overviewCounts } from './data'
import { MediaLibrary } from './media'
import { Providers } from './providers'
import { Executions, orderedRuns, RunTable } from './runs'
import { Settings } from './settings'
import { Studio } from './studio'
import type { Artifact, Run, User, Workflow } from './types'
import { Alert, Loading } from './ui'
import { Workflows } from './workflows'
import './style.css'

const pages = {
  studio: {
    label: 'Create Video',
    icon: Sparkles,
    help: 'Connect an API key, describe your idea, attach references, and generate MP4 video in one click.',
  },
  media: {
    label: 'Media Library',
    icon: Images,
    help: 'Preview, inspect, download and delete generated images, clips and final videos.',
  },
  executions: {
    label: 'Executions',
    icon: Activity,
    help: 'Inspect run status, steps, logs and outputs.',
  },
  providers: {
    label: 'API Keys',
    icon: Boxes,
    help: 'Manage Gemini, fal.ai and Mock provider keys, models and limits.',
  },
  workflows: {
    label: 'Workflows',
    icon: WorkflowIcon,
    help: 'Advanced visual DAG pipeline editor and reusable JSON definitions.',
  },
  overview: {
    label: 'Overview',
    icon: LayoutDashboard,
    help: 'Track cluster metrics, active runs and published outputs.',
  },
  settings: {
    label: 'Settings',
    icon: SettingsIcon,
    help: 'Manage your account and API tokens.',
  },
}
type Page = keyof typeof pages

function readRoute() {
  const [target, selected] = location.hash.replace(/^#\/?/, '').split('/')
  const page: Page = Object.hasOwn(pages, target)
    ? (target as Page)
    : 'studio'
  let id: string | undefined
  try {
    id =
      (page === 'executions' || page === 'media') && selected
        ? decodeURIComponent(selected)
        : undefined
  } catch {
    id = undefined
  }
  return { page, id }
}
function App() {
  const session = useResource<User>('/auth/me')
  const [route, setRoute] = useState(readRoute)
  const [error, setError] = useState('')
  useEffect(() => {
    const navigate = () => {
      setRoute(readRoute())
      window.scrollTo(0, 0)
    }
    window.addEventListener('hashchange', navigate)
    return () => window.removeEventListener('hashchange', navigate)
  }, [])
  useEffect(() => {
    if (session.value) setSessionToken(session.value.csrfToken)
  }, [session.value])
  const page = pages[route.page]
  return (
    <div className="shell">
      <aside className="sidebar">
        <a className="brand" href="#/studio">
          <span className="brand-mark">
            <Clapperboard size={21} />
          </span>
          KubeReel
        </a>
        <nav aria-label="Dashboard">
          {Object.entries(pages).map(([key, { label, icon: Icon }]) => (
            <a
              key={key}
              href={`#/${key}`}
              aria-current={route.page === key ? 'page' : undefined}
            >
              <Icon size={18} />
              {label}
            </a>
          ))}
        </nav>
        {session.value && (
          <div className="sidebar-account">
            <span className="avatar">
              {session.value.username.slice(0, 1).toUpperCase()}
            </span>
            <span>
              <strong>{session.value.username}</strong>
              <small>Administrator</small>
            </span>
            <button
              className="icon-button"
              type="button"
              aria-label="Sign out"
              onClick={async () => {
                try {
                  await request('/auth/logout', { method: 'POST' })
                  location.assign('/login')
                } catch (err) {
                  setError(errorMessage(err))
                }
              }}
            >
              <LogOut size={17} />
            </button>
          </div>
        )}
      </aside>
      <main className="workspace">
        <header className="topbar">
          <span>Workspace</span>
          <span className="slash">/</span>
          <strong>{page.label}</strong>
        </header>
        <div className="content">
          <div className="page-heading">
            <div>
              <h1>
                {route.page === 'executions' && route.id
                  ? 'Execution details'
                  : page.label}
              </h1>
              <p>{page.help}</p>
            </div>
            {route.page === 'overview' && session.value && (
              <a className="button primary" href="#/studio">
                <Play size={16} /> Create video
              </a>
            )}
          </div>
          {(error || session.error) && <Alert>{error || session.error}</Alert>}
          {!session.value ? (
            !session.error && <Loading />
          ) : (
            <>
              {route.page === 'studio' && <Studio />}
              {route.page === 'overview' && <Overview />}
              {route.page === 'workflows' && <Workflows />}
              {route.page === 'executions' && <Executions id={route.id} />}
              {route.page === 'providers' && <Providers />}
              {route.page === 'media' && (
                <MediaLibrary key={route.id ?? ''} initialQuery={route.id ?? ''} />
              )}
              {route.page === 'settings' && <Settings user={session.value} />}
            </>
          )}
        </div>
      </main>
    </div>
  )
}

const tiles = [
  { title: 'Workflows', icon: WorkflowIcon, tone: 'violet' },
  { title: 'Active runs', icon: Activity, tone: 'blue' },
  { title: 'Succeeded', icon: CircleCheck, tone: 'green' },
  { title: 'Failed', icon: CircleX, tone: 'rose' },
  { title: 'Published videos', icon: Film, tone: 'amber' },
]

function Overview() {
  const workflows = useResource<Workflow[]>('/workflows', true)
  const runs = useResource<Run[]>('/runs', true, 8000)
  const artifacts = useResource<Artifact[]>('/artifacts', true, 16000)
  const counts = overviewCounts(
    workflows.value?.length ?? 0,
    runs.value ?? [],
    artifacts.value ?? [],
  )
  const ready = [
    workflows.value,
    runs.value,
    runs.value,
    runs.value,
    artifacts.value,
  ]
  const error = workflows.error || runs.error || artifacts.error
  return (
    <div className="stack">
      {error && <Alert>{error}</Alert>}
      <div className="metric-grid">
        {tiles.map(({ title, icon: Icon, tone }, index) => (
          <div className="metric panel" key={title}>
            <span className={`metric-icon tone-${tone}`}>
              <Icon size={20} />
            </span>
            <span>{title}</span>
            <strong>
              {ready[index] ? counts[index].toLocaleString() : '—'}
            </strong>
          </div>
        ))}
      </div>
      <section className="panel">
        <div className="panel-head">
          <h2>Recent activity</h2>
          <Activity size={18} />
        </div>
        {runs.value ? (
          <RunTable runs={orderedRuns(runs.value).slice(0, 8)} activeLink />
        ) : (
          !runs.error && <Loading />
        )}
      </section>
    </div>
  )
}

createRoot(document.getElementById('root')!).render(<App />)
