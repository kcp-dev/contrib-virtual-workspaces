<script setup lang="ts">
// First run: somebody signed in and belongs to nothing.
//
// That is a real state, not an error — a fresh identity has no tenant and
// no project, and every other screen in this app is relative to a project.
// Rather than show them all empty, this asks for the two things that have
// to exist and makes them, in order, explaining what each one is for.
//
// Both steps are requests to the virtual workspace rather than writes:
// nobody can create a tenant record or reach into a tenant workspace
// themselves. The server does it after deciding the caller may.
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import { ApiError, requestProject, requestTenant } from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import Stepper from '@/components/Stepper.vue'
import NestingDiagram from '@/components/NestingDiagram.vue'

const session = useSession()
const router = useRouter()

/** The step we are on, which is just "what does not exist yet". */
const step = computed<'tenant' | 'project' | 'done'>(() => {
  if (!session.tenants.length) return 'tenant'
  if (!session.projects.length) return 'project'
  return 'done'
})

const tenantName = ref('')
const projectName = ref('first')
const busy = ref(false)
const error = ref('')
// What the server said it did, kept visible: a tenant's workspace is
// provisioned asynchronously, so "created" and "usable" are a moment apart
// and saying so is better than a spinner that looks stuck.
const note = ref('')

/** The tenant created here, which the project then goes into. */
const tenant = computed(() => session.tenants[0])

function suggest(): string {
  const email = session.username
  const local = email.includes('@') ? email.split('@')[0] : email
  const slug = local.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  return slug ? slug + '-org' : ''
}

