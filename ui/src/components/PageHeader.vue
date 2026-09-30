<script setup lang="ts">
defineProps<{ title: string; subtitle?: string; eyebrow?: string }>()
</script>

<template>
  <div class="mb-5 flex items-start justify-between gap-6">
    <div class="min-w-0">
      <div v-if="eyebrow" class="k-eyebrow mb-1">{{ eyebrow }}</div>
      <h1 class="font-display text-lg font-semibold tracking-tight" style="font-stretch: 115%">
        {{ title }}
      </h1>
      <p v-if="subtitle" class="mt-1 text-xs text-muted max-w-2xl leading-relaxed">{{ subtitle }}</p>
    </div>
    <div class="shrink-0 pt-1"><slot name="actions" /></div>
  </div>
</template>
