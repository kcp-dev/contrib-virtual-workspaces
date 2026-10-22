<script setup lang="ts">
// The catalog, and enabling from it. Enabling is a plain APIBinding the
// browser creates with the user's own token — there is no marketplace
// API to call and nothing privileged in the path, so a refusal here is
// kcp's answer about this person, shown as it came.
import { computed, onMounted, ref, watch } from 'vue'
import { useSession } from '@/stores/session'
import {
  offerings,
  createBinding,
  deleteBinding,
  bindings,
  ApiError,
  type APIBinding,
  type CatalogOffering,
} from '@/lib/api'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import StateBlock from '@/components/StateBlock.vue'

const session = useSession()

const items = ref<CatalogOffering[]>([])
const loading = ref(false)
const error = ref('')
const busy = ref('')
const note = ref('')
const noteError = ref(false)

const workspace = computed(() => session.workspace)

// What this project has already bound. Without it every entry offered an
// Enable button, including the ones already enabled — the catalog is global
// but being enabled is a fact about one project, so the two have to be read
// together.
const bound = ref<APIBinding[]>([])

async function load() {
  loading.value = true
  error.value = ''
  try {
    const body = await offerings()
    items.value = body.items ?? []
    bound.value = workspace.value ? (await bindings(workspace.value)).items ?? [] : []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

/**
 * bindingFor finds the binding that enabled an offering, matching on BOTH
 * the export name and the workspace it lives in.
 *
 * Requiring both is deliberate. A binding written by hand may reference its
 * export by cluster id rather than by path, so this can miss one and offer
 * Enable for something already enabled — and that is the safe direction:
 * the create returns 409 and says so. Matching on the name alone would do
 * the opposite, calling an offering enabled because some other producer
 * exports the same name, and leaving no way to enable it.
 */
function bindingFor(offer: CatalogOffering): APIBinding | undefined {
  return bound.value.find((b) => {
    const exported = b.spec?.reference?.export
    return exported?.name === offer.export && exported?.path === offer.workspacePath
  })
}

type State = 'enabled' | 'pending' | 'available'

function stateOf(offer: CatalogOffering): State {
  const binding = bindingFor(offer)
  if (!binding) return 'available'
  return binding.status?.phase === 'Bound' ? 'enabled' : 'pending'
}

async function enable(offer: CatalogOffering) {
  if (!workspace.value || !offer.export || !offer.workspacePath) return
  busy.value = offer.metadata.name
  note.value = ''
  try {
    await createBinding(workspace.value, offer.offering, offer.workspacePath, offer.export, offer.claims ?? [])
    noteError.value = false
    note.value = `Enabled ${offer.metadata.name} in ${session.tenant}/${session.project}.`
    enabledNow.value = offer.offering
    // Re-read, so the entry stops offering to do it again.
    await load()
  } catch (e) {
    noteError.value = true
    note.value =
      e instanceof ApiError && e.forbidden
        ? `Forbidden in ${session.tenant}/${session.project}: you may not create APIBindings, or the offering is unpublished.`
        : e instanceof ApiError && e.status === 409
          ? `${offer.metadata.name} is already enabled.`
          : e instanceof Error
            ? e.message
            : String(e)
  } finally {
    busy.value = ''
  }
}

// Disabling is deleting the binding, so it asks first: the API stops being
// served, and anything in the workspace that was using it stops working
// until it is enabled again.
const confirming = ref<CatalogOffering | null>(null)

// The binding just created, so the success note can point at the page
// that answers "now what" — which for a service-shaped producer is the
// whole point of enabling it.
const enabledNow = ref('')

async function disable(offer: CatalogOffering) {
  const binding = bindingFor(offer)
  if (!binding || !workspace.value) return
  busy.value = offer.metadata.name
  note.value = ''
  try {
    await deleteBinding(workspace.value, binding.metadata.name)
    noteError.value = false
    note.value = `Disabled ${offer.metadata.name} in ${session.tenant}/${session.project}.`
    confirming.value = null
    await load()
  } catch (e) {
    noteError.value = true
    note.value =
      e instanceof ApiError && e.forbidden
        ? `Forbidden in ${session.tenant}/${session.project}: you may not delete APIBindings there.`
        : e instanceof Error
          ? e.message
          : String(e)
  } finally {
    busy.value = ''
  }
}

onMounted(load)
// Being enabled is a fact about one project, so switching re-reads it.
watch(workspace, load)
</script>

<template>
  <div>
    <PageHeader
      title="Producers"
      subtitle="APIs other teams have published. Enable one to use it in this project."
    >
      <template #actions>
        <span v-if="session.project" class="k-badge k-badge--muted">
          {{ session.tenant }}/{{ session.project }}
        </span>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4 flex items-center gap-3" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      <span>{{ note }}</span>
      <RouterLink v-if="!noteError && enabledNow" :to="`/p/${enabledNow}`" class="ml-auto shrink-0 underline">
        How to use it →
      </RouterLink>
    </div>

    <StateBlock :loading="loading" :error="error">
      <div v-if="!items.length" class="k-card p-5 max-w-2xl text-xs text-muted leading-relaxed">
        <div class="text-[13px] font-semibold text-ink mb-1">Nothing has been published yet</div>
        <p class="mb-3">APIs appear here once a producer publishes one. To publish an API:</p>
        <ol class="list-decimal pl-4 space-y-1.5 mb-3">
          <li>Ask a platform admin to register you as a producer. This gives you your own producer workspace.</li>
          <li>In that workspace, define your API as an <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">APIResourceSchema</code> and an <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">APIExport</code>.</li>
          <li>
            Add the annotation <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">marketplace.kcp.hub/publish: "true"</code> to the <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">APIExport</code>,
            and <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">marketplace.kcp.hub/display-name</code> and
            <code class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">marketplace.kcp.hub/description</code> to control how it looks here.
          </li>
        </ol>
        <p>
          For a complete example to copy, see
          <a
            href="https://github.com/kcp-dev/contrib-virtual-workspaces/tree/marketplace.vw/producer-quickstart"
            target="_blank"
            rel="noreferrer"
            class="text-accent underline"
          ><code class="font-mono text-[11px] bg-overlay px-1 rounded-[3px]">producer-quickstart</code></a>
          in the contrib-virtual-workspaces repository.
        </p>
      </div>
      <div v-else class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <div v-for="o in items" :key="o.metadata.name" class="k-card p-4 flex flex-col gap-2.5">
          <div class="flex items-start gap-2">
            <div class="min-w-0">
              <div class="text-[13px] font-semibold truncate">{{ o.displayName || o.metadata.name }}</div>
              <div class="font-mono text-[10px] text-faint truncate">{{ o.metadata.name }}</div>
            </div>
            <span class="ml-auto k-badge shrink-0" :class="o.ready ? 'k-badge--success' : 'k-badge--warning'">
              <span v-if="o.ready" class="k-badge__dot" />
              {{ o.ready ? 'ready' : 'not ready' }}
            </span>
          </div>

          <p v-if="o.description" class="text-xs text-muted leading-relaxed line-clamp-2">{{ o.description }}</p>

          <div class="mt-auto pt-2 border-t border-edge flex items-center gap-2">
            <div class="min-w-0 font-mono text-[10px] text-faint truncate" :title="`${o.workspacePath} → ${o.export}`">
              {{ o.export }}
              <span v-if="o.claims?.length"> · claims {{ o.claims.map((c) => c.resource).join(', ') }}</span>
            </div>
            <!-- Already enabled here: say so rather than offering again.
                 A binding that exists but is not Bound yet is its own
                 state — kcp is still establishing the API. -->
            <div v-if="stateOf(o) === 'enabled'" class="ml-auto shrink-0 flex items-center gap-2">
              <span
                class="k-badge k-badge--success"
                :title="`bound in ${session.tenant}/${session.project}`"
              >
                <span class="k-badge__dot" />
                Enabled
              </span>
              <button
                class="k-btn k-btn--ghost !px-2.5 !py-1"
                :disabled="busy === o.metadata.name"
                @click="confirming = o"
              >
                {{ busy === o.metadata.name ? 'Disabling…' : 'Disable' }}
              </button>
            </div>
            <span
              v-else-if="stateOf(o) === 'pending'"
              class="k-badge k-badge--muted ml-auto shrink-0"
              title="the binding exists; kcp is still establishing the API"
            >
              Enabling…
            </span>
            <button
              v-else
              class="k-btn k-btn--ghost ml-auto shrink-0 !px-2.5 !py-1"
              :disabled="!o.ready || !workspace || busy === o.metadata.name"
              :title="workspace ? '' : 'Select a project'"
              @click="enable(o)"
            >
              {{ busy === o.metadata.name ? 'Enabling…' : 'Enable' }}
            </button>
          </div>
        </div>
      </div>
    </StateBlock>

    <div
      v-if="confirming"
      class="fixed inset-0 z-50 grid place-items-center bg-black/30 px-6"
      @click.self="confirming = null"
    >
      <div class="k-card max-w-sm w-full">
        <div class="px-4 py-2.5 border-b border-edge text-[13px] font-medium">
          Disable {{ confirming.metadata.name }}?
        </div>
        <p class="px-4 py-3 text-xs text-muted leading-relaxed">
          This deletes the APIBinding in {{ session.tenant }}/{{ session.project }}, so
          <code class="font-mono">{{ confirming.export }}</code> stops being served there and
          anything using it stops working. Objects already created through it are left alone, and
          enabling again brings the API back.
        </p>
        <div class="px-4 py-2.5 border-t border-edge flex gap-2">
          <button class="k-btn k-btn--danger" :disabled="!!busy" @click="disable(confirming)">
            Disable
          </button>
          <button class="k-btn k-btn--ghost" @click="confirming = null">Cancel</button>
        </div>
      </div>
    </div>
  </div>
</template>
