<script setup lang="ts">
// What is being built, drawn: a tenant with a project inside it, and you
// reaching the project rather than the tenant.
//
// The nesting is the one thing about this model that is not obvious from
// the words — people reasonably expect to work "in" an account — so the
// picture earns its place by showing the part the form cannot: that the
// outer box is a record store nobody enters, and the arrow stops at the
// inner one.
const props = defineProps<{
  /** Which step is live; earlier parts are drawn as done. */
  step: 'tenant' | 'project' | 'done'
  tenantName?: string
  projectName?: string
}>()

const tenantDone = () => props.step !== 'tenant'
const projectDone = () => props.step === 'done'
</script>

<template>
  <svg viewBox="0 0 260 190" class="w-full max-w-[260px]" role="img"
       :aria-label="`tenant ${tenantName || ''} containing project ${projectName || ''}`">
    <!-- Tenant: the outer record store. Dashed while it does not exist. -->
    <rect
      x="14" y="22" width="232" height="120" rx="6"
      :fill="tenantDone() ? 'var(--color-accent-subtle)' : 'transparent'"
      :stroke="tenantDone() ? 'var(--color-accent)' : 'var(--color-edge-strong)'"
      :stroke-dasharray="tenantDone() ? '0' : '4 3'"
      stroke-width="1.5"
    />
    <text x="26" y="40" class="lbl" :class="tenantDone() && 'lbl--on'">TENANT</text>
    <text v-if="tenantName" x="26" y="54" class="val">{{ tenantName }}</text>

    <!-- Project: the workspace you actually enter. -->
    <rect
      x="34" y="70" width="192" height="52" rx="6"
      :fill="projectDone() ? 'var(--color-raised)' : 'transparent'"
      :stroke="projectDone() ? 'var(--color-accent)' : 'var(--color-edge-strong)'"
      :stroke-dasharray="projectDone() ? '0' : '4 3'"
      stroke-width="1.5"
    />
    <text x="46" y="90" class="lbl" :class="projectDone() && 'lbl--on'">PROJECT</text>
    <text v-if="projectName && projectDone()" x="46" y="104" class="val">{{ projectName }}</text>
    <text v-else x="46" y="104" class="hint">a kcp workspace</text>

    <!-- You, reaching the project and not the tenant: the whole point. -->
    <text x="14" y="176" class="lbl" :class="projectDone() && 'lbl--on'">YOU</text>
    <path
      d="M46 171 H 150 Q 160 171 160 161 V 126"
      fill="none"
      :stroke="projectDone() ? 'var(--color-accent)' : 'var(--color-edge-strong)'"
      :stroke-dasharray="projectDone() ? '0' : '4 3'"
      stroke-width="1.5"
    />
    <path
      d="M156 128 L160 122 L164 128"
      fill="none"
      :stroke="projectDone() ? 'var(--color-accent)' : 'var(--color-edge-strong)'"
      stroke-width="1.5"
    />
  </svg>
</template>

<style scoped>
.lbl {
  font-family: 'IBM Plex Mono', ui-monospace, monospace;
  font-size: 8px;
  letter-spacing: 0.08em;
  fill: var(--color-faint);
}
.lbl--on {
  fill: var(--color-accent);
}
.val {
  font-family: 'IBM Plex Mono', ui-monospace, monospace;
  font-size: 10px;
  fill: var(--color-ink);
}
.hint {
  font-family: 'IBM Plex Mono', ui-monospace, monospace;
  font-size: 9px;
  fill: var(--color-faint);
}
</style>
