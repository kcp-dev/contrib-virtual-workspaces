<script setup lang="ts">
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import * as oidc from '@/lib/oidc'

const route = useRoute()
const error = ref('')
const busy = ref(false)
const issuer = oidc.config().issuer

async function signIn() {
  busy.value = true
  error.value = ''
  try {
    await oidc.begin((route.query.returnTo as string) || '/')
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    busy.value = false
  }
}
</script>

<template>
  <div class="min-h-screen grid place-items-center px-6">
    <div class="w-full max-w-xs">
      <div class="k-eyebrow mb-2">Console</div>
      <h1 class="font-display text-2xl font-semibold tracking-tight" style="font-stretch: 120%">
        kcp<span class="text-faint"> Platform</span>
      </h1>
      <p class="mt-2 text-xs text-muted leading-relaxed">
        Tenants, projects and the service catalog.
      </p>

      <button class="k-btn k-btn--primary mt-7 w-full !py-2" :disabled="busy || !issuer" @click="signIn">
        {{ busy ? 'Redirecting…' : 'Sign in' }}
      </button>

      <p v-if="!issuer" class="k-note k-note--danger mt-4">
        No issuer configured. Set <code class="font-mono">VITE_OIDC_ISSUER</code>.
      </p>
      <p v-if="error" class="k-note k-note--danger mt-4">{{ error }}</p>

      <p class="mt-7 font-mono text-[10px] text-faint truncate" :title="issuer">
        {{ issuer || 'no issuer' }}
      </p>
    </div>
  </div>
</template>
