<script setup lang="ts">
// One enabled producer, in this project: how to use it, then what it
// serves.
//
// This is the page the sidebar lands on, instead of a bare table for
// whichever kind the producer happens to serve first. A producer that is
// an API gets its tables one click away; a producer that is a service —
// MCP — gets what it actually needs to show, which is how to connect.
// Neither wrote a line of UI: the instructions are a ConfigMap, the
// tables come from the schema.
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useSession } from '@/stores/session'
import * as oidc from '@/lib/oidc'
import { bindings, offerings, ApiError, type APIBinding, type CatalogOffering } from '@/lib/api'
import type { Variables } from '@/lib/instructions'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'
import Instructions from '@/components/Instructions.vue'

const route = useRoute()
const session = useSession()

const name = computed(() => String(route.params.binding ?? ''))
const workspace = computed(() => session.workspace)

const binding = ref<APIBinding | undefined>()
const offer = ref<CatalogOffering | undefined>()
const loading = ref(false)
const error = ref('')

async function load() {
  binding.value = undefined
  offer.value = undefined
  if (!workspace.value || !name.value) return
  loading.value = true
  error.value = ''
  try {
    const [bound, catalog] = await Promise.all([bindings(workspace.value), offerings()])
    binding.value = (bound.items ?? []).find((b) => b.metadata.name === name.value)
    const ref = binding.value?.spec?.reference?.export
    // The catalog entry is found by the binding's own coordinates, so a
    // binding made by hand still gets its producer's page if the
    // coordinates match — and a plain "nothing in the catalog" if not.
    offer.value = (catalog.items ?? []).find(
      (o) => o.export === ref?.name && o.workspacePath === ref?.path,
    )
  } catch (e) {
    error.value = e instanceof ApiError && e.forbidden ? 'Forbidden.' : e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const served = computed(() =>
  (binding.value?.status?.boundResources ?? []).map((r) => ({
    group: r.group ?? '',
    resource: r.resource,
    to: `/r/${r.group || '-'}/${r.resource}`,
  })),
)

const title = computed(() => offer.value?.displayName || name.value)

// What the instructions may refer to. The server is where kcp is, which in
// development is not this page's origin — the dev server proxies to it.
const server = ((import.meta.env.VITE_KCP_SERVER as string) || window.location.origin).replace(/\/$/, '')
const vars = computed<Variables>(() => ({
  server,
  workspace: workspace.value ? server + workspace.value : '',
  cluster: workspace.value.replace(/^\/clusters\//, ''),
  tenant: session.tenant,
  project: session.project,
  user: session.username,
  export: offer.value?.export ?? binding.value?.spec?.reference?.export?.name ?? '',
  producerWorkspace: offer.value?.workspacePath ?? binding.value?.spec?.reference?.export?.path ?? '',
  binding: name.value,
  issuer: oidc.config().issuer,
  clientId: oidc.config().clientId,
}))

// Whether the instructions are open. Remembered per browser, per producer:
// once you have connected your client you want the tables, not the
// walkthrough, but the next producer's steps are still new to you.
// localStorage is a convenience, not state — it can be absent or throw
// (private window, blocked site data), and then the page simply opens.
const STORAGE_HIDDEN = 'contrib.producer.howtoHidden'

function readHidden(): Set<string> {
  try {
    const raw = localStorage.getItem(STORAGE_HIDDEN)
    return new Set(raw ? (JSON.parse(raw) as string[]) : [])
  } catch {
    return new Set()
  }
}

const hidden = ref<Set<string>>(readHidden())
const howtoHidden = computed(() => hidden.value.has(name.value))

function toggleHowto() {
  const next = new Set(hidden.value)
  if (next.has(name.value)) next.delete(name.value)
  else next.add(name.value)
  hidden.value = next
  try {
    localStorage.setItem(STORAGE_HIDDEN, JSON.stringify([...next]))
  } catch {
    // Nothing to do: the toggle still works for this page view.
  }
}

const hasInstructions = computed(
  () => !!offer.value?.instructions && Object.keys(offer.value.instructions).length > 0,
)

onMounted(load)
watch([workspace, name], load)
</script>

<template>
  <div>
    <PageHeader :title="title" :subtitle="offer?.description" :eyebrow="`${session.tenant}/${session.project}`">
      <template #actions>
        <span
          v-if="binding?.status?.phase === 'Bound'"
          class="k-badge k-badge--success"
        >
          <span class="k-badge__dot" />
          Enabled
        </span>
        <span v-else-if="binding" class="k-badge k-badge--muted">{{ binding.status?.phase || 'pending' }}</span>
        <a
          v-if="offer?.docsURL"
          :href="offer.docsURL"
          target="_blank"
          rel="noreferrer"
          class="k-btn k-btn--ghost"
        >
          Docs ↗
        </a>
      </template>
    </PageHeader>

    <StateBlock :loading="loading" :error="error" :empty="!loading && !binding" :empty-text="`Nothing named ${name} is enabled here.`">
      <div class="space-y-4">
        <!-- How to use it, the width of the page: the instructions carry
             code blocks and tabs, and they are what a service-shaped
             producer is for. Hideable, because once a client is connected
             they are in the way; the choice is remembered per browser. -->
        <Panel v-if="hasInstructions">
          <template #header>
            <span>How to use it</span>
            <button class="ml-auto k-btn k-btn--ghost !px-2 !py-0.5 text-[10px] normal-case tracking-normal" @click="toggleHowto">
              {{ howtoHidden ? 'Show' : 'Hide' }}
            </button>
          </template>
          <Instructions v-if="!howtoHidden" :instructions="offer!.instructions!" :vars="vars" />
        </Panel>

        <!-- No instructions is a legitimate state: an API producer's
             "how to use it" is the tables below. Say so briefly. -->
        <Panel v-else-if="offer">
          <div class="px-4 py-3 text-[11px] text-faint">
            This producer ships no instructions. Each kind below is an ordinary API in this project —
            open one to list, create and edit its objects.
          </div>
        </Panel>

        <Panel v-else-if="binding">
          <div class="px-4 py-3 text-[11px] text-faint">
            Enabled here, but not on the catalog — the binding references
            <code class="font-mono">{{ binding.spec?.reference?.export?.path }}</code> /
            <code class="font-mono">{{ binding.spec?.reference?.export?.name }}</code>, which no producer
            currently publishes under those coordinates.
          </div>
        </Panel>

        <!-- What it serves. Each entry is an ordinary API in this project,
             one click from its table. -->
        <div>
          <div class="k-eyebrow px-0.5 pb-2 flex items-center gap-2">
            <span>Kinds</span>
            <RouterLink to="/catalog" class="ml-auto normal-case tracking-normal text-[11px] text-accent hover:underline">
              Manage in Producers →
            </RouterLink>
          </div>
          <div v-if="served.length" class="grid gap-3 grid-cols-[repeat(auto-fill,minmax(14rem,1fr))]">
            <RouterLink
              v-for="s in served"
              :key="s.to"
              :to="s.to"
              class="k-card p-4 flex flex-col gap-1.5 hover:border-accent transition-colors"
            >
              <div class="text-[13px] font-semibold truncate">
                {{ s.resource.charAt(0).toUpperCase() + s.resource.slice(1) }}
              </div>
              <div class="font-mono text-[10px] text-faint truncate">{{ s.group || 'core' }}</div>
              <div class="mt-auto pt-2 text-[11px] text-accent">Open →</div>
            </RouterLink>
          </div>
          <div v-else class="k-card px-4 py-3 text-[11px] text-faint">
            Nothing bound yet — kcp is still establishing the API.
          </div>
        </div>
      </div>
    </StateBlock>
  </div>
</template>
