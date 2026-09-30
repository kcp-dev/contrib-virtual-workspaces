<script setup lang="ts">
// The catalog, and enabling from it. Enabling is a plain APIBinding the
// browser creates with the user's own token — there is no marketplace
// API to call and nothing privileged in the path, so a refusal here is
// kcp's answer about this person, shown as it came.
import { computed, onMounted, ref } from 'vue'
import { useSession } from '@/stores/session'
import { offerings, createBinding, ApiError, type CatalogOffering } from '@/lib/api'
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

async function load() {
  loading.value = true
  error.value = ''
  try {
    const body = await offerings()
    items.value = body.items ?? []
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function enable(offer: CatalogOffering) {
  if (!workspace.value || !offer.export || !offer.workspacePath) return
  busy.value = offer.metadata.name
  note.value = ''
  try {
    await createBinding(workspace.value, offer.offering, offer.workspacePath, offer.export)
    noteError.value = false
    note.value = `Enabled ${offer.metadata.name} in ${session.tenant}/${session.project}.`
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

onMounted(load)
</script>

<template>
  <div>
    <PageHeader
      title="Service Catalog"
      subtitle="Each entry carries the coordinates of the APIBinding enabling it creates."
    >
      <template #actions>
        <span v-if="session.project" class="k-badge k-badge--muted">
          {{ session.tenant }}/{{ session.project }}
        </span>
      </template>
    </PageHeader>

    <div v-if="note" class="k-note mb-4" :class="noteError ? 'k-note--danger' : 'k-note--success'">
      {{ note }}
    </div>

    <StateBlock :loading="loading" :error="error" :empty="!items.length" empty-text="The catalog is empty.">
      <div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
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
            <button
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
  </div>
</template>
