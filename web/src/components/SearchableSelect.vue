<script setup lang="ts">
import { computed, ref, watch } from 'vue'

// A filterable single-select combobox bound via v-model. Type to filter the
// option list; click or Enter to choose. With allowCustom, a typed value
// that matches no option is accepted as-is (used for the VM folder, where a
// not-yet-created path is valid).
//
// Options are plain strings, or {value, label} pairs when the stored value
// differs from what the user should see and search for (used for timezones,
// where "Europe/Berlin" is listed as "Europe/Berlin (UTC+02:00)").
type Option = string | { value: string; label: string }

const props = withDefaults(
  defineProps<{
    modelValue: string
    options: Option[]
    placeholder?: string
    // Label for an explicit "no selection" entry (e.g. "Cluster default").
    emptyLabel?: string
    allowCustom?: boolean
  }>(),
  { placeholder: 'Select…', emptyLabel: '', allowCustom: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()

const open = ref(false)
const query = ref('')
const highlight = ref(0)

const normalized = computed(() =>
  props.options.map((o) => (typeof o === 'string' ? { value: o, label: o } : o)),
)

// Label of the current selection, falling back to the raw value so a custom
// or unknown value still shows.
const selectedLabel = computed(
  () => normalized.value.find((o) => o.value === props.modelValue)?.label ?? props.modelValue,
)

// While closed the field shows the selected option; while open it shows the
// live filter query.
const display = computed(() => (open.value ? query.value : selectedLabel.value))

const filtered = computed(() => {
  const q = query.value.toLowerCase()
  return normalized.value.filter(
    (o) => o.label.toLowerCase().includes(q) || o.value.toLowerCase().includes(q),
  )
})

watch(open, (isOpen) => {
  if (isOpen) {
    query.value = ''
    highlight.value = 0
  }
})

function choose(value: string) {
  emit('update:modelValue', value)
  open.value = false
}

function onInput(e: Event) {
  query.value = (e.target as HTMLInputElement).value
  open.value = true
  highlight.value = 0
  if (props.allowCustom) emit('update:modelValue', query.value)
}

function onEnter() {
  if (!open.value) return
  const choice = filtered.value[highlight.value]
  if (choice !== undefined) choose(choice.value)
  else if (props.allowCustom) open.value = false
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

// Close when focus leaves the component.
function onBlur() {
  // Delay so a click on an option registers first.
  setTimeout(() => {
    open.value = false
  }, 150)
}

const fieldClass =
  'w-full rounded-lg border border-slate-300 px-3 py-1.5 text-sm focus:border-slate-500 focus:outline-none'
</script>

<template>
  <div class="relative">
    <input
      :class="fieldClass"
      :value="display"
      :placeholder="placeholder"
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
        v-if="emptyLabel"
        type="button"
        class="block w-full px-3 py-1.5 text-left text-sm text-slate-500 hover:bg-slate-100"
        @mousedown.prevent="choose('')"
      >
        {{ emptyLabel }}
      </button>
      <button
        v-for="(o, i) in filtered"
        :key="o.value"
        type="button"
        class="block w-full px-3 py-1.5 text-left text-sm hover:bg-slate-100"
        :class="{ 'bg-slate-100': i === highlight, 'font-medium': o.value === modelValue }"
        @mousedown.prevent="choose(o.value)"
      >
        {{ o.label }}
      </button>
      <p v-if="filtered.length === 0 && !emptyLabel" class="px-3 py-1.5 text-sm text-slate-400">
        {{ allowCustom ? 'Enter to use typed value' : 'No matches' }}
      </p>
    </div>
  </div>
</template>
