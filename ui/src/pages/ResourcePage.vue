<script setup lang="ts">
// One enabled offering's resources, with the ordinary Kubernetes verbs.
//
// Nothing here knows what the resource is. The columns are the ones the
// producer declared on their APIResourceSchema, rendered by the server in
// answer to a Table request — the same path kubectl takes — and which
// buttons exist comes from discovery's verbs. A producer publishes a
// schema and gets this screen; there is no place for them to put UI code.
//
// The table and the editor sit side by side rather than one replacing the
// other: picking a row loads it into the editor, so reading the list and
// changing a thing in it are the same motion.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '@/stores/session'
import {
  ApiError,
  createObject,
  deleteObject,
  objectTable,
  openapi,
  replaceObject,
  resources,
  skeleton,
  specSchema,
  type APIResource,
  type OpenAPISchema,
  type TableColumn,
  type TableRow,
} from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'
import ObjectEditor from '@/components/ObjectEditor.vue'

const route = useRoute()
const session = useSession()

// The route spells the core group "-", because an empty path segment is
// not a thing.
const group = computed(() => {
  const g = String(route.params.group ?? '')
  return g === '-' ? '' : g
})
const resource = computed(() => String(route.params.resource ?? ''))
const workspace = computed(() => session.workspace)

const columns = ref<TableColumn[]>([])
const rows = ref<TableRow[]>([])
const meta = ref<APIResource | undefined>()
const groupVersion = ref('')
const loading = ref(false)
const error = ref('')
const wide = ref(false)

/** '' is the list on its own; the rest open the side panel. */
const mode = ref<'' | 'create' | 'edit' | 'view'>('')
const subject = ref<Record<string, unknown> | null>(null)
const selected = ref<TableRow | null>(null)
const busy = ref('')
const note = ref('')
const noteError = ref(false)
const confirming = ref<TableRow | null>(null)

// A namespaced resource has to be created somewhere. `default` is what the
// tenancy operator provisions in every project workspace, so it is the
// right guess rather than an arbitrary one.
const namespace = ref('default')
const namespaced = computed(() => meta.value?.namespaced === true)

// The spec's schema, read once per resource: the form is generated from it
// and so is the create skeleton, so the two cannot describe different
// shapes. Null is a supported state — the editor falls back to YAML only.
const schema = ref<OpenAPISchema | null>(null)

const can = (verb: string) => (meta.value?.verbs ?? []).includes(verb)

// kubectl hides priority > 0 columns unless asked; so does this. The
// definitions are kept whole because a cell is addressed by its column's
// index — filtering them would put values under the wrong heading.
const shown = computed(() => columns.value.filter((c) => wide.value || !c.priority))
const shownIndexes = computed(() =>
  columns.value.map((c, i) => ({ c, i })).filter(({ c }) => wide.value || !c.priority).map(({ i }) => i),
)
const hasWide = computed(() => columns.value.some((c) => c.priority))

