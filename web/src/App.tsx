import { FormEvent, useCallback, useEffect, useState } from 'react'

type Monitor = {
  id: number; name: string; url: string; intervalSeconds: number; timeoutSeconds: number; active: boolean; public: boolean
  lastCheckedAt: string | null; lastStatusCode: number | null; lastResponseMs: number | null; lastError: string | null
  certificateExpiresAt: string | null; uptime24h: number; incidentStartedAt: string | null
  failureThreshold: number; recoveryThreshold: number; consecutiveFailures: number; consecutiveSuccesses: number; maintenanceUntil: string | null
  monitorType: 'http' | 'heartbeat' | 'tcp' | 'dns'; expectedKeyword: string
}
type MonitorForm = { name: string; monitorType: 'http' | 'heartbeat' | 'tcp' | 'dns'; url: string; expectedKeyword: string; intervalSeconds: number; timeoutSeconds: number; failureThreshold: number; recoveryThreshold: number; maintenanceUntil: string; active: boolean; public: boolean }
type Incident = { id: number; monitorName: string; startedAt: string; resolvedAt: string | null; cause: string; acknowledgedAt?: string | null; note?: string }
type ApiKey = { id: number; name: string; prefix: string; scope: 'read' | 'write' | 'admin'; lastUsedAt: string | null; createdAt: string }
type Report = { days: number; monitors: { monitorId: number; monitorName: string; uptime: number; averageResponseMs: number; checks: number }[] }
type AuditEvent = { actor: string; action: string; resource: string; status: number; createdAt: string }
const blank: MonitorForm = { name: '', monitorType: 'http', url: '', expectedKeyword: '', intervalSeconds: 60, timeoutSeconds: 10, failureThreshold: 2, recoveryThreshold: 2, maintenanceUntil: '', active: true, public: true }

function stateOf(monitor: Monitor) {
  if (!monitor.active) return 'paused'
  if (monitor.maintenanceUntil && new Date(monitor.maintenanceUntil) > new Date()) return 'maintenance'
  if (!monitor.lastCheckedAt) return 'pending'
  if (monitor.incidentStartedAt) return 'down'
  return monitor.consecutiveFailures > 0 ? 'degraded' : 'up'
}

function date(value: string | null) {
  return value ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : 'Not checked yet'
}

function PublicStatus() {
  const [monitors, setMonitors] = useState<Monitor[]>([])
  const [incidents, setIncidents] = useState<Incident[]>([])
  const [updatedAt, setUpdatedAt] = useState('')
  const [page, setPage] = useState({ name: 'PulseOps', message: 'Live service health and incident updates.' })
  const load = useCallback(async () => {
    const response = await fetch('/api/status')
    if (!response.ok) return
    const data = await response.json()
    setMonitors(data.monitors); setIncidents(data.incidents || []); setUpdatedAt(data.updatedAt); if (data.page) setPage(data.page)
  }, [])
  useEffect(() => { load(); const events = new EventSource('/api/events'); events.addEventListener('status', load); return () => events.close() }, [load])
  const operational = monitors.every((monitor) => stateOf(monitor) !== 'down')
  return <div className="shell status-page">
    <header><a className="brand" href="/">{page.name}</a><span className="live"><i /> Live</span></header>
    <main>
      <section className={`hero-status ${operational ? 'healthy' : 'outage'}`}><div className="big-dot" /><div><p className="kicker">CURRENT STATUS</p><h1>{operational ? 'All systems operational' : 'Service disruption detected'}</h1><p>{page.message} {updatedAt ? `Updated ${date(updatedAt)}` : 'Loading current status…'}</p></div></section>
      <div className="status-list">{monitors.length === 0 ? <div className="empty">No public services configured.</div> : monitors.map((monitor) => <article className="status-row" key={monitor.id}><div><h2>{monitor.name}</h2><p>{monitor.uptime24h.toFixed(2)}% uptime over 24 hours</p></div><span className={`pill ${stateOf(monitor)}`}>{stateOf(monitor)}</span></article>)}</div>
      <section className="incident-section"><div className="section-title"><div><p className="kicker">HISTORY</p><h2>Recent incidents</h2></div></div><div className="timeline">{incidents.length === 0 ? <div className="empty">No incidents reported.</div> : incidents.map((incident) => <article className="timeline-row" key={incident.id}><i className={incident.resolvedAt ? 'resolved' : 'open'} /><div><div className="timeline-title"><strong>{incident.monitorName}</strong><span className={`pill ${incident.resolvedAt ? 'up' : 'down'}`}>{incident.resolvedAt ? 'resolved' : 'investigating'}</span></div><p>{incident.cause}</p><small>{date(incident.startedAt)}{incident.resolvedAt && ` · Resolved ${date(incident.resolvedAt)}`}</small></div></article>)}</div></section>
    </main>
    <footer>{page.name} · Powered by PulseOps</footer>
  </div>
}

