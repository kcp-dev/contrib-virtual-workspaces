import { describe, expect, it } from 'vitest'
import { boundKinds, isPlatformGroup, type APIBinding } from './api'

function bound(name: string, group: string, resource: string): APIBinding {
  return {
    metadata: { name },
    status: { phase: 'Bound', boundResources: [{ group, resource }] },
  } as APIBinding
}

describe('boundKinds', () => {
  it('lists what a producer binds, including one in the platform namespace', () => {
    const kinds = boundKinds([
      bound('things.acme.example', 'acme.example', 'things'),
      // MCP is a producer that happens to live under platform.kcp.io; it
      // disappeared from the sidebar once when this was a suffix match.
      bound('mcp', 'mcp.platform.kcp.io', 'mcpendpoints'),
    ])
    expect(kinds.map((k) => k.binding)).toEqual(['things.acme.example', 'mcp'])
  })

  it('hides the platform machinery and anything not yet Bound', () => {
    const pending = { metadata: { name: 'late' }, status: { phase: 'Binding', boundResources: [] } } as APIBinding
    const kinds = boundKinds([bound('tenancy-x', 'tenancy.platform.kcp.io', 'projects'), pending])
    expect(kinds).toEqual([])
  })
})

describe('isPlatformGroup', () => {
  it('is an exact list, not a suffix', () => {
    expect(isPlatformGroup('tenancy.platform.kcp.io')).toBe(true)
    expect(isPlatformGroup('marketplace.platform.kcp.io')).toBe(true)
    expect(isPlatformGroup('mcp.platform.kcp.io')).toBe(false)
    expect(isPlatformGroup('platform.kcp.io')).toBe(false)
  })
})
