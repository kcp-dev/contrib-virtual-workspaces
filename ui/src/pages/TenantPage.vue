<script setup lang="ts">
// One tenant, managed. A tenant is not a place you can work in, so this
// is the screen that stands in for going there: what it contains, who you
// are in it, and the one thing you can do to it — add a project.
//
// Creating a project belongs here rather than only on the cross-tenant
// list, because a tenant with no projects has nothing to select and so no
// way to be reached from the project picker at all.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '@/stores/session'
import { requestProject, ApiError } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'
import RoleBadge from '@/components/RoleBadge.vue'

const session = useSession()
const route = useRoute()

const name = computed(() => String(route.params.name ?? ''))
const tenant = computed(() => session.tenants.find((t) => t.name === name.value))
const projects = computed(() => tenant.value?.projects ?? [])
const mayCreate = computed(() => (tenant.value?.roles ?? []).includes('admin'))

const creating = ref(false)
const showForm = ref(false)
const newName = ref('')
const note = ref('')
const noteError = ref(false)

async function create() {
  const project = newName.value.trim()
  if (!project || !name.value) return
  creating.value = true
  note.value = ''
  try {
    const answer = await requestProject({ tenant: name.value, name: project })
    noteError.value = false
    note.value = answer.status?.message ?? `Created ${project}.`
    newName.value = ''
    showForm.value = false
    // The workspace provisions asynchronously; the review reports it.
    await session.load()
  } catch (e) {
    noteError.value = true
    note.value =
      e instanceof ApiError && e.forbidden
        ? `Forbidden: requires admin on ${name.value}.`
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
// Switching tenants in the URL drops a half-typed name rather than
// carrying it to a tenant it was not meant for.
watch(name, () => {
  showForm.value = false
  newName.value = ''
  note.value = ''
})
</script>

<template>
  <div>
    <PageHeader :title="tenant?.displayName || name" :subtitle="name">
      <template #actions>
        <button v-if="mayCreate && !showForm" class="k-btn k-btn--primary" @click="showForm = true">
          New project
        </button>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <Panel v-if="showForm" class="mb-4">
      <template #header>New project in {{ name }}</template>
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
    </Panel>

    <div v-if="!session.loading && !tenant" class="k-card px-4 py-6 text-xs text-muted">
      No tenant {{ name }} — or nothing grants you access to it.
    </div>

    <div v-else class="grid gap-6 lg:grid-cols-2">
      <Panel>
        <template #header>Projects</template>
        <StateBlock
          :loading="session.loading"
          :error="session.error"
          :empty="!projects.length"
          empty-text="No projects."
        >
          <table class="k-table">
            <thead>
              <tr><th>Project</th><th>Role</th></tr>
            </thead>
            <tbody>
              <tr
                v-for="p in projects"
                :key="p.name"
                class="is-interactive"
                :class="name === session.tenant && p.name === session.project && 'is-selected'"
                @click="session.select(name, p.name)"
              >
                <td>
                  <div class="font-medium truncate">{{ p.displayName || p.name }}</div>
                  <div class="font-mono text-[10px] text-faint truncate">{{ p.endpoint || '—' }}</div>
                </td>
                <td class="w-28">
                  <RoleBadge :roles="p.roles?.length ? p.roles : tenant?.roles" />
                </td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
        <div v-if="!mayCreate" class="px-4 py-2.5 border-t border-edge text-[11px] text-faint">
          Adding a project requires admin here.
        </div>
      </Panel>

      <Panel>
        <template #header>Your access</template>
        <div class="px-4 py-3 space-y-2.5">
          <div class="flex items-center gap-2">
            <RoleBadge :roles="tenant?.roles" />
            <span class="text-[11px] text-faint">on the tenant</span>
          </div>
          <p class="text-[11px] text-faint">
            A tenant-wide role reaches every project in it, including ones added later.
          </p>
          <p class="text-[11px] text-faint">
            The tenant workspace itself holds the Project and Membership records and is closed to
            members — the projects above are what you enter.
          </p>
        </div>
        <div v-if="mayCreate" class="px-4 py-2.5 border-t border-edge">
          <RouterLink :to="`/tenants/${name}/access`" class="text-[11px] text-accent hover:underline">
            Manage who else may reach this tenant →
          </RouterLink>
        </div>
      </Panel>
    </div>
  </div>
</template>
