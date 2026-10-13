<script setup lang="ts">
// An editor for one object: a form for the parts of the schema that have
// an obvious control, and YAML for everything else and for anything the
// form declines to guess at.
//
// The form is not a replacement for the YAML. A form rendered from JSON
// Schema has to hold opinions about oneOf, arrays and every
// x-kubernetes-* extension, and would hold them wrongly for producers
// nobody has seen. So it covers the scalar fields, names what it is not
// covering, and the YAML tab is always there and always authoritative for
// the shape as a whole.
import { computed, ref, watch } from 'vue'
import { parse, stringify } from 'yaml'
import type { OpenAPISchema } from '@/lib/api'
import { setValueAt, shapeOf, valueAt, type FormField } from '@/lib/form'

const props = defineProps<{
  /** The object to edit; changing it reloads the buffers. */
  object: Record<string, unknown>
  title: string
  busy?: boolean
  /**
   * A create may carry none of the server's bookkeeping; an update must
   * carry some of it. What is hidden differs accordingly.
   */
  mode: 'create' | 'edit'
  /** The spec's schema, when known. Without it this is YAML only. */
  schema?: OpenAPISchema | null
}>()

const emit = defineEmits<{
  save: [object: Record<string, unknown>]
  cancel: []
}>()

// What the server recomputes on every write, and which no one can
// usefully edit. Safe to hide in both directions.
const NOISE = ['generation', 'creationTimestamp', 'managedFields', 'selfLink']

// Additionally meaningless on a create — the object does not exist yet, so
// it has no version, identity or owner.
const SERVER_OWNED = ['uid', 'resourceVersion', 'ownerReferences']

/**
 * forEditing hides what cannot be edited, and nothing else.
 *
 * An update keeps `resourceVersion`, because it is not noise: it is the
 * optimistic-concurrency token the API requires, and sending a stale one
 * is how a write that would clobber someone else's gets refused instead.
 * It keeps `ownerReferences` too — dropping those from an update deletes
 * them, which detaches the object from its owner and defeats garbage
 * collection — and `status`, because a resource whose CRD declares no
 * status subresource stores status here, where omitting it would wipe it.
 */
function forEditing(o: Record<string, unknown>): Record<string, unknown> {
  const copy = JSON.parse(JSON.stringify(o)) as Record<string, unknown>
  if (props.mode === 'create') delete copy.status
  const meta = copy.metadata as Record<string, unknown> | undefined
  if (meta) {
    const hide = props.mode === 'create' ? [...NOISE, ...SERVER_OWNED] : NOISE
    for (const k of hide) delete meta[k]
  }
  return copy
}

const shape = computed(() => shapeOf(props.schema ?? null))
const hasForm = computed(() => shape.value.fields.length > 0)

const tab = ref<'form' | 'yaml'>('form')
// One object is the state the form edits; the YAML tab keeps its own text
// buffer, because re-serializing under a cursor on every keystroke would
// fight whoever is typing. The two are synced when the tab changes, and
// whichever tab is open when Apply is pressed is the one that is read.
const draft = ref<Record<string, unknown>>({})
const text = ref('')
const parseError = ref('')

watch(
  [() => props.object, () => props.mode],
  ([o]) => {
    draft.value = forEditing(o)
    text.value = stringify(draft.value)
    parseError.value = ''
    tab.value = hasForm.value ? 'form' : 'yaml'
  },
  { immediate: true },
)

function show(next: 'form' | 'yaml') {
  if (next === tab.value) return
  if (next === 'yaml') {
    // Leaving the form: render what it has built.
    text.value = stringify(draft.value)
    parseError.value = ''
  } else {
    // Leaving the YAML: adopt it, or refuse to switch and say why.
    try {
      const parsed = parse(text.value)
      if (!parsed || typeof parsed !== 'object') {
        parseError.value = 'That is not an object.'
        return
      }
      draft.value = parsed as Record<string, unknown>
      parseError.value = ''
    } catch (e) {
      parseError.value = e instanceof Error ? e.message : String(e)
      return
    }
  }
  tab.value = next
}

/** name is its own field: every object has one and it is always required. */
const name = computed({
  get: () => String(valueAt(draft.value, ['metadata', 'name']) ?? ''),
  set: (v: string) => setValueAt(draft.value, ['metadata', 'name'], v),
})

function fieldValue(field: FormField): unknown {
  const v = valueAt(draft.value, field.path)
  if (v !== undefined) return v
  return field.type === 'boolean' ? false : ''
}

function setField(field: FormField, raw: unknown) {
  let value = raw
  if (field.type === 'integer' || field.type === 'number') {
    value = raw === '' || raw === null ? '' : Number(raw)
    if (typeof value === 'number' && Number.isNaN(value)) return
  }
  setValueAt(draft.value, field.path, value)
}