async function load() {
  loading.value = true
  error.value = ''
  try {
    if (!workspace.value || !resource.value) {
      rows.value = []
      columns.value = []
      return
    }
    const gv = group.value ? await preferredVersion() : 'v1'
    groupVersion.value = gv
    meta.value = (await resources(workspace.value, gv)).find((r) => r.name === resource.value)

    // Listed across namespaces: what is here matters more than where.
    const t = await objectTable(workspace.value, gv, resource.value)
    columns.value = t.columnDefinitions ?? []
    rows.value = t.rows ?? []

    // Best effort: a resource with no readable schema is still listable,
    // editable and deletable, just without a generated form.
    schema.value = await loadSchema(gv)
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}

async function preferredVersion(): Promise<string> {
  const doc = await fetchJSON<{ preferredVersion?: { groupVersion?: string } }>(
    `${workspace.value}/apis/${group.value}`,
  )
  return doc.preferredVersion?.groupVersion ?? `${group.value}/v1alpha1`
}

async function fetchJSON<T>(path: string): Promise<T> {
  const resp = await fetch(path, {
    headers: {
      Accept: 'application/json',
      Authorization: `Bearer ${localStorage.getItem('contrib.idToken') ?? ''}`,
    },
  })
  if (!resp.ok) throw new ApiError(`${path} answered ${resp.status}`, resp.status)
  return (await resp.json()) as T
}

/** loadSchema resolves the spec schema, or null if anything is missing. */
async function loadSchema(gv: string): Promise<OpenAPISchema | null> {
  try {
    return specSchema(await openapi(workspace.value, gv), resource.value)
  } catch {
    return null
  }
}

async function startCreate() {
  note.value = ''
  busy.value = 'skeleton'
  try {
    let doc = null
    try {
      doc = await openapi(workspace.value, groupVersion.value)
    } catch {
      doc = null
    }
    subject.value = skeleton(doc, groupVersion.value, resource.value, meta.value?.kind ?? '')
    mode.value = 'create'
    selected.value = null
  } finally {
    busy.value = ''
  }
}

/**
 * Picking a row opens it in the side panel — to edit if you may, to read
 * if not.
 *
 * A create in progress is left alone: its YAML was typed and exists
 * nowhere else, whereas switching between existing objects only loses
 * edits that can be re-read from the server.
 */
function pick(row: TableRow) {
  if (mode.value === 'create') return
  if (selected.value === row) {
    close()
    return
  }
  selected.value = row
  subject.value = row.object ?? null
  mode.value = row.object && can('update') ? 'edit' : 'view'
}

function close() {
  mode.value = ''
  subject.value = null
  selected.value = null
}

async function save(object: Record<string, unknown>) {
  busy.value = 'save'
  note.value = ''
  try {
    const ns = namespaced.value ? nsOf(object) || namespace.value : undefined
    if (mode.value === 'create') {
      await createObject(workspace.value, groupVersion.value, resource.value, object, ns)
      note.value = `Created ${nameOf(object)}.`
    } else {
      await replaceObject(workspace.value, groupVersion.value, resource.value, nameOf(object), object, ns)
      note.value = `Updated ${nameOf(object)}.`
    }
    noteError.value = false
    close()
    await load()
  } catch (e) {
    noteError.value = true
    note.value = describe(e)
  } finally {
    busy.value = ''
  }
}

async function remove(row: TableRow) {
  const name = row.object ? nameOf(row.object) : ''
  if (!name) return
  busy.value = name
  note.value = ''
  try {
    await deleteObject(
      workspace.value,
      groupVersion.value,
      resource.value,
      name,
      namespaced.value ? nsOf(row.object!) || namespace.value : undefined,
    )
    noteError.value = false
    note.value = `Deleted ${name}.`
    confirming.value = null
    if (selected.value === row) close()
    await load()
  } catch (e) {
    noteError.value = true
    note.value = describe(e)
  } finally {
    busy.value = ''
  }
}

function nameOf(o: Record<string, unknown>): string {
  return (o.metadata as { name?: string } | undefined)?.name ?? ''
}

function nsOf(o: Record<string, unknown>): string {
  return (o.metadata as { namespace?: string } | undefined)?.namespace ?? ''
}

/**
 * cell renders one table cell. A date column carries the raw timestamp —
 * the server leaves formatting to the client, which is why kubectl shows
 * an age rather than an RFC 3339 string.
 */
function cell(v: unknown, column?: TableColumn): string {
  if (v === null || v === undefined || v === '') return '—'
  if (column?.type === 'date') return age(String(v))
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

function age(timestamp: string): string {
  const then = Date.parse(timestamp)
  if (Number.isNaN(then)) return timestamp
  const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000))
  if (seconds < 60) return `${seconds}s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours < 48) return `${hours}h`
  return `${Math.floor(hours / 24)}d`
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.forbidden) return 'Forbidden — your role here does not allow that.'
  // The point of sending resourceVersion: a write that would have
  // overwritten someone else's is refused rather than silently winning.
  if (e instanceof ApiError && e.status === 409) {
    return 'This changed since you opened it. Close the editor and pick it again to get the current version.'
  }
  return e instanceof Error ? e.message : String(e)
}

onMounted(load)
watch([workspace, resource, group], () => {
  close()
  load()
})
</script>

<template>
  <div>
    <PageHeader
      :title="meta?.kind ? `${meta.kind}s` : resource"
      :subtitle="groupVersion || 'core'"
      :eyebrow="`${session.tenant}/${session.project}`"
    >
      <template #actions>
        <label v-if="hasWide" class="text-[11px] text-muted flex items-center gap-1.5">
          <input v-model="wide" type="checkbox" /> wide
        </label>
        <button
          v-if="can('create')"
          class="k-btn k-btn--primary"
          :disabled="busy === 'skeleton'"
          @click="startCreate"
        >
          New {{ meta?.kind || resource }}
        </button>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <div v-if="namespaced && can('create')" class="mb-4 flex items-center gap-2">
      <span class="k-eyebrow">Namespace</span>
      <input v-model="namespace" class="k-input !w-auto max-w-40" />
      <span class="text-[11px] text-faint">used when the object names none</span>
    </div>

    <!-- Left: the list, always. Right: whatever is being looked at. -->
    <div class="grid gap-4 items-start" :class="mode && 'xl:grid-cols-[minmax(0,1fr)_32rem]'">
      <Panel>
        <StateBlock
          :loading="loading"
          :error="error"
          :empty="!rows.length"
          :empty-text="`No ${resource}.`"
        >
          <table class="k-table">
            <thead>
              <tr>
                <th v-for="c in shown" :key="c.name" :title="c.description">{{ c.name }}</th>
                <th v-if="can('delete')" class="w-16"></th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="(row, i) in rows"
                :key="i"
                :class="[mode !== 'create' && 'is-interactive', selected === row && 'is-selected']"
                @click="pick(row)"
              >
                <td
                  v-for="idx in shownIndexes"
                  :key="idx"
                  :class="idx === 0 && 'font-medium'"
                >
                  {{ cell(row.cells?.[idx], columns[idx]) }}
                </td>
                <td v-if="can('delete')" class="text-right">
                  <button
                    v-if="row.object"
                    class="k-btn k-btn--ghost !px-2"
                    @click.stop="confirming = row"
                  >
                    Delete
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
      </Panel>

      <div v-if="mode" class="xl:sticky xl:top-6 space-y-2">
        <p v-if="mode === 'create'" class="text-[11px] text-faint">
          Finish or cancel this to open something from the list.
        </p>

        <ObjectEditor
          v-if="subject && (mode === 'create' || mode === 'edit')"
          :object="subject"
          :title="mode === 'create' ? `New ${meta?.kind || resource}` : `Edit ${nameOf(subject)}`"
          :busy="busy === 'save'"
          :mode="mode === 'create' ? 'create' : 'edit'"
          :schema="schema"
          @save="save"
          @cancel="close"
        />

        <Panel v-else-if="subject">
          <template #header>
            <span>{{ nameOf(subject) || 'Object' }}</span>
            <span class="ml-auto k-badge k-badge--muted">read-only</span>
          </template>
          <pre class="px-4 py-3 font-mono text-[11px] leading-relaxed overflow-auto max-h-[32rem] text-muted">{{ JSON.stringify(subject, null, 2) }}</pre>
          <div class="px-4 py-2.5 border-t border-edge">
            <button class="k-btn k-btn--ghost" @click="close">Close</button>
          </div>
        </Panel>
      </div>
    </div>

    <!-- Deleting is not undoable, so it is the one thing that asks. -->
    <div
      v-if="confirming"
      class="fixed inset-0 z-50 grid place-items-center bg-black/30 px-6"
      @click.self="confirming = null"
    >
      <div class="k-card max-w-sm w-full">
        <div class="px-4 py-2.5 border-b border-edge text-[13px] font-medium">
          Delete {{ confirming.object ? nameOf(confirming.object) : '' }}?
        </div>
        <p class="px-4 py-3 text-xs text-muted">
          This deletes the object in {{ session.tenant }}/{{ session.project }}. It cannot be undone.
        </p>
        <div class="px-4 py-2.5 border-t border-edge flex gap-2">
          <button class="k-btn k-btn--danger" :disabled="!!busy" @click="remove(confirming)">
            Delete
          </button>
          <button class="k-btn k-btn--ghost" @click="confirming = null">Cancel</button>
        </div>
      </div>
    </div>
  </div>
</template>
