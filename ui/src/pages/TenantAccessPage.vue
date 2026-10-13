<script setup lang="ts">
// Who may reach what in one tenant.
//
// The grants live in the tenant's workspace, which nobody — including its
// admins — may enter, so this talks to the virtual workspace instead. That
// is also why this screen exists at all: without it, adding a colleague
// would need a platform administrator.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '@/stores/session'
import { memberships, grant, revoke, ApiError, type Membership, type Role } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'
import RoleBadge from '@/components/RoleBadge.vue'

const session = useSession()
const route = useRoute()

const name = computed(() => String(route.params.name ?? ''))
const tenant = computed(() => session.tenants.find((t) => t.name === name.value))
const projects = computed(() => tenant.value?.projects ?? [])
const mayManage = computed(() => (tenant.value?.roles ?? []).includes('admin'))

const all = ref<Membership[]>([])
const loading = ref(false)
const error = ref('')

// The API answers for every tenant the caller administers; this screen is
// about one.
const grants = computed(() => all.value.filter((m) => m.spec.tenant === name.value))

async function load() {
  if (!mayManage.value) {
    all.value = []
    return
  }
  loading.value = true
  error.value = ''
  try {
    const body = await memberships()
    all.value = body.items ?? []
  } catch (e) {
    error.value = describe(e)
  } finally {
    loading.value = false
  }
}

const kind = ref<'User' | 'Group'>('User')
const subject = ref('')
const role = ref<Role>('view')
/** scope is '' for tenant-wide, or a project name. */
const scope = ref('')
const busy = ref('')
const note = ref('')
const noteError = ref(false)

async function add() {
  const who = subject.value.trim()
  if (!who) return
  busy.value = 'add'
  note.value = ''
  try {
    const made = await grant({
      subject: { kind: kind.value, name: who },
      role: role.value,
      tenant: name.value,
      ...(scope.value ? { project: scope.value } : {}),
    })
    noteError.value = false
    note.value = `${who} is now ${role.value} on ${scope.value || name.value}.`
    subject.value = ''
    await load()
    // A grant to yourself changes what you may reach, so the review has to
    // be re-read rather than assumed.
    if (who === session.username) await session.load()
    return made
  } catch (e) {
    noteError.value = true
    note.value = describe(e)
  } finally {
    busy.value = ''
  }
}

async function remove(m: Membership) {
  busy.value = m.metadata.name
  note.value = ''
  try {
    await revoke(m.metadata.name)
    noteError.value = false
    note.value = `Revoked ${m.spec.role} for ${m.spec.subject.name}.`
    await load()
    if (m.spec.subject.name === session.username) await session.load()
  } catch (e) {
    noteError.value = true
    note.value = describe(e)
  } finally {
    busy.value = ''
  }
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.forbidden) return `Forbidden: requires admin on ${name.value}.`
  return e instanceof Error ? e.message : String(e)
}

onMounted(async () => {
  if (!session.tenants.length) await session.load()
  await load()
})
watch([name, mayManage], load)
</script>

<template>
  <div>
    <PageHeader title="Access" :subtitle="name">
      <template #actions>
        <RouterLink :to="`/tenants/${name}`" class="k-btn k-btn--ghost">Overview</RouterLink>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <div v-if="!session.loading && !mayManage" class="k-card px-4 py-6 text-xs text-muted">
      Managing access needs the admin role on {{ name }}.
    </div>

    <template v-else>
      <Panel class="mb-4">
        <template #header>Grant access</template>
        <div class="p-3 flex flex-wrap gap-2 items-center">
          <select v-model="kind" class="k-input !w-auto" title="User or group">
            <option value="User">User</option>
            <option value="Group">Group</option>
          </select>
          <input
            v-model="subject"
            :placeholder="kind === 'User' ? 'email or username' : 'group name'"
            class="k-input flex-1 min-w-48"
            @keyup.enter="add"
          />
          <select v-model="role" class="k-input !w-auto" title="What they may do">
            <option value="view">view</option>
            <option value="edit">edit</option>
            <option value="admin">admin</option>
          </select>
          <select v-model="scope" class="k-input !w-auto max-w-44" title="Where it applies">
            <option value="">whole tenant</option>
            <option v-for="p in projects" :key="p.name" :value="p.name">{{ p.name }}</option>
          </select>
          <button
            class="k-btn k-btn--primary"
            :disabled="busy === 'add' || !subject.trim()"
            @click="add"
          >
            {{ busy === 'add' ? 'Granting…' : 'Grant' }}
          </button>
        </div>
        <p class="px-3 pb-3 text-[11px] text-faint">
          The name is matched verbatim against the identity the servers authenticate — for a
          user that is usually an email address. Granting the same subject again changes the role.
        </p>
      </Panel>

      <Panel>
        <template #header>Grants</template>
        <StateBlock :loading="loading" :error="error" :empty="!grants.length" empty-text="No grants.">
          <table class="k-table">
            <thead>
              <tr><th>Subject</th><th>Role</th><th>Applies to</th><th class="w-20"></th></tr>
            </thead>
            <tbody>
              <tr v-for="m in grants" :key="m.metadata.name">
                <td>
                  <div class="font-medium truncate">{{ m.spec.subject.name }}</div>
                  <div class="k-eyebrow">{{ m.spec.subject.kind }}</div>
                </td>
                <td class="w-24"><RoleBadge :roles="[m.spec.role]" /></td>
                <td class="text-muted">
                  {{ m.spec.project ? `project ${m.spec.project}` : 'whole tenant' }}
                </td>
                <td class="text-right">
                  <button
                    class="k-btn k-btn--ghost"
                    :disabled="busy === m.metadata.name"
                    @click="remove(m)"
                  >
                    {{ busy === m.metadata.name ? '…' : 'Revoke' }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </StateBlock>
        <div class="px-4 py-2.5 border-t border-edge text-[11px] text-faint">
          A tenant-wide role reaches every project, including ones added later. The last admin
          cannot be revoked — grant admin to someone else first.
        </div>
      </Panel>
    </template>
  </div>
</template>
