<script setup lang="ts">
// The platform's own records: the store where Tenants live and the
// provisioning parent their workspaces are created under. This menu
// appears only when kcp answered that this identity may write there —
// and if that answer is somehow wrong, kcp still refuses the writes.
import { computed, onMounted, ref } from 'vue'
import { useSession } from '@/stores/session'
import { list, createObject, canI, ApiError } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'

const session = useSession()

const STORE = '/clusters/root:tenancy:store'
const CATALOG = '/clusters/root:marketplace:catalog'

const tenants = ref<Record<string, unknown>[]>([])
const providers = ref<Record<string, unknown>[]>([])
const loading = ref(false)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [t, p] = await Promise.allSettled([
      list(STORE, 'tenancy.platform.kcp.io/v1alpha1', 'tenants'),
      list(CATALOG, 'marketplace.platform.kcp.io/v1alpha1', 'producers'),
    ])
    tenants.value = t.status === 'fulfilled' ? (t.value.items ?? []) : []
    providers.value = p.status === 'fulfilled' ? (p.value.items ?? []) : []
    if (t.status === 'rejected') error.value = describe(t.reason)
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}

const mayRegister = ref(false)
const showForm = ref(false)
const newName = ref('')
const newDisplayName = ref('')
const creating = ref(false)
const note = ref('')
const noteError = ref(false)

// The producer's name is also its workspace's name.
const nameValid = computed(() => /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/.test(newName.value.trim()))

async function registerProducer() {
  const name = newName.value.trim()
  if (!nameValid.value) return
  creating.value = true
  note.value = ''
  try {
    await createObject(CATALOG, 'marketplace.platform.kcp.io/v1alpha1', 'producers', {
      apiVersion: 'marketplace.platform.kcp.io/v1alpha1',
      kind: 'Producer',
      metadata: { name },
      spec: { displayName: newDisplayName.value.trim() || name, mode: 'provisioned' },
    })
    noteError.value = false
    note.value = `Registered producer ${name}. Its workspace appears below once the marketplace operator has created it.`
    newName.value = ''
    newDisplayName.value = ''
    showForm.value = false
    await load()
  } catch (e) {
    noteError.value = true
    note.value =
      e instanceof ApiError && e.status === 409 ? `A producer named ${name} already exists.` : describe(e)
  } finally {
    creating.value = false
  }
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.forbidden) return 'Forbidden.'
  return e instanceof Error ? e.message : String(e)
}

function field(o: Record<string, unknown>, path: string[]): string {
  let cur: unknown = o
  for (const p of path) {
    if (typeof cur !== 'object' || cur === null) return '—'
    cur = (cur as Record<string, unknown>)[p]
  }
  return cur === undefined || cur === null || cur === '' ? '—' : String(cur)
}

onMounted(async () => {
  load()
  mayRegister.value = await canI(CATALOG, {
    group: 'marketplace.platform.kcp.io',
    resource: 'producers',
    verb: 'create',
  })
})
</script>

<template>
  <div>
    <PageHeader
      title="Admin"
      subtitle="Platform records: Tenants in the store workspace, Producers in the catalog."
    >
      <template #actions>
        <button v-if="session.admin && mayRegister && !showForm" class="k-btn k-btn--primary" @click="showForm = true">
          Register producer
        </button>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <Panel v-if="session.admin && mayRegister && showForm" class="mb-4">
      <template #header>Register a producer</template>
      <div class="p-3 flex gap-2">
        <input
          v-model="newName"
          placeholder="name, e.g. acme"
          class="k-input flex-1 min-w-0"
          autofocus
          @keyup.enter="registerProducer"
          @keyup.escape="showForm = false"
        />
        <input
          v-model="newDisplayName"
          placeholder="display name (optional)"
          class="k-input flex-1 min-w-0"
          @keyup.enter="registerProducer"
          @keyup.escape="showForm = false"
        />
        <button class="k-btn k-btn--primary" :disabled="creating || !nameValid" @click="registerProducer">
          {{ creating ? 'Registering…' : 'Register' }}
        </button>
        <button class="k-btn k-btn--ghost" @click="showForm = false">Cancel</button>
      </div>
      <p class="px-3 pb-3 text-[11px]" :class="newName.trim() && !nameValid ? 'text-danger' : 'text-faint'">
        The name becomes the producer's workspace name: lowercase letters, digits and hyphens, starting
        and ending with a letter or digit. The display name can be anything.
      </p>
    </Panel>

    <div v-if="!session.admin" class="k-card px-4 py-6 text-xs text-muted">
      Forbidden: you may not write the platform records.
    </div>

    <div v-else class="grid gap-6 lg:grid-cols-2">
      <Panel>
        <template #header>Tenants · {{ STORE }}</template>
        <StateBlock :loading="loading" :error="error" :empty="!tenants.length" empty-text="No tenants yet.">
          <table class="k-table">
            <thead>
              <tr><th>Name</th><th>Phase</th><th>Workspace</th></tr>
            </thead>
            <tbody>
              <tr v-for="(t, i) in tenants" :key="i" >
                <td class="font-medium">{{ field(t, ['metadata', 'name']) }}</td>
                <td class="text-muted">{{ field(t, ['status', 'phase']) }}</td>
                <td class="text-muted">{{ field(t, ['status', 'workspace']) }}</td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
      </Panel>

      <Panel>
        <template #header>Producers · {{ CATALOG }}</template>
        <StateBlock :loading="loading" :empty="!providers.length" empty-text="No producers registered.">
          <table class="k-table">
            <thead>
              <tr><th>Name</th><th>Phase</th><th>Workspace</th></tr>
            </thead>
            <tbody>
              <tr v-for="(p, i) in providers" :key="i" >
                <td class="font-medium">{{ field(p, ['metadata', 'name']) }}</td>
                <td class="text-muted">{{ field(p, ['status', 'phase']) }}</td>
                <td class="text-muted">{{ field(p, ['status', 'workspacePath']) }}</td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
      </Panel>
    </div>

    <p v-if="session.admin" class="mt-3 text-[11px] text-faint">
      Write with <code class="font-mono">contribctl admin</code>.
    </p>
  </div>
</template>
