<script setup lang="ts">
import { ref } from 'vue'
import { useRoute } from 'vue-router'
import * as oidc from '@/lib/oidc'

const route = useRoute()
const error = ref('')
// Set when the browser could not reach the issuer at all, which in a
// development stack almost always means one thing — see below.
const untrusted = ref(false)
const busy = ref(false)
const issuer = oidc.config().issuer

async function signIn() {
  busy.value = true
  error.value = ''
  untrusted.value = false
  try {
    await oidc.begin((route.query.returnTo as string) || '/')
  } catch (e) {
    // A fetch that fails before any response is a network-level refusal,
    // and the browser deliberately does not say why. With a self-signed
    // development certificate it is the certificate: the browser will not
    // talk to the issuer until someone has visited it once and accepted
    // the warning, and a regenerated certificate invalidates that
    // acceptance. Saying so beats showing "Failed to fetch".
    untrusted.value = e instanceof TypeError
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
        Tenants, projects and producers.
      </p>

      <button class="k-btn k-btn--primary mt-7 w-full !py-2" :disabled="busy || !issuer" @click="signIn">
        {{ busy ? 'Redirecting…' : 'Sign in' }}
      </button>

      <p v-if="!issuer" class="k-note k-note--danger mt-4">
        No issuer configured. Set <code class="font-mono">VITE_OIDC_ISSUER</code>.
      </p>

      <div v-else-if="untrusted" class="k-note k-note--danger mt-4 space-y-2">
        <p>
          Your browser would not connect to the identity provider. In a
          development stack it is serving a self-signed certificate, which has
          to be accepted once — and accepting it again after it is regenerated.
        </p>
        <p>
          <a :href="issuer" target="_blank" rel="noreferrer" class="underline">
            Open it in a new tab
          </a>
          , accept the warning, then sign in again.
        </p>
      </div>

      <p v-else-if="error" class="k-note k-note--danger mt-4">{{ error }}</p>
    </div>
  </div>
</template>
