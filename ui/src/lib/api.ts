// Every call the UI makes goes to kcp's front-proxy, carrying the user's
// own token. There is no backend of our own in between: what the UI can
// see is exactly what kcp and the virtual workspaces will answer for that
// identity, so the screens cannot show more than the person may have.

import { token } from './oidc'

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly reason?: string,
  ) {
    super(message)
  }

  /** forbidden is the one a caller usually wants to treat as information rather than failure. */
  get forbidden(): boolean {
    return this.status === 403
  }

  get unauthorized(): boolean {
    return this.status === 401
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const t = token()
  const headers = new Headers(init.headers)
  if (!headers.has('Accept')) headers.set('Accept', 'application/json')
  if (t) headers.set('Authorization', `Bearer ${t}`)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')

  const resp = await fetch(path, { ...init, headers })
  const text = await resp.text()
  const body = text ? safeJson(text) : null

  if (!resp.ok) {
    const fromBody =
      body && typeof body === 'object' && 'message' in body
        ? (body as { message?: string }).message
        : undefined
    const message = fromBody || `${resp.status} ${resp.statusText}`
    const reason =
      body && typeof body === 'object' && 'reason' in body ? (body as { reason?: string }).reason : undefined
    throw new ApiError(message, resp.status, reason)
  }
  return body as T
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text)
  } catch {
    return { message: text }
  }
}

// ── the tenancy virtual workspace ────────────────────────────────────

export interface ProjectClaim {
  name: string
  displayName?: string
  roles?: string[]
  endpoint?: string
}

export interface TenantClaim {
  name: string
  displayName?: string
  roles?: string[]
  endpoint?: string
  projects?: ProjectClaim[]
}

export interface SelfTenancyReview {
  status?: { tenants?: TenantClaim[] }
}

/**
 * review asks the tenancy virtual workspace what this identity can reach.
 * It is a create, not a get: the answer is computed for the caller, and
 * the request body carries no username — whoever the token says you are
 * is who the answer is about.
 */
export function review(): Promise<SelfTenancyReview> {
  return request<SelfTenancyReview>(
    '/services/tenancy/apis/tenancy.platform.kcp.io/v1alpha1/selftenancyreviews',
    { method: 'POST', body: JSON.stringify({ apiVersion: 'tenancy.platform.kcp.io/v1alpha1', kind: 'SelfTenancyReview' }) },
  )
}

// ── the marketplace virtual workspace ────────────────────────────────

export interface OfferingClaim {
  group?: string
  resource: string
  verbs?: string[]
  /** Required when the claimed resource is itself served by an APIExport. */
  identityHash?: string
  /** The producer's suggested scope; absent means every object. */
  matchLabels?: Record<string, string>
}

export interface CatalogOffering {
  metadata: { name: string }
  producer: string
  offering: string
  displayName?: string
  description?: string
  export?: string
  workspacePath?: string
  /** revision identifies the claim surface: a digest, not a counter. */
  revision?: string
  claims?: OfferingClaim[]
  ready: boolean
  docsURL?: string
  /**
   * The producer's "how to use this", keyed by section. Static text with
   * {{variables}} the console fills in — see lib/instructions.ts.
   */
  instructions?: Record<string, string>
}

export function offerings(): Promise<{ items?: CatalogOffering[] }> {
  return request('/services/marketplace/apis/marketplace.platform.kcp.io/v1alpha1/offerings')
}

/**
 * requestTenant asks for a tenant, with the caller as its admin.
 *
 * The one request with no membership behind it — it is how someone with
 * nothing gets started — so the server decides whether self-service is
 * offered at all.
 */
export function requestTenant(spec: {
  name: string
  displayName?: string
  description?: string
}): Promise<{ status?: { created?: boolean; tenant?: string; message?: string } }> {
  return request('/services/tenancy/apis/tenancy.platform.kcp.io/v1alpha1/selftenantrequests', {
    method: 'POST',
    body: JSON.stringify({
      apiVersion: 'tenancy.platform.kcp.io/v1alpha1',
      kind: 'SelfTenantRequest',
      spec,
    }),
  })
}

/**
 * requestProject asks the tenancy virtual workspace to create a project.
 *
 * It is a request rather than a create because the caller cannot write
 * there themselves: projects live in the tenant's workspace, which is
 * also where the Memberships deciding access live, so the virtual
 * workspace holds the only credential into that tier and performs the
 * one write it offers — after checking the caller's own admin Membership.
 */
