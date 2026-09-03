import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

describe('App', () => {
  beforeEach(() => sessionStorage.clear())
  afterEach(() => vi.unstubAllGlobals())

  it('protects the dashboard with the configured token', () => { render(<App />); expect(screen.getByRole('heading', { name: 'See trouble before your users do.' })).toBeInTheDocument(); expect(screen.getByLabelText('API token')).toBeInTheDocument() })

  it('shows reliability controls on the dashboard', async () => {
    sessionStorage.setItem('pulseops-token', '0123456789abcdef')
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => [] })))
    vi.stubGlobal('EventSource', class { addEventListener() {}; close() {} })
    render(<App />)
    expect(await screen.findByLabelText('Failures before incident')).toBeInTheDocument()
    expect(screen.getByLabelText(/Maintenance until/)).toBeInTheDocument()
  })
})
