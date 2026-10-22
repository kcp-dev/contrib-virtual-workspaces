<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import * as oidc from '@/lib/oidc'
import { useSession } from '@/stores/session'

const router = useRouter()
const session = useSession()
const error = ref('')

onMounted(async () => {
  try {
    await oidc.complete(window.location.search)
    session.claims = oidc.claims()
    await session.load()
    await session.checkAdmin()
    router.replace(oidc.returnTo())
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
})
</script>

<template>
  <div class="min-h-screen grid place-items-center px-6">
    <div v-if="!error" class="text-xs text-muted">Signing you in…</div>
    <div v-else class="w-full max-w-sm">
      <p class="k-note k-note--danger">{{ error }}</p>
      <RouterLink to="/login" class="mt-4 inline-block text-xs text-accent">Try again</RouterLink>
    </div>
  </div>
</template>
