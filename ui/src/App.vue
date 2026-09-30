<script setup lang="ts">
// The frame: a thin top bar carrying the one thing that scopes everything
// else — which project you are working in — and a sidebar for the places
// inside it. Tenants are not a working context, so they are a page up
// here rather than a second picker.
import { computed, onMounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useSession } from '@/stores/session'
import ProjectPicker from '@/components/ProjectPicker.vue'

const session = useSession()
const route = useRoute()
const router = useRouter()

const chrome = computed(() => !route.meta.public)

// The tenant section has its own sidebar. A tenant is administered rather
// than worked in, so the things you do to one — see what it holds, decide
// who may reach it — are not the things you do inside a project, and
// mixing them into one menu would suggest they are.
const inTenants = computed(() => String(route.name ?? '').startsWith('tenant'))
const tenantName = computed(() => String(route.params.name ?? ''))

const tenantNav = computed(() =>
  tenantName.value
    ? [
        { to: `/tenants/${tenantName.value}`, label: 'Overview' },
        { to: `/tenants/${tenantName.value}/access`, label: 'Access' },
      ]
    : [],
)

const nav = computed(() => {
  const items = [{ to: '/catalog', label: 'Service Catalog' }]
  if (session.admin) items.push({ to: '/admin', label: 'Admin' })
  return items
})

// Anything enabled in this project earns its own entry, because that is
// what enabling was for: the offering becomes an ordinary API here, and
// should be one click away rather than something to find in discovery.
const enabledNav = computed(() =>
  session.enabled.map((e) => ({
    to: `/r/${e.group || '-'}/${e.resource}`,
    label: e.resource.charAt(0).toUpperCase() + e.resource.slice(1),
    hint: e.group || 'core',
  })),
)

// The bottom of the sidebar is about the place rather than the work done
// in it.
const bottomNav = [{ to: '/api', label: 'API Explorer' }]

onMounted(async () => {
  if (session.authenticated) {
    await session.load()
    await session.checkAdmin()
  }
})

watch(() => session.tenant, () => session.checkAdmin())

function logout() {
  session.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <div v-if="!chrome" class="min-h-full">
    <RouterView />
  </div>

  <div v-else class="min-h-full flex flex-col">
    <!-- Top: the working project, and identity. Both are context rather
         than navigation, which is why they are not in the side. -->
    <header class="h-12 shrink-0 border-b border-edge bg-raised">
      <div class="h-full px-4 flex items-center gap-4">
        <RouterLink
          to="/"
          class="font-display font-semibold tracking-tight text-sm shrink-0 mr-1"
          style="font-stretch: 120%"
        >
          kcp<span class="text-faint"> Platform</span>
        </RouterLink>

        <ProjectPicker />

        <div class="ml-auto flex items-center gap-1">
          <RouterLink to="/tenants" class="k-nav-link !py-1.5">Tenants</RouterLink>
          <RouterLink to="/profile" class="k-nav-link !py-1.5">Settings</RouterLink>
          <button class="k-nav-link !py-1.5" @click="logout">Sign out</button>
        </div>
      </div>
    </header>

    <div class="flex-1 flex min-h-0">
      <!-- Side: in the tenant section, what you can do to a tenant;
           everywhere else, the places inside the selected project. -->
      <aside class="w-52 shrink-0 border-r border-edge bg-raised/60 flex flex-col">
        <nav v-if="inTenants" class="p-2 flex-1 overflow-auto space-y-0.5">
          <RouterLink to="/tenants" class="k-nav-link">All tenants</RouterLink>

          <template v-if="tenantNav.length">
            <div class="k-eyebrow px-2.5 pt-4 pb-1 truncate" :title="tenantName">{{ tenantName }}</div>
            <RouterLink v-for="item in tenantNav" :key="item.to" :to="item.to" class="k-nav-link">
              {{ item.label }}
            </RouterLink>
          </template>
        </nav>

        <nav v-else class="p-2 flex-1 overflow-auto space-y-0.5">
          <RouterLink v-for="item in nav" :key="item.to" :to="item.to" class="k-nav-link">
            {{ item.label }}
          </RouterLink>

          <template v-if="enabledNav.length">
            <div class="k-eyebrow px-2.5 pt-4 pb-1">Providers</div>
            <RouterLink v-for="item in enabledNav" :key="item.to" :to="item.to" class="k-nav-link">
              <span class="truncate">{{ item.label }}</span>
              <span class="ml-auto font-mono text-[9px] text-faint truncate max-w-20">{{ item.hint }}</span>
            </RouterLink>
          </template>
        </nav>

        <nav v-if="!inTenants" class="p-2 border-t border-edge space-y-0.5">
          <RouterLink v-for="item in bottomNav" :key="item.to" :to="item.to" class="k-nav-link">
            {{ item.label }}
          </RouterLink>
        </nav>

        <div class="px-3 py-2.5 border-t border-edge">
          <div class="font-mono text-[10px] text-faint truncate" :title="session.username">
            {{ session.username }}
          </div>
        </div>
      </aside>

      <main class="flex-1 min-w-0 overflow-auto">
        <div class="px-7 py-6 max-w-5xl">
          <RouterView />
        </div>
      </main>
    </div>
  </div>
</template>