function save() {
  if (tab.value === 'yaml') {
    try {
      const parsed = parse(text.value)
      if (!parsed || typeof parsed !== 'object') {
        parseError.value = 'That is not an object.'
        return
      }
      emit('save', parsed as Record<string, unknown>)
      return
    } catch (e) {
      parseError.value = e instanceof Error ? e.message : String(e)
      return
    }
  }
  emit('save', JSON.parse(JSON.stringify(draft.value)))
}
</script>

<template>
  <div class="k-card">
    <div class="px-4 py-2.5 border-b border-edge flex items-center gap-3">
      <span class="text-[13px] font-medium truncate">{{ title }}</span>
      <div class="ml-auto flex gap-1 shrink-0">
        <button
          v-if="hasForm"
          class="k-nav-link !py-1 !px-2 text-[11px]"
          :class="tab === 'form' && '!bg-accent-subtle !text-accent-hover'"
          @click="show('form')"
        >
          Form
        </button>
        <button
          class="k-nav-link !py-1 !px-2 text-[11px]"
          :class="tab === 'yaml' && '!bg-accent-subtle !text-accent-hover'"
          @click="show('yaml')"
        >
          YAML
        </button>
      </div>
    </div>

    <!-- Form -->
    <div v-if="tab === 'form'" class="px-4 py-3 space-y-3.5 max-h-[34rem] overflow-auto">
      <div>
        <label class="k-eyebrow block mb-1">
          name <span class="text-danger">*</span>
        </label>
        <input v-model="name" class="k-input" :disabled="mode === 'edit'" placeholder="a DNS name" />
        <p class="mt-1 text-[11px] text-faint">
          {{ mode === 'edit' ? "A name cannot change; it is the object's identity." : 'Lowercase letters, digits and dashes.' }}
        </p>
      </div>

      <div v-for="field in shape.fields" :key="field.label">
        <label class="k-eyebrow block mb-1">
          {{ field.label }}
          <span v-if="field.required" class="text-danger">*</span>
        </label>

        <select
          v-if="field.type === 'enum'"
          class="k-input"
          :value="fieldValue(field)"
          @change="setField(field, ($event.target as HTMLSelectElement).value)"
        >
          <option value="">—</option>
          <option v-for="o in field.options" :key="o" :value="o">{{ o }}</option>
        </select>

        <label v-else-if="field.type === 'boolean'" class="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            :checked="fieldValue(field) === true"
            @change="setField(field, ($event.target as HTMLInputElement).checked)"
          />
          <span class="text-muted">{{ fieldValue(field) === true ? 'true' : 'false' }}</span>
        </label>

        <input
          v-else
          class="k-input"
          :type="field.type === 'string' ? 'text' : 'number'"
          :value="fieldValue(field)"
          :placeholder="field.default !== undefined ? `default: ${field.default}` : ''"
          @input="setField(field, ($event.target as HTMLInputElement).value)"
        />

        <p v-if="field.description" class="mt-1 text-[11px] text-faint leading-relaxed">
          {{ field.description }}
        </p>
      </div>

      <!-- Said out loud, so a missing control reads as a decision rather
           than as something lost. -->
      <p v-if="shape.complex.length" class="k-note">
        <span class="font-medium">{{ shape.complex.join(', ') }}</span>
        {{ shape.complex.length === 1 ? 'is a list or a free-form object' : 'are lists or free-form objects' }},
        which the form does not guess at — edit {{ shape.complex.length === 1 ? 'it' : 'them' }} in YAML.
      </p>

      <p v-if="!shape.fields.length" class="text-[11px] text-faint">
        This resource's schema has no simple fields. Use the YAML tab.
      </p>
    </div>

    <!-- YAML -->
    <textarea
      v-else
      v-model="text"
      spellcheck="false"
      class="w-full font-mono text-[11px] leading-relaxed px-4 py-3 bg-transparent resize-y outline-none min-h-[22rem]"
      @keydown.ctrl.enter="save"
      @keydown.meta.enter="save"
    />

    <p v-if="parseError" class="k-note k-note--danger mx-4 mb-3">{{ parseError }}</p>

    <div class="px-4 py-2.5 border-t border-edge flex items-center gap-2">
      <button class="k-btn k-btn--primary" :disabled="busy" @click="save">
        {{ busy ? 'Applying…' : 'Apply' }}
      </button>
      <button class="k-btn k-btn--ghost" :disabled="busy" @click="emit('cancel')">Cancel</button>
      <span v-if="tab === 'yaml'" class="ml-auto text-[10px] text-faint">⌘/Ctrl + Enter applies</span>
    </div>
  </div>
</template>