export function requestProject(spec: {
  tenant: string
  name: string
  displayName?: string
  description?: string
}): Promise<{ status?: { created?: boolean; project?: string; message?: string } }> {
  return request('/services/tenancy/apis/tenancy.platform.kcp.io/v1alpha1/selfprojectrequests', {
    method: 'POST',
    body: JSON.stringify({
      apiVersion: 'tenancy.platform.kcp.io/v1alpha1',
      kind: 'SelfProjectRequest',
      spec,
    }),
  })
}

// ── grants ───────────────────────────────────────────────────────────

export type Role = 'view' | 'edit' | 'admin'

export interface Membership {
  metadata: { name: string }
  spec: {
    subject: { kind: 'User' | 'Group'; name: string }
    role: Role
    tenant: string
    /** Empty for a tenant-wide grant, which reaches every project in it. */
    project?: string
  }
}

const MEMBERSHIPS = '/services/tenancy/apis/tenancy.platform.kcp.io/v1alpha1/memberships'

/**
 * memberships lists the grants of every tenant the caller administers.
 *
 * Served by the virtual workspace rather than read from the tenant
 * workspace, which members — admins included — may not enter: the grants
 * deciding their access live there. The list is not scoped to one tenant,
 * so a screen about one filters on `spec.tenant`.
 */
export function memberships(): Promise<{ items?: Membership[] }> {
  return request(MEMBERSHIPS)
}

/**
 * grant gives a subject a role, on a whole tenant or one of its projects.
 *
 * The object's name is derived by the server from what is granted to whom,
 * so granting the same access twice changes the role in place instead of
 * leaving two objects that disagree.
 */
export function grant(spec: Membership['spec']): Promise<Membership> {
  return request(MEMBERSHIPS, {
    method: 'POST',
    body: JSON.stringify({
      apiVersion: 'tenancy.platform.kcp.io/v1alpha1',
      kind: 'Membership',
      spec,
    }),
  })
}

/** revoke removes a grant by the name the list reported for it. */
export function revoke(name: string): Promise<unknown> {
  return request(`${MEMBERSHIPS}/${encodeURIComponent(name)}`, { method: 'DELETE' })
}

// ── workspaces themselves ────────────────────────────────────────────

/** clusterPath turns a workspace URL from a review into a same-origin path. */
export function clusterPath(endpoint: string): string {
  try {
    return new URL(endpoint).pathname.replace(/\/$/, '')
  } catch {
    return endpoint.replace(/\/$/, '')
  }
}

export interface APIGroup {
  name: string
  preferredVersion?: { groupVersion: string; version: string }
  versions?: { groupVersion: string; version: string }[]
}

export interface APIResource {
  name: string
  singularName?: string
  namespaced: boolean
  kind: string
  verbs: string[]
}

/** groups lists the API groups served in a workspace: what is bound there. */
export async function groups(cluster: string): Promise<APIGroup[]> {
  const body = await request<{ groups?: APIGroup[] }>(`${cluster}/apis`)
  return body.groups ?? []
}

/** resources lists what one group/version serves. */
export async function resources(cluster: string, groupVersion: string): Promise<APIResource[]> {
  const path = groupVersion === 'v1' ? `${cluster}/api/v1` : `${cluster}/apis/${groupVersion}`
  const body = await request<{ resources?: APIResource[] }>(path)
  return (body.resources ?? []).filter((r) => !r.name.includes('/'))
}

/** list fetches the objects of one resource, for the viewer. */
export function list(
  cluster: string,
  groupVersion: string,
  resource: string,
): Promise<{ items?: Record<string, unknown>[] }> {
  const base = groupVersion === 'v1' ? `${cluster}/api/v1` : `${cluster}/apis/${groupVersion}`
  return request(`${base}/${resource}?limit=100`)
}

// ── generic CRUD on whatever a workspace serves ──────────────────────
//
// None of this knows anything about any provider. An offering is an
// ordinary Kubernetes API once its APIBinding exists, so discovery says
// what is there and what may be done to it, a Table request renders it
// with the provider's own printer columns, and create/update/delete are
// the plain REST verbs. A provider writes an APIResourceSchema and gets a
// working screen; there is nowhere for them to put UI code even if they
// wanted to.

