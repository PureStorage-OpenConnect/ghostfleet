<script setup lang="ts">
import { computed } from 'vue'

// A dependency-free inline-SVG line chart for a throughput series.
const props = withDefaults(
  defineProps<{ values: number[]; width?: number; height?: number; unit?: string; live?: boolean }>(),
  { width: 220, height: 40, unit: 'MB/s', live: false },
)

const max = computed(() => Math.max(1, ...props.values))

const points = computed(() => {
  const n = props.values.length
  if (n === 0) return ''
  const w = props.width
  const h = props.height
  return props.values
    .map((v, i) => {
      const x = n === 1 ? w : (i / (n - 1)) * w
      const y = h - (v / max.value) * (h - 2) - 1
      return `${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
})

const peak = computed(() => Math.max(0, ...props.values))
const latest = computed(() => props.values[props.values.length - 1] ?? 0)
</script>

<template>
  <div class="flex items-center gap-3">
    <svg :width="width" :height="height" class="overflow-visible">
      <polyline
        :points="points"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        class="text-sky-500"
      />
    </svg>
    <span class="font-mono text-xs whitespace-nowrap text-slate-500">
      <template v-if="live">{{ latest.toFixed(0) }} {{ unit }} · </template>peak {{ peak.toFixed(0) }}
      <template v-if="!live">{{ unit }}</template>
    </span>
  </div>
</template>