export function App() {
  if (location.pathname === '/status') return <PublicStatus />
  const [token, setToken] = useState(() => sessionStorage.getItem('pulseops-token') || '')
  const [draftToken, setDraftToken] = useState('')
  const [monitors, setMonitors] = useState<Monitor[]>([])
  const [incidents, setIncidents] = useState<Incident[]>([])
  const [keys, setKeys] = useState<ApiKey[]>([])
  const [report, setReport] = useState<Report | null>(null)
  const [audit, setAudit] = useState<AuditEvent[]>([])
  const [newKey, setNewKey] = useState({ name: '', scope: 'read' as 'read' | 'write' | 'admin' })
  const [createdToken, setCreatedToken] = useState('')
  const [heartbeatUrl, setHeartbeatUrl] = useState('')
  const [form, setForm] = useState<MonitorForm>(blank)
  const [editing, setEditing] = useState<number | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const request = useCallback(async (path: string, options: RequestInit = {}) => {
    const response = await fetch(path, { ...options, headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}`, ...options.headers } })
    if (response.status === 401) { sessionStorage.removeItem('pulseops-token'); setToken(''); throw new Error('Session expired') }
    if (!response.ok) { const data = await response.json().catch(() => ({})); throw new Error(data.error || 'Request failed') }
    return response.status === 204 ? null : response.json()
  }, [token])
  const load = useCallback(async () => { if (!token) return; try { const [nextMonitors, nextIncidents, nextReport] = await Promise.all([request('/api/monitors'), request('/api/incidents'), request('/api/reports/uptime?days=30')]); setMonitors(nextMonitors); setIncidents(nextIncidents); setReport(nextReport); setError(''); try { setKeys(await request('/api/keys')) } catch { setKeys([]) } try { setAudit(await request('/api/audit')) } catch { setAudit([]) } } catch (err) { setError((err as Error).message) } }, [request, token])
  useEffect(() => { load() }, [load])
  useEffect(() => { if (!token) return; const events = new EventSource('/api/events'); events.addEventListener('status', load); return () => events.close() }, [load, token])

  function login(event: FormEvent) { event.preventDefault(); const clean = draftToken.trim(); if (clean.length < 16) { setError('Token must be at least 16 characters.'); return }; sessionStorage.setItem('pulseops-token', clean); setToken(clean); setError('') }
  async function save(event: FormEvent) { event.preventDefault(); setBusy(true); setError(''); try { const payload = { ...form, maintenanceUntil: form.maintenanceUntil ? new Date(form.maintenanceUntil).toISOString() : null }; const result = await request(editing ? `/api/monitors/${editing}` : '/api/monitors', { method: editing ? 'PUT' : 'POST', body: JSON.stringify(payload) }); if (result?.heartbeatUrl) setHeartbeatUrl(`${location.origin}${result.heartbeatUrl}`); setForm(blank); setEditing(null); await load() } catch (err) { setError((err as Error).message) } finally { setBusy(false) } }
  function edit(monitor: Monitor) { setEditing(monitor.id); const maintenanceUntil = monitor.maintenanceUntil ? new Date(new Date(monitor.maintenanceUntil).getTime() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16) : ''; setForm({ name: monitor.name, monitorType: monitor.monitorType, url: monitor.url, expectedKeyword: monitor.expectedKeyword, intervalSeconds: monitor.intervalSeconds, timeoutSeconds: monitor.timeoutSeconds, failureThreshold: monitor.failureThreshold, recoveryThreshold: monitor.recoveryThreshold, maintenanceUntil, active: monitor.active, public: monitor.public }); scrollTo({ top: 0, behavior: 'smooth' }) }
  async function remove(monitor: Monitor) { if (!confirm(`Delete ${monitor.name} and its history?`)) return; try { await request(`/api/monitors/${monitor.id}`, { method: 'DELETE' }); await load() } catch (err) { setError((err as Error).message) } }
  async function annotate(incident: Incident) { const note = prompt('Incident note', incident.note || ''); if (note === null) return; try { await request(`/api/incidents/${incident.id}`, { method: 'PATCH', body: JSON.stringify({ acknowledged: true, note }) }); await load() } catch (err) { setError((err as Error).message) } }
  async function createKey(event: FormEvent) { event.preventDefault(); try { const result = await request('/api/keys', { method: 'POST', body: JSON.stringify(newKey) }); setCreatedToken(result.token); setNewKey({ name: '', scope: 'read' }); await load() } catch (err) { setError((err as Error).message) } }
  async function deleteKey(key: ApiKey) { if (!confirm(`Revoke ${key.name}?`)) return; try { await request(`/api/keys/${key.id}`, { method: 'DELETE' }); await load() } catch (err) { setError((err as Error).message) } }
  async function downloadReport() { try { const response = await fetch('/api/reports/uptime?days=30&format=csv', { headers: { Authorization: `Bearer ${token}` } }); if (!response.ok) throw new Error('Download failed'); const href=URL.createObjectURL(await response.blob()); const link=document.createElement('a'); link.href=href; link.download='pulseops-uptime.csv'; link.click(); URL.revokeObjectURL(href) } catch (err) { setError((err as Error).message) } }

  if (!token) return <div className="login"><div className="login-card"><a className="brand" href="/">Pulse<span>Ops</span></a><p className="kicker">CONTROL ROOM</p><h1>See trouble before your users do.</h1><p>Enter the API token configured on your PulseOps server.</p><form onSubmit={login}><label>API token<input type="password" value={draftToken} onChange={(e) => setDraftToken(e.target.value)} autoComplete="current-password" required /></label>{error && <p className="error" role="alert">{error}</p>}<button>Open dashboard</button></form><a className="public-link" href="/status">View public status page →</a></div></div>

  const down = monitors.filter((m) => stateOf(m) === 'down').length
  const average = monitors.length ? monitors.reduce((sum, m) => sum + m.uptime24h, 0) / monitors.length : 100
  return <div className="shell dashboard">
    <header><a className="brand" href="/">Pulse<span>Ops</span></a><nav><a href="/status">Public status</a><button className="text-button" onClick={() => { sessionStorage.removeItem('pulseops-token'); setToken('') }}>Sign out</button></nav></header>
    <main>
      <div className="headline"><div><p className="kicker">CONTROL ROOM</p><h1>Good {new Date().getHours() < 12 ? 'morning' : 'evening'}.</h1><p>Everything that matters, in one quiet place.</p></div><div className={`overview ${down ? 'outage' : ''}`}><i />{down ? `${down} incident${down > 1 ? 's' : ''}` : 'All clear'}</div></div>
      <div className="metrics"><div><strong>{monitors.length}</strong><span>Monitors</span></div><div><strong>{average.toFixed(2)}%</strong><span>24h uptime</span></div><div><strong>{down}</strong><span>Open incidents</span></div></div>
      {report && <section className="panel report"><div className="section-title"><div><p className="kicker">30 DAY REPORT</p><h2>Service reliability</h2></div><button className="secondary" onClick={downloadReport}>Download CSV</button></div><div className="report-grid">{report.monitors.map((item) => <div key={item.monitorId}><strong>{item.uptime.toFixed(2)}%</strong><span>{item.monitorName} · {item.averageResponseMs} ms · {item.checks} checks</span></div>)}</div></section>}
      <section className="panel form-panel"><div><p className="kicker">{editing ? 'EDIT MONITOR' : 'NEW MONITOR'}</p><h2>{editing ? 'Update endpoint' : 'Watch an endpoint'}</h2></div><form onSubmit={save}>
        <label>Name<input value={form.name} maxLength={100} onChange={(e) => setForm({ ...form, name: e.target.value })} placeholder="Marketing site" required /></label>
        <label>Monitor type<select value={form.monitorType} disabled={editing !== null} onChange={(e) => setForm({ ...form, monitorType: e.target.value as MonitorForm['monitorType'] })}><option value="http">HTTP / HTTPS</option><option value="heartbeat">Cron heartbeat</option><option value="tcp">TCP port</option><option value="dns">DNS lookup</option></select></label>
        {form.monitorType === 'http' && <><label className="wide">HTTP/HTTPS URL<input type="url" value={form.url} maxLength={2048} onChange={(e) => setForm({ ...form, url: e.target.value })} placeholder="https://example.com/health" required /></label><label className="wide">Expected content <span className="optional">optional</span><input value={form.expectedKeyword} maxLength={500} onChange={(e) => setForm({ ...form, expectedKeyword: e.target.value })} placeholder="ok" /></label></>}
        {(form.monitorType === 'tcp' || form.monitorType === 'dns') && <label className="wide">{form.monitorType === 'tcp' ? 'Host and port' : 'Hostname'}<input value={form.url} maxLength={2048} onChange={(e) => setForm({ ...form, url: e.target.value })} placeholder={form.monitorType === 'tcp' ? 'example.com:443' : 'example.com'} required /></label>}
        <label>Interval (seconds)<input type="number" min={15} max={86400} value={form.intervalSeconds} onChange={(e) => setForm({ ...form, intervalSeconds: Number(e.target.value) })} required /></label>
        <label>Timeout (seconds)<input type="number" min={1} max={30} value={form.timeoutSeconds} onChange={(e) => setForm({ ...form, timeoutSeconds: Number(e.target.value) })} required /></label>
        <label>Failures before incident<input type="number" min={1} max={10} value={form.failureThreshold} onChange={(e) => setForm({ ...form, failureThreshold: Number(e.target.value) })} required /></label>
        <label>Successes before recovery<input type="number" min={1} max={10} value={form.recoveryThreshold} onChange={(e) => setForm({ ...form, recoveryThreshold: Number(e.target.value) })} required /></label>
        <label className="wide">Maintenance until <span className="optional">optional</span><input type="datetime-local" value={form.maintenanceUntil} onChange={(e) => setForm({ ...form, maintenanceUntil: e.target.value })} /></label>
        <label className="check"><input type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} /> Active</label><label className="check"><input type="checkbox" checked={form.public} onChange={(e) => setForm({ ...form, public: e.target.checked })} /> Public</label>
        <div className="actions"><button disabled={busy}>{busy ? 'Saving…' : editing ? 'Save changes' : 'Add monitor'}</button>{editing && <button type="button" className="secondary" onClick={() => { setEditing(null); setForm(blank) }}>Cancel</button>}</div>
      </form></section>
      {heartbeatUrl && <p className="token-result"><strong>Send a POST after every successful job — copy now:</strong> <code>{heartbeatUrl}</code></p>}
      {error && <p className="error banner" role="alert">{error}</p>}
      <section className="monitor-section"><div className="section-title"><div><p className="kicker">ENDPOINTS</p><h2>Your monitors</h2></div><button className="secondary" onClick={load}>Refresh</button></div>
        <div className="monitor-grid">{monitors.length === 0 ? <div className="empty">Add your first endpoint above. PulseOps will check it within seconds.</div> : monitors.map((monitor) => <article className="monitor-card" key={monitor.id}><div className="card-top"><span className={`pill ${stateOf(monitor)}`}>{stateOf(monitor)}</span><span className="uptime">{monitor.uptime24h.toFixed(2)}%</span></div><h3>{monitor.name}</h3>{monitor.monitorType === 'http' ? <a href={monitor.url} target="_blank" rel="noreferrer">{monitor.url}</a> : <span className="monitor-kind">{monitor.monitorType === 'heartbeat' ? `Cron heartbeat · every ${monitor.intervalSeconds}s` : `${monitor.monitorType.toUpperCase()} · ${monitor.url}`}</span>}<div className="card-data"><div><span>{monitor.monitorType === 'http' ? 'Response' : monitor.monitorType === 'heartbeat' ? 'Last ping' : 'Latency'}</span><strong>{monitor.monitorType === 'heartbeat' ? date(monitor.lastCheckedAt) : monitor.lastResponseMs == null ? '—' : `${monitor.lastResponseMs} ms`}</strong></div><div><span>Last check</span><strong>{date(monitor.lastCheckedAt)}</strong></div><div><span>SSL expiry</span><strong>{monitor.certificateExpiresAt ? date(monitor.certificateExpiresAt) : '—'}</strong></div></div>{monitor.expectedKeyword && <p className="monitor-kind">Content contains “{monitor.expectedKeyword}”</p>}{monitor.lastError && <p className="incident">{monitor.lastError}</p>}<div className="card-actions"><button className="secondary" onClick={() => edit(monitor)}>Edit</button><button className="danger" onClick={() => remove(monitor)}>Delete</button></div></article>)}</div>
      </section>
      <section className="incident-section"><div className="section-title"><div><p className="kicker">TIMELINE</p><h2>Recent incidents</h2></div></div><div className="timeline">{incidents.length === 0 ? <div className="empty">No incidents recorded.</div> : incidents.map((incident) => <article className="timeline-row" key={incident.id}><i className={incident.resolvedAt ? 'resolved' : 'open'} /><div><div className="timeline-title"><strong>{incident.monitorName}</strong><span className={`pill ${incident.resolvedAt ? 'up' : 'down'}`}>{incident.resolvedAt ? 'resolved' : incident.acknowledgedAt ? 'acknowledged' : 'open'}</span></div><p>{incident.cause}{incident.note && ` · ${incident.note}`}</p><small>Started {date(incident.startedAt)}{incident.resolvedAt && ` · Resolved ${date(incident.resolvedAt)}`}</small><button className="secondary compact" onClick={() => annotate(incident)}>Acknowledge / note</button></div></article>)}</div></section>
      {audit.length > 0 && <section className="panel audit"><div className="section-title"><div><p className="kicker">ACCOUNTABILITY</p><h2>Recent changes</h2></div></div><div className="audit-list">{audit.slice(0, 20).map((event, index) => <div className="audit-row" key={`${event.createdAt}-${index}`}><span className="audit-action">{event.action}</span><span className="audit-resource">{event.resource}</span><span className="audit-actor">{event.actor}</span><time dateTime={event.createdAt}>{date(event.createdAt)}</time></div>)}</div></section>}
      <section className="panel keys"><div><p className="kicker">TEAM ACCESS</p><h2>Members and API keys</h2><p>Viewers inspect data, operators manage monitoring, and admins manage team access.</p></div><form onSubmit={createKey}><label>Member or integration<input value={newKey.name} maxLength={100} onChange={(e) => setNewKey({ ...newKey, name: e.target.value })} placeholder="On-call engineer" required /></label><label>Role<select value={newKey.scope} onChange={(e) => setNewKey({ ...newKey, scope: e.target.value as 'read' | 'write' | 'admin' })}><option value="read">Viewer</option><option value="write">Operator</option><option value="admin">Admin</option></select></label><button>Create access key</button></form>{createdToken && <p className="token-result"><strong>Copy now — shown once:</strong> <code>{createdToken}</code></p>}<div className="key-list">{keys.map((key) => <div key={key.id}><span><strong>{key.name}</strong><small>{key.prefix}… · {key.scope === 'read' ? 'viewer' : key.scope === 'write' ? 'operator' : 'admin'} · {key.lastUsedAt ? `used ${date(key.lastUsedAt)}` : 'never used'}</small></span><button className="danger compact" onClick={() => deleteKey(key)}>Revoke</button></div>)}</div></section>
    </main>
    <footer>PulseOps · PostgreSQL-backed monitoring</footer>
  </div>
}