/** apiBase is /api/v1 for the core group, /apis/<group>/<version> otherwise. */
function apiBase(cluster: string, groupVersion: string): string {
  return groupVersion === 'v1' ? `${cluster}/api/v1` : `${cluster}/apis/${groupVersion}`
}

/**
 * collectionPath addresses a resource's collection. A namespaced resource
 * with no namespace given is addressed across all of them, which is right
 * for listing and wrong for creating — so create always passes one.
 */
export function collectionPath(
  cluster: string,
  groupVersion: string,
  resource: string,
  namespace?: string,
): string {
  const base = apiBase(cluster, groupVersion)
  return namespace ? `${base}/namespaces/${namespace}/${resource}` : `${base}/${resource}`
}

export interface TableColumn {
  name: string
  type: string
  format?: string
  description?: string
  /** priority > 0 is a wide-only column, exactly as kubectl treats it. */
  priority?: number
}

export interface TableRow {
  cells: unknown[]
  object?: Record<string, unknown>
}

export interface Table {
  columnDefinitions?: TableColumn[]
  rows?: TableRow[]
}

// Asking for a Table is what gets the CRD's additionalPrinterColumns:
// the server renders the rows, so the columns are the provider's, not
// ours. The fallback to plain JSON matters — a resource whose server has
// no table converter would otherwise fail rather than degrade.
const TABLE_ACCEPT =
  'application/json;as=Table;v=1;g=meta.k8s.io,application/json'

/**
 * objectTable lists a resource for display, preferring a server-rendered
 * Table so the columns are the provider's own printer columns.
 *
 * A Table is a request the server is free to decline, and asking for one
 * carries two things that can go wrong independently: content negotiation
 * may hand back an ordinary List, and `includeObject` is a Table-only
 * parameter that a plain list request has no reason to accept. So the
 * answer is normalized, and any failure falls back to the plain GET that
 * every resource answers.
 *
 * The fallback is the point. Without it a negotiation detail shows an
 * empty screen for a resource that plainly has objects in it — which is
 * exactly what happened when this function did not have one.
 */
export async function objectTable(
  cluster: string,
  groupVersion: string,
  resource: string,
  namespace?: string,
): Promise<Table> {
  const path = collectionPath(cluster, groupVersion, resource, namespace)

  try {
    const body = await request<Table & { kind?: string; items?: Record<string, unknown>[] }>(
      `${path}?limit=200&includeObject=Object`,
      { headers: { Accept: TABLE_ACCEPT } },
    )
    if (body.kind === 'Table' && body.columnDefinitions?.length) {
      return { columnDefinitions: body.columnDefinitions, rows: body.rows ?? [] }
    }
    if (body.items) return fromItems(body.items)
    if (body.rows) return fromItems(body.rows.map((r) => r.object ?? {}))
  } catch {
    // Fall through: whatever went wrong, a plain list still works.
  }

  const plain = await request<{ items?: Record<string, unknown>[] }>(`${path}?limit=200`)
  return fromItems(plain.items ?? [])
}

/** fromItems builds the two columns every object can answer for. */
function fromItems(items: Record<string, unknown>[]): Table {
  return {
    columnDefinitions: [
      { name: 'Name', type: 'string' },
      { name: 'Age', type: 'date' },
    ],
    rows: items.map((object) => ({
      cells: [
        (object.metadata as { name?: string } | undefined)?.name ?? '',
        (object.metadata as { creationTimestamp?: string } | undefined)?.creationTimestamp ?? '',
      ],
      object,
    })),
  }
}

export function createObject(
  cluster: string,
  groupVersion: string,
  resource: string,
  object: Record<string, unknown>,
  namespace?: string,
): Promise<Record<string, unknown>> {
  return request(collectionPath(cluster, groupVersion, resource, namespace), {
    method: 'POST',
    body: JSON.stringify(object),
  })
}

