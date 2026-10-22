<script setup lang="ts">
// Renders a producer's instructions: one tab per section, each a column
// of blocks. Blocks become elements, never HTML — that is the whole of why
// a producer's text is safe to show.
import { computed, ref, watch } from 'vue'
import { render, sections, type Block, type Variables } from '@/lib/instructions'

const props = defineProps<{
  instructions: Record<string, string> | undefined
  vars: Variables
}>()

const tabs = computed(() => sections(props.instructions))
const active = ref('')
watch(tabs, (t) => {
  if (!t.find((s) => s.key === active.value)) active.value = t[0]?.key ?? ''
}, { immediate: true })

const blocks = computed<Block[]>(() => {
  const s = tabs.value.find((t) => t.key === active.value)
  return s ? render(s.text, props.vars) : []
})

// Copy buttons on code blocks: the filled-in snippet is meant to be run,
// so make getting it out one click.
const copied = ref(-1)
async function copy(text: string, i: number) {
  try {
    await navigator.clipboard.writeText(text)
    copied.value = i
    setTimeout(() => (copied.value = -1), 1500)
  } catch {
    // Clipboard access can be refused; the text is still selectable.
  }
}
</script>

<template>
  <div v-if="tabs.length">
    <div v-if="tabs.length > 1" class="px-4 pt-3 flex flex-wrap gap-1 border-b border-edge pb-2">
      <button
        v-for="t in tabs"
        :key="t.key"
        class="k-nav-link !py-1 !px-2.5 text-[11px]"
        :class="t.key === active && '!bg-accent-subtle !text-accent-hover'"
        @click="active = t.key"
      >
        {{ t.label }}
      </button>
    </div>

    <div class="px-4 py-3.5 space-y-3 text-[12px] leading-relaxed">
      <template v-for="(b, i) in blocks" :key="i">
        <h2 v-if="b.kind === 'heading' && b.level === 1" class="font-display text-base font-semibold tracking-tight">
          <template v-for="(s, j) in b.spans" :key="j"><span v-if="s.kind === 'text'">{{ s.text }}</span><code v-else-if="s.kind === 'code'" class="font-mono text-[11px] bg-overlay px-1 rounded-[3px]">{{ s.text }}</code><strong v-else-if="s.kind === 'strong'">{{ s.text }}</strong><a v-else :href="s.href" target="_blank" rel="noreferrer" class="text-accent underline">{{ s.text }}</a></template>
        </h2>
        <h3 v-else-if="b.kind === 'heading'" class="font-medium text-[13px]">
          <template v-for="(s, j) in b.spans" :key="j"><span v-if="s.kind === 'text'">{{ s.text }}</span><code v-else-if="s.kind === 'code'" class="font-mono text-[11px] bg-overlay px-1 rounded-[3px]">{{ s.text }}</code><strong v-else-if="s.kind === 'strong'">{{ s.text }}</strong><a v-else :href="s.href" target="_blank" rel="noreferrer" class="text-accent underline">{{ s.text }}</a></template>
        </h3>

        <p v-else-if="b.kind === 'paragraph'" class="text-muted">
          <template v-for="(s, j) in b.spans" :key="j"><span v-if="s.kind === 'text'">{{ s.text }}</span><code v-else-if="s.kind === 'code'" class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">{{ s.text }}</code><strong v-else-if="s.kind === 'strong'" class="text-ink">{{ s.text }}</strong><a v-else :href="s.href" target="_blank" rel="noreferrer" class="text-accent underline">{{ s.text }}</a></template>
        </p>

        <ul v-else-if="b.kind === 'list'" class="list-disc pl-5 space-y-1 text-muted">
          <li v-for="(item, k) in b.items" :key="k">
            <template v-for="(s, j) in item" :key="j"><span v-if="s.kind === 'text'">{{ s.text }}</span><code v-else-if="s.kind === 'code'" class="font-mono text-[11px] text-ink bg-overlay px-1 rounded-[3px]">{{ s.text }}</code><strong v-else-if="s.kind === 'strong'" class="text-ink">{{ s.text }}</strong><a v-else :href="s.href" target="_blank" rel="noreferrer" class="text-accent underline">{{ s.text }}</a></template>
          </li>
        </ul>

        <div v-else-if="b.kind === 'code'" class="relative group">
          <pre class="font-mono text-[11px] leading-relaxed bg-overlay border border-edge rounded-[4px] px-3 py-2.5 overflow-auto whitespace-pre">{{ b.text }}</pre>
          <div class="absolute top-1.5 right-1.5 flex items-center gap-1.5">
            <span v-if="b.lang" class="k-badge k-badge--muted">{{ b.lang }}</span>
            <button class="k-btn k-btn--ghost !px-2 !py-0.5 text-[10px]" @click="copy(b.text, i)">
              {{ copied === i ? 'Copied' : 'Copy' }}
            </button>
          </div>
        </div>
      </template>
    </div>
  </div>
</template>