async function createTenant() {
  const name = tenantName.value.trim()
  if (!name) return
  busy.value = true
  error.value = ''
  try {
    const answer = await requestTenant({ name })
    note.value = answer.status?.message ?? `Created ${name}.`
    // The workspace and the admin grant follow asynchronously; the review
    // is what reports them, so poll it rather than assuming.
    await waitForTenant(name)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

/** waitForTenant reloads the review until the tenant is admin-usable. */
async function waitForTenant(name: string) {
  for (let i = 0; i < 30; i++) {
    await session.load()
    const found = session.tenants.find((t) => t.name === name)
    if (found && (found.roles ?? []).includes('admin')) return
    await sleep(2000)
  }
  error.value =
    `Tenant ${name} was created, but your admin grant has not appeared yet. ` +
    'It is seeded once the workspace is ready — reload in a moment.'
}

async function createProject() {
  const name = projectName.value.trim()
  if (!name || !tenant.value) return
  busy.value = true
  error.value = ''
  try {
    const answer = await requestProject({ tenant: tenant.value.name, name })
    note.value = answer.status?.message ?? `Created ${name}.`
    await waitForProject(name)
  } catch (e) {
    error.value = describe(e)
  } finally {
    busy.value = false
  }
}

/**
 * waitForProject waits for an endpoint, not just for the object: a project
 * without one has no workspace yet, and every screen that follows needs
 * the workspace.
 */
async function waitForProject(name: string) {
  for (let i = 0; i < 30; i++) {
    await session.load()
    const found = session.projects.find((p) => p.project.name === name)
    if (found?.project.endpoint) {
      session.select(found.tenant, found.project.name)
      return
    }
    await sleep(2000)
  }
  error.value =
    `Project ${name} was created, but its workspace is still provisioning. ` +
    'It will appear in the picker once it is ready.'
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function describe(e: unknown): string {
  if (e instanceof ApiError && e.status === 503) {
    return 'Self-service is not enabled here, so an administrator has to create your tenant.'
  }
  if (e instanceof ApiError && e.forbidden) return 'Forbidden.'
  return e instanceof Error ? e.message : String(e)
}

const STEPS = [
  { key: 'tenant', label: 'Tenant' },
  { key: 'project', label: 'Project' },
  { key: 'done', label: 'Ready' },
]

onMounted(async () => {
  if (!session.tenants.length) await session.load()
  tenantName.value = suggest()
})
</script>

<template>
  <div class="max-w-3xl">
    <PageHeader title="Welcome" :subtitle="session.username" eyebrow="Getting set up" />

    <!-- Left to right: how much of this is left, at a glance. -->
    <div class="k-card px-5 py-4 mb-4">
      <Stepper :steps="STEPS" :current="step" />
    </div>

    <div v-if="error" class="k-note k-note--danger mb-4">{{ error }}</div>
    <div v-else-if="note" class="k-note k-note--success mb-4">{{ note }}</div>

    <!-- Form on the left, what it is building on the right. -->
    <div class="grid gap-4 md:grid-cols-[minmax(0,1fr)_17rem] items-start">
      <Panel>
        <template #header>
          {{ step === 'tenant' ? 'Name your tenant'
             : step === 'project' ? `Add a project to ${tenant?.name}`
             : 'You are set up' }}
        </template>

        <div v-if="step === 'tenant'" class="px-4 py-3.5 space-y-3">
          <p class="text-[11px] text-faint leading-relaxed">
            A tenant is the account: it holds your projects and decides who may
            reach them. You become its admin. Nobody works inside a tenant
            directly — that is what the project in step 2 is for.
          </p>
          <div class="flex gap-2">
            <input
              v-model="tenantName"
              class="k-input flex-1 min-w-0"
              placeholder="name"
              autofocus
              :disabled="busy"
              @keyup.enter="createTenant"
            />
            <button
              class="k-btn k-btn--primary shrink-0"
              :disabled="busy || !tenantName.trim()"
              @click="createTenant"
            >
              {{ busy ? 'Creating…' : 'Create' }}
            </button>
          </div>
          <p class="text-[11px] text-faint">Lowercase letters, digits and dashes.</p>
        </div>

        <div v-else-if="step === 'project'" class="px-4 py-3.5 space-y-3">
          <p class="text-[11px] text-faint leading-relaxed">
            A project is a real kcp workspace — the thing your kubeconfig points
            at, and where anything you enable from a producer lands. You can add
            more later.
          </p>
          <div class="flex gap-2">
            <input
              v-model="projectName"
              class="k-input flex-1 min-w-0"
              placeholder="name"
              autofocus
              :disabled="busy"
              @keyup.enter="createProject"
            />
            <button
              class="k-btn k-btn--primary shrink-0"
              :disabled="busy || !projectName.trim()"
              @click="createProject"
            >
              {{ busy ? 'Creating…' : 'Create' }}
            </button>
          </div>
        </div>

        <div v-else class="px-4 py-3.5 space-y-3">
          <p class="text-[11px] text-faint leading-relaxed">
            Working in
            <span class="font-medium text-ink">{{ session.tenant }}/{{ session.project }}</span>.
            The picker at the top switches between projects.
          </p>
          <div class="flex gap-2">
            <button class="k-btn k-btn--primary" @click="router.push('/catalog')">
              Browse producers
            </button>
            <button class="k-btn k-btn--ghost" @click="router.push('/tenants')">
              Manage tenants
            </button>
          </div>
        </div>

        <!-- Provisioning is asynchronous, and the wizard waits for the thing
             to be usable rather than for the call to return. Say which. -->
        <div v-if="busy" class="px-4 py-2.5 border-t border-edge text-[11px] text-faint">
          {{ step === 'tenant'
             ? 'Waiting for the workspace and your admin grant…'
             : 'Waiting for the workspace to be provisioned…' }}
        </div>
      </Panel>

      <Panel>
        <template #header>What you are building</template>
        <div class="px-4 py-4 grid place-items-center">
          <NestingDiagram
            :step="step"
            :tenant-name="tenant?.name || tenantName"
            :project-name="session.project || projectName"
          />
        </div>
        <p class="px-4 pb-3.5 text-[11px] text-faint leading-relaxed">
          A tenant is a record store no member enters. The project inside it is
          the workspace you reach.
        </p>
      </Panel>
    </div>
  </div>
</template>
