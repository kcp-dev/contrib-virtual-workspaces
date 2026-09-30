<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ roles?: string[] }>()
const shown = computed(() => (props.roles?.length ? props.roles : []))
const tone = (r: string) =>
  r === 'admin' ? 'k-badge--warning' : r === 'edit' ? 'k-badge--accent' : 'k-badge--muted'
</script>

<template>
  <span class="inline-flex gap-1.5">
    <span v-if="!shown.length" class="text-xs text-faint">—</span>
    <span v-for="r in shown" :key="r" class="k-badge" :class="tone(r)">{{ r }}</span>
  </span>
</template>
