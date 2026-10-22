// The form is generated, so what it decides to render — and what it
// refuses to — is logic worth pinning down. Two silent regressions in this
// UI came from behaviour that only a comment described.

import { describe, expect, it } from 'vitest'
import type { OpenAPISchema } from './api'
import { setValueAt, shapeOf, valueAt } from './form'

describe('shapeOf', () => {
  it('renders a scalar field and carries its description', () => {
    const { fields, complex } = shapeOf({
      type: 'object',
      properties: { message: { type: 'string', description: 'What this Thing says.' } },
    })
    expect(fields).toHaveLength(1)
    expect(fields[0]).toMatchObject({
      path: ['spec', 'message'],
      label: 'message',
      type: 'string',
      required: false,
      description: 'What this Thing says.',
    })
    expect(complex).toEqual([])
  })

  it('makes an enum a select, whatever its declared type', () => {
    const { fields } = shapeOf({
      type: 'object',
      required: ['size'],
      properties: { size: { type: 'string', enum: ['small', 'large'], default: 'small' } },
    })
    expect(fields[0]).toMatchObject({ type: 'enum', options: ['small', 'large'], required: true })
  })

  it('flattens a nested object into dotted fields', () => {
    const { fields } = shapeOf({
      type: 'object',
      properties: { limits: { type: 'object', properties: { cpu: { type: 'string' } } } },
    })
    expect(fields[0]).toMatchObject({ label: 'limits.cpu', path: ['spec', 'limits', 'cpu'] })
  })

  it('refuses to guess at lists and free-form objects', () => {
    const { fields, complex } = shapeOf({
      type: 'object',
      properties: {
        ports: { type: 'array', items: { type: 'integer' } },
        labels: { type: 'object' },
      },
    })
    expect(fields).toEqual([])
    expect(complex).toEqual(['ports', 'labels'])
  })

  it('caps depth rather than nesting forever', () => {
    let deep: OpenAPISchema = { type: 'object', properties: { leaf: { type: 'string' } } }
    for (let i = 0; i < 6; i++) deep = { type: 'object', properties: { [`l${i}`]: deep } }
    const { fields, complex } = shapeOf(deep)
    expect(fields).toEqual([])
    expect(complex).toHaveLength(1)
  })

  it('treats no schema as a supported state', () => {
    expect(shapeOf(null)).toEqual({ fields: [], complex: [] })
  })
})

describe('paths', () => {
  it('creates the objects along the way', () => {
    const o: Record<string, unknown> = {}
    setValueAt(o, ['spec', 'message'], 'hi')
    expect(o).toEqual({ spec: { message: 'hi' } })
    expect(valueAt(o, ['spec', 'message'])).toBe('hi')
  })

  it('removes a key rather than writing an empty string', () => {
    const o: Record<string, unknown> = { spec: { message: 'hi' } }
    setValueAt(o, ['spec', 'message'], '')
    expect(o).toEqual({ spec: {} })
  })

  // The reason the emptiness test is `=== ''` and not a falsy check: 0 and
  // false are values someone meant to set.
  it('keeps zero and false', () => {
    const o: Record<string, unknown> = {}
    setValueAt(o, ['spec', 'count'], 0)
    setValueAt(o, ['spec', 'paused'], false)
    expect(o).toEqual({ spec: { count: 0, paused: false } })
  })

  it('reads a missing path as undefined', () => {
    expect(valueAt({}, ['nope', 'deep'])).toBeUndefined()
  })
})
