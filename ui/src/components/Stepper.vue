<script setup lang="ts">
// A left-to-right stepper: where you are, what is behind you, what is
// left. Horizontal because it is answering "how much more of this is
// there", and a column of numbers answers that worse than a line does.
const props = defineProps<{
  steps: { key: string; label: string }[]
  current: string
}>()

const index = () => props.steps.findIndex((s) => s.key === props.current)
const state = (i: number): 'done' | 'current' | 'todo' => {
  const at = index()
  return i < at ? 'done' : i === at ? 'current' : 'todo'
}
</script>

<template>
  <ol class="flex items-center gap-0">
    <li v-for="(s, i) in steps" :key="s.key" class="flex items-center" :class="i < steps.length - 1 && 'flex-1'">
      <div class="flex items-center gap-2 shrink-0">
        <span class="k-step" :class="`k-step--${state(i)}`">
          <svg v-if="state(i) === 'done'" viewBox="0 0 12 12" class="w-3 h-3" aria-hidden="true">
            <path d="M2.5 6.2 L4.8 8.5 L9.5 3.8" fill="none" stroke="currentColor"
                  stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" />
          </svg>
          <template v-else>{{ i + 1 }}</template>
        </span>
        <span
          class="text-[11px] whitespace-nowrap"
          :class="state(i) === 'todo' ? 'text-faint' : 'text-ink font-medium'"
        >
          {{ s.label }}
        </span>
      </div>
      <!-- The connector carries the progress, so it fills behind you. -->
      <span
        v-if="i < steps.length - 1"
        class="mx-3 h-px flex-1 min-w-6"
        :class="state(i) === 'done' ? 'bg-accent' : 'bg-edge-strong'"
      />
    </li>
  </ol>
</template>