export function replaceObject(
  cluster: string,
  groupVersion: string,
  resource: string,
  name: string,
  object: Record<string, unknown>,
  namespace?: string,
): Promise<Record<string, unknown>> {
  const path = collectionPath(cluster, groupVersion, resource, namespace)
  return request(`${path}/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: JSON.stringify(object),
  })
}

export function deleteObject(
  cluster: string,
  groupVersion: string,
  resource: string,
  name: string,
  namespace?: string,
): Promise<unknown> {
  const path = collectionPath(cluster, groupVersion, resource, namespace)
  return request(`${path}/${encodeURIComponent(name)}`, { method: 'DELETE' })
}

// ── the schema, for a create skeleton ────────────────────────────────

export interface OpenAPISchema {
  type?: string
  properties?: Record<string, OpenAPISchema>
  required?: string[]
  $ref?: string
  items?: OpenAPISchema
  description?: string
  enum?: unknown[]
  default?: unknown
  format?: string
}

export interface OpenAPIDoc {
  paths?: Record<string, { post?: { requestBody?: { content?: Record<string, { schema?: OpenAPISchema }> } } }>
  components?: { schemas?: Record<string, OpenAPISchema> }
}

/**
 * openapi fetches the schema document for one group/version in one
 * workspace. kcp serves this per logical cluster, built from the
 * APIResourceSchema the provider published — so a consumer workspace
 * describes the provider's API at its own address.
 */
export function openapi(cluster: string, groupVersion: string): Promise<OpenAPIDoc> {
  const path = groupVersion === 'v1' ? 'api/v1' : `apis/${groupVersion}`
  return request<OpenAPIDoc>(`${cluster}/openapi/v3/${path}`)
}

/**
 * skeleton builds a starting object for a create.
 *
 * The schema is found by following the collection path's POST body rather
 * than by guessing a key in components.schemas, whose naming is an
 * implementation detail. If anything is missing — no document, no path, no
 * schema — the result is still a valid object to edit, just emptier.
 */
export function skeleton(
  doc: OpenAPIDoc | null,
  groupVersion: string,
  resource: string,
  kind: string,
): Record<string, unknown> {
  const base: Record<string, unknown> = {
    apiVersion: groupVersion,
    kind,
    metadata: { name: '' },
  }

  const schema = doc ? postSchemaFor(doc, resource) : null
  const spec = schema?.properties?.spec
  if (!spec) return { ...base, spec: {} }

  const stub: Record<string, unknown> = {}
  // Required fields first and always; a handful of optional ones so the
  // shape is visible without pasting the whole schema into the editor.
  const required = new Set(spec.required ?? [])
  for (const [field, def] of Object.entries(spec.properties ?? {})) {
    if (required.has(field) || required.size === 0) stub[field] = emptyFor(def)
  }
  return { ...base, spec: stub }
}

/**
 * specSchema is the schema of the object's spec, which is the part a
 * person fills in. Resolved through the same POST body the skeleton uses,
 * so the form and the skeleton cannot describe different shapes.
 */
export function specSchema(doc: OpenAPIDoc | null, resource: string): OpenAPISchema | null {
  if (!doc) return null
  const object = postSchemaFor(doc, resource)
  const spec = object?.properties?.spec
  return spec ? resolveRef(doc, spec) : null
}

function postSchemaFor(doc: OpenAPIDoc, resource: string): OpenAPISchema | null {
  for (const [path, ops] of Object.entries(doc.paths ?? {})) {
    // The collection path ends in the resource; item paths end in {name}.
    if (!path.endsWith(`/${resource}`)) continue
    const ref = ops.post?.requestBody?.content?.['application/json']?.schema
    if (!ref) continue
    return resolveRef(doc, ref)
  }
  return null
}

function resolveRef(doc: OpenAPIDoc, schema: OpenAPISchema): OpenAPISchema | null {
  if (!schema.$ref) return schema
  const key = schema.$ref.replace('#/components/schemas/', '')
  const target = doc.components?.schemas?.[key]
  return target ? resolveRef(doc, target) : null
}

/** emptyFor is a placeholder of the right JSON shape for a field. */
function emptyFor(def: OpenAPISchema): unknown {
  switch (def.type) {
    case 'array':
      return []
    case 'object':
      return {}
    case 'boolean':
      return false
    case 'integer':
    case 'number':
      return 0
    default:
      return ''
  }
}

// ── what may I do? ───────────────────────────────────────────────────

/**
 * canI asks kcp itself, with a SelfSubjectAccessReview, rather than
 * guessing from group membership. The UI hides what the answer says is
 * not allowed — but the answer comes from the same authorizer that would
 * refuse the real request, so hiding is a courtesy, not the control.
 */
export async function canI(
  cluster: string,
  attrs: { group?: string; resource: string; verb: string; namespace?: string },
): Promise<boolean> {
  try {
    const body = await request<{ status?: { allowed?: boolean } }>(
      `${cluster}/apis/authorization.k8s.io/v1/selfsubjectaccessreviews`,
      {
        method: 'POST',
        body: JSON.stringify({
          apiVersion: 'authorization.k8s.io/v1',
          kind: 'SelfSubjectAccessReview',
          spec: { resourceAttributes: { ...attrs } },
        }),
      },
    )
    return body.status?.allowed === true
  } catch {
    // Not being allowed to ask is itself an answer.
    return false
  }
}

/** createBinding enables an offering: the APIBinding is the whole act. */
/**
 * createBinding enables an offering: the APIBinding is the whole act.
 *
 * The catalog's claims are accepted on it, which is not a detail — for an
 * offering whose claims ARE its function, a binding that accepts none of
 * them grants nothing and looks like it worked. Accepting here is the
 * consent, made on the consumer's own object.
 */
export function createBinding(
  cluster: string,
  name: string,
  exportPath: string,
  exportName: string,
  claims: OfferingClaim[] = [],
): Promise<unknown> {
  return request(`${cluster}/apis/apis.kcp.io/v1alpha2/apibindings`, {
    method: 'POST',
    body: JSON.stringify({
      apiVersion: 'apis.kcp.io/v1alpha2',
      kind: 'APIBinding',
      metadata: { name },
      spec: {
        reference: { export: { path: exportPath, name: exportName } },
        permissionClaims: claims.map((c) => ({
          group: c.group ?? '',
          resource: c.resource,
          verbs: c.verbs ?? [],
          identityHash: c.identityHash ?? '',
          // The provider's suggested scope, or every object — which is
          // what a binding means by default.
          selector: c.matchLabels ? { matchLabels: c.matchLabels } : { matchAll: true },
          state: 'Accepted',
        })),
      },
    }),
  })
}

/**
 * deleteBinding disables an offering. Deleting the APIBinding IS disabling:
 * the API stops being served in the workspace, and the claims it carried are
 * revoked with it. Objects already created through it are not removed — they
 * simply have no API in front of them until it is enabled again.
 */
export function deleteBinding(cluster: string, name: string): Promise<unknown> {
  return request(
    `${cluster}/apis/apis.kcp.io/v1alpha2/apibindings/${encodeURIComponent(name)}`,
    { method: 'DELETE' },
  )
}

export interface BoundResource {
  group?: string
  resource: string
  schema?: { name?: string }
}

export interface APIBinding {
  metadata: { name: string }
  spec?: { reference?: { export?: { path?: string; name?: string } } }
  status?: { phase?: string; boundResources?: BoundResource[] }
}

export function bindings(cluster: string): Promise<{ items?: APIBinding[] }> {
  return request(`${cluster}/apis/apis.kcp.io/v1alpha2/apibindings`)
}

/**
 * isPlatformGroup reports whether a group is the platform's own machinery
 * — tenancy, the marketplace — rather than something a producer offers.
 *
 * Named exactly, not matched by suffix. The suffix rule that was here
 * before (`*.platform.kcp.io`) read well but was wrong in practice: the
 * platform's own bindings in a project bind no resources at all, so the
 * rule hid nothing it meant to — and did hide MCP, a producer that merely
 * shares the suffix. A new component is more likely a producer than
 * machinery, so the default is to show it.
 */
const platformGroups = new Set(['tenancy.platform.kcp.io', 'marketplace.platform.kcp.io'])

export function isPlatformGroup(group: string): boolean {
  return platformGroups.has(group)
}

/**
 * boundKinds flattens what the bindings in a workspace actually put
 * there. A binding reports this itself in status.boundResources, so the
 * UI does not have to guess from the export's name — what is listed is
 * what is really servable.
 */
export function boundKinds(items: APIBinding[]): { binding: string; group: string; resource: string }[] {
  const out: { binding: string; group: string; resource: string }[] = []
  for (const b of items) {
    if (b.status?.phase !== 'Bound') continue
    for (const r of b.status.boundResources ?? []) {
      // The platform's own bindings carry the machinery a project needs
      // rather than anything a person asked for; they are visible in the
      // API viewer, just not worth a menu entry.
      if (isPlatformGroup(r.group ?? '')) continue
      out.push({ binding: b.metadata.name, group: r.group ?? '', resource: r.resource })
    }
  }
  return out
}
