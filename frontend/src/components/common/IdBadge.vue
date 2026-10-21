<!--
IdBadge.vue marks adjacent monospaced text as an identifier in shared detail and
table displays. Its fixed uppercase label stays separate from the value that
callers copy, and the compact nonselectable badge fits beside identifiers of
varying lengths.
-->
<template>
  <span
    class="shrink-0 select-none rounded-compact border border-gray-200 bg-gray-100 px-1 py-px font-mono text-xs font-semibold uppercase tracking-wide text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-400"
  >
    ID
  </span>
</template>
