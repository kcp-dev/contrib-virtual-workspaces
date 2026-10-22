<template>
  <div class="k-card overflow-hidden">
    <div v-if="$slots.header" class="px-4 py-2.5 border-b border-edge k-eyebrow flex items-center gap-2">
      <slot name="header" />
    </div>
    <slot />
  </div>
</template>
