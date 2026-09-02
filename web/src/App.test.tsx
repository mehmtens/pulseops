import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { App } from './App'

describe('App', () => { it('protects the dashboard with the configured token', () => { render(<App />); expect(screen.getByRole('heading', { name: 'See trouble before your users do.' })).toBeInTheDocument(); expect(screen.getByLabelText('API token')).toBeInTheDocument() }) })
