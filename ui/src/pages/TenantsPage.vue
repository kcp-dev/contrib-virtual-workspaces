<script setup lang="ts">
// Tenants: the unit access is granted in. A tenant itself is the record
// store the platform keeps Projects and Memberships in — no member can
// enter it — so this page reports membership and creates, and the work
// happens in the projects inside.
import { onMounted, ref } from 'vue'
import { useSession } from '@/stores/session'
import { requestTenant, ApiError } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'
import RoleBadge from '@/components/RoleBadge.vue'

const session = useSession()

const creating = ref(false)
const showForm = ref(false)
const newName = ref('')
const note = ref('')
const noteError = ref(false)

async function create() {
  const name = newName.value.trim()
  if (!name) return
  creating.value = true
  note.value = ''
  try {
    const answer = await requestTenant({ name })
    noteError.value = false
    note.value = answer.status?.message ?? `Created ${name}.`
    newName.value = ''
    showForm.value = false
    // The workspace and the admin grant follow asynchronously; the review
    // is what reports them.
    await session.load()
  } catch (e) {
    noteError.value = true
    note.value =
      e instanceof ApiError && e.status === 503
        ? 'Self-service tenants are not enabled here.'
        : e instanceof ApiError && e.forbidden
          ? 'Forbidden.'
          : e instanceof Error
            ? e.message
            : String(e)
  } finally {
    creating.value = false
  }
}

onMounted(() => {
  if (!session.tenants.length) session.load()
})
</script>

<template>
  <div>
    <PageHeader
      title="Tenants"
      subtitle="A tenant-wide role reaches every project in it, including later ones."
    >
      <template #actions>
        <button v-if="!showForm" class="k-btn k-btn--primary" @click="showForm = true">New tenant</button>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <Panel v-if="showForm" class="mb-4">
      <template #header>New tenant</template>
      <div class="p-3 flex gap-2">
        <input
          v-model="newName"
          placeholder="name"
          class="k-input flex-1 min-w-0"
          autofocus
          @keyup.enter="create"
          @keyup.escape="showForm = false"
        />
        <button class="k-btn k-btn--primary" :disabled="creating || !newName.trim()" @click="create">
          {{ creating ? 'Creating…' : 'Create' }}
        </button>
        <button class="k-btn k-btn--ghost" @click="showForm = false">Cancel</button>
      </div>
      <p class="px-3 pb-3 text-[11px] text-faint">You become its admin once the workspace is ready.</p>
    </Panel>

    <Panel>
      <StateBlock
        :loading="session.loading"
        :error="session.error"
        :empty="!session.tenants.length"
        empty-text="No tenants."
      >
        <table class="k-table">
          <thead>
            <tr><th>Tenant</th><th>Display name</th><th>Your role</th><th>Projects</th></tr>
          </thead>
          <tbody>
            <tr
              v-for="t in session.tenants"
              :key="t.name"
              class="is-interactive"
              :class="t.name === session.tenant && 'is-selected'"
              @click="$router.push({ name: 'tenant', params: { name: t.name } })"
            >
              <td class="font-medium">{{ t.name }}</td>
              <td class="text-muted">{{ t.displayName || '—' }}</td>
              <td><RoleBadge :roles="t.roles" /></td>
              <td class="text-muted">
                <span v-if="(t.projects ?? []).length">{{ (t.projects ?? []).map((p) => p.name).join(', ') }}</span>
                <span v-else>—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </StateBlock>
    </Panel>
  </div>
</template>
