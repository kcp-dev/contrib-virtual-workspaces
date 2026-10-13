// Turning a resource's OpenAPI schema into form fields.
//
// Only the parts of a schema that have an unambiguous control are turned
// into one: a scalar leaf becomes an input, an enum becomes a select. Lists
// and free-form objects are left alone rather than guessed at — there is no
// single right control for them, and inventing one would be wrong for
// exactly the producers nobody has seen yet. Those stay in the YAML tab,
// and the form says so instead of pretending they do not exist.

import type { OpenAPISchema } from './api'

export interface FormField {
  /** Where the value lives, e.g. ['spec', 'message']. */
  path: string[]
  /** Dotted path, minus the leading spec — what the label shows. */
  label: string
  type: 'string' | 'integer' | 'number' | 'boolean' | 'enum'
  description?: string
  required: boolean
  options?: string[]
  /** The schema's own default, used when a create starts empty. */
  default?: unknown
}

export interface FormShape {
  fields: FormField[]
  /** Paths a control would have to guess at; the YAML tab owns these. */
  complex: string[]
}

const MAX_DEPTH = 4

/**
 * shapeOf walks a spec schema and collects the fields a form can offer.
 *
 * Depth is capped because a schema can nest arbitrarily and a form that
 * scrolls forever helps nobody; anything deeper is reported as complex and
 * edited as YAML.
 */
export function shapeOf(schema: OpenAPISchema | null, root: string[] = ['spec']): FormShape {
  const fields: FormField[] = []
  const complex: string[] = []
  if (schema) walk(schema, root, [], fields, complex, 0)
  return { fields, complex }
}

function walk(
  schema: OpenAPISchema,
  path: string[],
  labelParts: string[],
  fields: FormField[],
  complex: string[],
  depth: number,
): void {
  const required = new Set(schema.required ?? [])
  for (const [name, raw] of Object.entries(schema.properties ?? {})) {
    const at = [...path, name]
    const label = [...labelParts, name].join('.')

    // An enum has one obvious control whatever its declared type is.
    if (raw.enum?.length) {
      fields.push({
        path: at,
        label,
        type: 'enum',
        description: raw.description,
        required: required.has(name),
        options: raw.enum.map(String),
        default: raw.default,
      })
      continue
    }

    switch (raw.type) {
      case 'string':
      case 'integer':
      case 'number':
      case 'boolean':
        fields.push({
          path: at,
          label,
          type: raw.type,
          description: raw.description,
          required: required.has(name),
          default: raw.default,
        })
        break

      case 'object':
        // An object with named properties is just more fields; one without
        // is a free-form map, which has no fixed shape to render.
        if (raw.properties && depth < MAX_DEPTH) {
          walk(raw, at, [...labelParts, name], fields, complex, depth + 1)
        } else {
          complex.push(label)
        }
        break

      default:
        // Arrays, and anything the schema does not pin down.
        complex.push(label)
    }
  }
}

/** valueAt reads a path out of an object, or undefined. */
export function valueAt(object: Record<string, unknown>, path: string[]): unknown {
  let at: unknown = object
  for (const key of path) {
    if (at === null || typeof at !== 'object') return undefined
    at = (at as Record<string, unknown>)[key]
  }
  return at
}

/**
 * setValueAt writes a path into an object, creating the objects along the
 * way. An empty value removes the key instead of writing a blank, so an
 * untouched optional field is absent rather than set to "".
 */
export function setValueAt(object: Record<string, unknown>, path: string[], value: unknown): void {
  const last = path[path.length - 1]
  let at = object
  for (const key of path.slice(0, -1)) {
    if (typeof at[key] !== 'object' || at[key] === null) at[key] = {}
    at = at[key] as Record<string, unknown>
  }
  if (value === '' || value === undefined || value === null) delete at[last]
  else at[last] = value
}
