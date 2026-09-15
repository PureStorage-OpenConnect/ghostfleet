<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// A filterable multi-select combobox bound via v-model to a string[]. Type to
// filter the option list; click an option (or Enter) to toggle it. Selected
// values are shown as removable chips above the input. Sibling of
// SearchableSelect, which is the single-select equivalent.
const props = withDefaults(
  defineProps<{
    modelValue: string[]
    options: string[]
    placeholder?: string
    // Optional secondary text per option (keyed by value), shown dimmed in the
    // dropdown and on the selected chips — e.g. datastore free space.
    hints?: Record<string, string>
  }>(),
  { placeholder: 'Select one or more…' },
)
const emit = defineEmits<{ 'update:modelValue': [value: string[]] }>()

const open = ref(false)
const query = ref('')
const highlight = ref(0)

const filtered = computed(() => {
  const q = query.value.toLowerCase()
  return props.options.filter((o) => o.toLowerCase().includes(q))
})

watch(open, (isOpen) => {
  if (isOpen) {
    query.value = ''
    highlight.value = 0
  }
})

function toggle(value: string) {
  const set = new Set(props.modelValue)
  if (set.has(value)) set.delete(value)
  else set.add(value)
  // Preserve option order so distribution reads predictably.
  emit(
    'update:modelValue',
    props.options.filter((o) => set.has(o)),
  )
}

function remove(value: string) {
  emit(
    'update:modelValue',
    props.modelValue.filter((v) => v !== value),
  )
}

function onInput(e: Event) {
  query.value = (e.target as HTMLInputElement).value
  open.value = true
  highlight.value = 0
}

function onEnter() {
  if (!open.value) return
  const choice = filtered.value[highlight.value]
  if (choice !== undefined) toggle(choice)
}

function move(delta: number) {
  if (!open.value) {
    open.value = true
    return
  }
  const n = filtered.value.length
  if (n === 0) return
  highlight.value = (highlight.value + delta + n) % n
}

function onBlur() {
  setTimeout(() => {
    open.value = false
  }, 150)
}

const fieldClass =
  'w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none'
</script>

<template>
  <div class="relative">
    <div v-if="modelValue.length" class="mb-1.5 flex flex-wrap gap-1">
      <span
        v-for="v in modelValue"
        :key="v"
        class="inline-flex items-center gap-1 rounded-md bg-slate-100 px-2 py-0.5 text-xs text-slate-700"
      >
        {{ v }}
        <span v-if="hints?.[v]" class="text-slate-400">· {{ hints[v] }}</span>
        <button
          type="button"
          class="text-slate-400 hover:text-slate-700"
          @mousedown.prevent="remove(v)"
          aria-label="Remove"
        >
          ×
        </button>
      </span>
    </div>
    <input
      :class="fieldClass"
      :value="open ? query : ''"
      :placeholder="modelValue.length ? 'Add another…' : placeholder"
      @focus="open = true"
      @blur="onBlur"
      @input="onInput"
      @keydown.down.prevent="move(1)"
      @keydown.up.prevent="move(-1)"
      @keydown.enter.prevent="onEnter"
      @keydown.esc="open = false"
    />
    <div
      v-if="open"
      class="absolute z-10 mt-1 max-h-56 w-full overflow-auto rounded-lg border border-slate-200 bg-white py-1 shadow-lg"
    >
      <button
        v-for="(o, i) in filtered"
        :key="o"
        type="button"
        class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-slate-100"
        :class="{ 'bg-slate-100': i === highlight }"
        @mousedown.prevent="toggle(o)"
      >
        <span class="w-3 text-slate-600">{{ modelValue.includes(o) ? '✓' : '' }}</span>
        <span :class="{ 'font-medium': modelValue.includes(o) }">{{ o }}</span>
        <span v-if="hints?.[o]" class="ml-auto pl-3 text-xs text-slate-400">{{ hints[o] }}</span>
      </button>
      <p v-if="filtered.length === 0" class="px-3 py-1.5 text-sm text-slate-400">No matches</p>
    </div>
  </div>
</template>
