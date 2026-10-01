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

// The top of the sidebar is this project's own things; what you could add
// to it, and discovery over it, sit at the bottom.
const nav = computed(() => {
  const items: { to: string; label: string }[] = []
  if (session.admin) items.push({ to: '/admin', label: 'Admin' })
  return items
})

// Anything enabled in this project earns its own entry, because that is
// what enabling was for. One entry per PRODUCER — grouped by the binding
// that enabled it — not per kind it serves: a producer serving three kinds
// is one thing you enabled, and its page lists the three.
const enabledNav = computed(() => {
  const byBinding = new Map<string, number>()
  for (const e of session.enabled) byBinding.set(e.binding, (byBinding.get(e.binding) ?? 0) + 1)
  return [...byBinding].map(([binding, kinds]) => ({
    to: `/p/${binding}`,
    label: binding
      .split(/[.-]/)[0]
      .replace(/^\w/, (c) => c.toUpperCase()),
    hint: kinds === 1 ? '1 kind' : `${kinds} kinds`,
  }))
})

// The bottom of the sidebar is about the place rather than the work done
// in it.
const bottomNav = [
  { to: '/catalog', label: 'Producers' },
  { to: '/api', label: 'API Explorer' },
]

onMounted(async () => {
  if (!session.authenticated) return
  await session.load()
  await session.checkAdmin()

  // Every screen here is relative to a project, so having none is the one
  // state the app cannot show anything useful in — whether that is because
  // the identity belongs to nothing at all, or belongs to a tenant nobody
  // has added a project to. Both land on the wizard, which asks for
  // whichever of the two is missing.
  //
  // Only when the review succeeded: an error, or a refused sign-in, means
  // we do not know what they have, which is not the same as knowing they
  // have nothing.
  const nothingToWorkIn = !session.error && !session.unauthorized && !session.projects.length
  if (nothingToWorkIn && !route.meta.public && route.name !== 'welcome') {
    router.replace({ name: 'welcome' })
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

    <div v-if="session.unauthorized" class="px-7 pt-4">
      <div class="k-note k-note--danger flex items-center gap-3">
        <span>
          The servers did not accept your sign-in. It may have expired — or a
          validator may be unable to reach the identity provider.
        </span>
        <button class="k-btn k-btn--ghost ml-auto shrink-0" @click="logout">Sign in again</button>
      </div>
    </div>

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
            <div class="k-eyebrow px-2.5 pb-1" :class="nav.length > 0 ? 'pt-4' : ''">Producers</div>
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
