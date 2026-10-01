<script setup lang="ts">
// One enabled producer, in this project: what it serves, with how to use
// it alongside.
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
      <!-- The side column is the editor width ResourcePage uses, so the
           two pages line up: code blocks in instructions need the room, and
           the content area is capped at 64rem anyway. -->
      <div class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_32rem] items-start">
        <!-- What the producer serves is the page. Each entry is an
             ordinary API in this project, one click from its table; no
             heading because the title already says whose these are. -->
        <div class="min-w-0">
          <div v-if="served.length" class="grid gap-3 grid-cols-[repeat(auto-fill,minmax(12rem,1fr))]">
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

        <div class="space-y-4 min-w-0">
          <Panel v-if="offer?.instructions && Object.keys(offer.instructions).length">
            <template #header>How to use it</template>
            <Instructions :instructions="offer.instructions" :vars="vars" />
          </Panel>

          <!-- No instructions is a legitimate state: an API producer's
               "how to use it" is the tables. Say so briefly. -->
          <Panel v-else-if="offer">
            <template #header>How to use it</template>
            <div class="px-4 py-3 text-[11px] text-faint">
              This producer ships no instructions. Each entry it serves is an ordinary API in this
              project — open one to list, create and edit its objects.
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

          <RouterLink to="/catalog" class="block text-[11px] text-accent hover:underline">
            Manage in Producers →
          </RouterLink>
        </div>
      </div>
    </StateBlock>
  </div>
</template>
