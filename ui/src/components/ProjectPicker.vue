<script setup lang="ts">
// The UI's `use`: the project every screen is relative to.
//
// Projects are the only selectable thing here. A tenant is a heading, not
// an option — no member can enter a tenant workspace, so offering it as a
// choice would offer somewhere you cannot go. A tenant with no projects
// still appears, with its emptiness stated, because a tenant vanishing
// from the list is worse than a tenant you cannot pick: it is how you know
// there is a project to create. That form is on the Projects page and
// carries its own tenant field, so nothing here needs to be pickable for
// the first project to be made.
import { computed } from 'vue'
import { useSession } from '@/stores/session'

const session = useSession()

const groups = computed(() =>
  session.tenants.map((t) => ({
    tenant: t.displayName || t.name,
    projects: (t.projects ?? []).map((p) => ({
      key: `${t.name}/${p.name}`,
      label: p.displayName || p.name,
    })),
  })),
)

const value = computed({
  get: () => (session.project ? `${session.tenant}/${session.project}` : ''),
  set: (key: string) => {
    const [tenant, project] = key.split('/')
    if (project) session.select(tenant, project)
  },
})
</script>

<template>
  <div v-if="groups.length" class="flex items-center gap-2 min-w-0">
    <span class="k-eyebrow hidden sm:inline">Project</span>
    <select v-model="value" class="k-input !w-auto max-w-64 !py-1" title="Scopes every screen">
      <!-- Nothing selected is a real state: a tenant can have no projects
           yet, and then there is nothing to be in. -->
      <option v-if="!session.project" value="" disabled>Select a project</option>
      <optgroup v-for="g in groups" :key="g.tenant" :label="g.tenant">
        <option v-for="p in g.projects" :key="p.key" :value="p.key">{{ p.label }}</option>
        <option v-if="!g.projects.length" value="" disabled>no projects</option>
      </optgroup>
    </select>
  </div>
  <!-- Nothing to pick yet: point at the thing that fixes that, not at a
       list that would also be empty. -->
  <RouterLink v-else-if="!session.loading" to="/welcome" class="text-xs text-muted hover:text-ink">
    Get started →
  </RouterLink>
</template>
