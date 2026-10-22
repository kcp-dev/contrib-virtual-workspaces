<script setup lang="ts">
// A viewer for whatever the selected workspace serves. Which APIs exist
// in a workspace is the interesting part of kcp: they are there because
// something was bound, so this is the enabling story seen from inside.
import { computed, onMounted, ref, watch } from 'vue'
import { useSession } from '@/stores/session'
import {
  groups,
  resources,
  objectTable,
  ApiError,
  type APIResource,
  type TableColumn,
  type TableRow,
} from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'

const session = useSession()
const workspace = computed(() => session.workspace)

const groupVersions = ref<string[]>([])
const groupVersion = ref('')
const kinds = ref<APIResource[]>([])
const kind = ref('')
// Rendered by the server, so these are whatever printer columns the
// resource declares — the same ones the resource's own page shows, rather
// than a second opinion about how to display it.
const columns = ref<TableColumn[]>([])
const rows = ref<TableRow[]>([])
const listable = ref(true)

const loadingGroups = ref(false)
const loadingKinds = ref(false)
const loadingObjects = ref(false)
const error = ref('')

async function loadGroups() {
  groupVersions.value = []
  kinds.value = []
  rows.value = []
  if (!workspace.value) return

  loadingGroups.value = true
  error.value = ''
  try {
    const gs = await groups(workspace.value)
    // v1 is always served and is not in /apis; it is what people look for first.
    groupVersions.value = ['v1', ...gs.map((g) => g.preferredVersion?.groupVersion ?? g.name).filter(Boolean)]
    groupVersion.value = groupVersions.value[0] ?? ''
    await loadKinds()
  } catch (e) {
    error.value = describe(e)
  } finally {
    loadingGroups.value = false
  }
}

async function loadKinds() {
  kinds.value = []
  rows.value = []
  kind.value = ''
  if (!workspace.value || !groupVersion.value) return

  loadingKinds.value = true
  try {
    kinds.value = await resources(workspace.value, groupVersion.value)
  } catch (e) {
    error.value = describe(e)
  } finally {
    loadingKinds.value = false
  }
}

async function loadObjects(name: string) {
  kind.value = name
  rows.value = []
  columns.value = []
  error.value = ''
  listable.value = (kinds.value.find((k) => k.name === name)?.verbs ?? []).includes('list')
  if (!listable.value) return

  loadingObjects.value = true
  try {
    const t = await objectTable(workspace.value, groupVersion.value, name)
    // Kept whole: a cell is addressed by its column's index, so filtering
    // the definitions here would put the wrong value under each heading.
    columns.value = t.columnDefinitions ?? []
    rows.value = t.rows ?? []
  } catch (e) {
    error.value = describe(e)
  } finally {
    loadingObjects.value = false
  }
}

// The viewer is a browser, not a console: it shows the default columns,
// and the resource's own page is where the wide ones and the verbs live.
const shownIndexes = computed(() =>
  columns.value.map((c, i) => ({ c, i })).filter(({ c }) => !c.priority).map(({ i }) => i),
)
const shown = computed(() => columns.value.filter((c) => !c.priority))

function cell(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—'
  if (typeof v === 'object') return JSON.stringify(v)
  return String(v)
}

/** The route of the resource's own page, where it can also be changed. */
function pageFor(name: string): string {
  const g = groupVersion.value === 'v1' ? '-' : groupVersion.value.split('/')[0]
  return `/r/${g}/${name}`
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.forbidden) return 'Forbidden.'
  return e instanceof Error ? e.message : String(e)
}

onMounted(async () => {
  if (!session.tenants.length) await session.load()
  await loadGroups()
})
watch(workspace, loadGroups)
watch(groupVersion, loadKinds)
</script>

<template>
  <div>
    <PageHeader
      title="API Explorer"
      subtitle="Browse the APIs this project offers and the objects in them you have access to."
    >
      <template #actions>
        <span v-if="session.project" class="k-badge k-badge--muted">
          {{ session.tenant }}/{{ session.project }}
        </span>
      </template>
    </PageHeader>

    <div v-if="!workspace" class="k-card px-4 py-6 text-xs text-muted">
      No project selected.
    </div>

    <div v-else class="grid gap-4 lg:grid-cols-[13rem_15rem_1fr]">
      <Panel>
        <template #header>Group / version</template>
        <StateBlock :loading="loadingGroups" :empty="!groupVersions.length" empty-text="Nothing served.">
          <ul class="max-h-[28rem] overflow-auto p-1">
            <li
              v-for="gv in groupVersions"
              :key="gv"
              class="px-2 py-1.5 rounded-[4px] font-mono text-[11px] cursor-pointer truncate transition-colors text-muted hover:text-ink hover:bg-hover/60"
              :class="gv === groupVersion && '!bg-accent-subtle !text-accent-hover'"
              @click="groupVersion = gv"
            >
              {{ gv }}
            </li>
          </ul>
        </StateBlock>
      </Panel>

      <Panel>
        <template #header>Kinds</template>
        <StateBlock :loading="loadingKinds" :empty="!kinds.length" empty-text="No resources.">
          <ul class="max-h-[28rem] overflow-auto p-1">
            <li
              v-for="r in kinds"
              :key="r.name"
              class="px-2 py-1.5 rounded-[4px] cursor-pointer transition-colors text-muted hover:text-ink hover:bg-hover/60"
              :class="r.name === kind && '!bg-accent-subtle !text-accent-hover'"
              @click="loadObjects(r.name)"
            >
              <div class="text-[12px] truncate">{{ r.kind }}</div>
              <div class="font-mono text-[10px] text-faint truncate">{{ r.name }}</div>
            </li>
          </ul>
        </StateBlock>
      </Panel>

      <Panel>
        <template #header>
          <span>{{ kind || 'Objects' }}</span>
          <RouterLink
            v-if="kind"
            :to="pageFor(kind)"
            class="ml-auto text-[11px] text-accent hover:underline"
          >
            Open →
          </RouterLink>
        </template>
        <StateBlock
          :loading="loadingObjects"
          :error="error"
          :empty="!kind || !rows.length"
          :empty-text="!kind ? 'Select a kind.' : listable ? 'None.' : 'This kind can only be created, not listed, so there are no objects to show.'"
        >
          <table class="k-table">
            <thead>
              <tr><th v-for="c in shown" :key="c.name" :title="c.description">{{ c.name }}</th></tr>
            </thead>
            <tbody>
              <tr v-for="(row, i) in rows" :key="i">
                <td v-for="idx in shownIndexes" :key="idx" :class="idx === 0 && 'font-medium'">
                  {{ cell(row.cells?.[idx]) }}
                </td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
      </Panel>
    </div>
  </div>
</template>
