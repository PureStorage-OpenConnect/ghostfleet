<script setup lang="ts">
// The GhostFleet mark: a ghost rising from a stacked storage cylinder, on a
// dark tile. One symbol for the header, the dashboard and the favicon.
withDefaults(defineProps<{ size?: number }>(), { size: 28 })
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 128 128"
    xmlns="http://www.w3.org/2000/svg"
    role="img"
    aria-label="GhostFleet logo"
  >
    <rect width="128" height="128" rx="28" fill="#0b1018" />
    <g>
      <path d="M22 86 L22 110 A42 10 0 0 0 106 110 L106 86 Z" fill="#2ec5e6" />
      <ellipse cx="64" cy="86" rx="42" ry="10" fill="#2ec5e6" />
      <path d="M22 86 A42 10 0 0 0 106 86" fill="none" stroke="#0b1018" stroke-width="2.5" />
      <path d="M22 97 A42 10 0 0 0 106 97" fill="none" stroke="#0b1018" stroke-width="2.5" />
      <path d="M22 108 A42 10 0 0 0 106 108" fill="none" stroke="#0b1018" stroke-width="2.5" />
    </g>
    <path
      d="M46 44 L46 38 L54 24 L74 24 L82 38 L82 68 L77 80 L70 70 L64 82 L58 70 L51 80 L46 68 Z"
      fill="#2ec5e6"
    />
    <polygon points="51,45 60,48 60,52 51,50" fill="#0b1018" />
    <polygon points="77,45 68,48 68,52 77,50" fill="#0b1018" />
  </svg>
</template>
