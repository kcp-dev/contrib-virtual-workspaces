<script setup lang="ts">
import { computed } from 'vue'
import { useSession } from '@/stores/session'
import * as oidc from '@/lib/oidc'
import PageHeader from '@/components/PageHeader.vue'
import Panel from '@/components/Panel.vue'
import RoleBadge from '@/components/RoleBadge.vue'

const session = useSession()
const cfg = oidc.config()

const expiry = computed(() => {
  const exp = session.claims?.exp
  return exp ? new Date(exp * 1000).toLocaleString() : '—'
})

const rows = computed(() => [
  { label: 'Username', value: session.username },
  { label: 'Issuer', value: session.claims?.iss || cfg.issuer || '—' },
  { label: 'Groups', value: (session.claims?.groups ?? []).join(', ') || '—' },
  { label: 'Token expires', value: expiry.value },
])
</script>

<template>
  <div>
    <PageHeader
      title="Settings"
      subtitle="The identity the servers authorize. A Membership must name this username exactly."
    />

    <div class="grid gap-6 lg:grid-cols-2">
      <Panel>
        <template #header>Identity</template>
        <dl class="divide-y divide-edge">
          <div v-for="r in rows" :key="r.label" class="px-4 py-2.5 flex gap-4">
            <dt class="w-32 shrink-0 k-eyebrow pt-0.5">{{ r.label }}</dt>
            <dd class="text-[13px] break-all">{{ r.value }}</dd>
          </div>
        </dl>
      </Panel>

      <Panel>
        <template #header>Tenants</template>
        <div v-if="!session.tenants.length" class="px-4 py-6 text-xs text-muted">
          No tenants.
        </div>
        <ul v-else class="divide-y divide-edge">
          <li v-for="t in session.tenants" :key="t.name" class="px-4 py-2.5">
            <div class="flex items-center gap-3">
              <span class="text-[13px] font-medium">{{ t.displayName || t.name }}</span>
              <RoleBadge :roles="t.roles" />
            </div>
            <p class="mt-0.5 text-[11px] text-faint">
              {{ (t.projects ?? []).length }} project<span v-if="(t.projects ?? []).length !== 1">s</span>
            </p>
          </li>
        </ul>
      </Panel>
    </div>
  </div>
</template>
