<script setup lang="ts">
defineProps<{
  columns: { key: string; label: string; width?: string }[]
  rows: Record<string, any>[]
}>()
defineEmits<{ rowClick: [row: Record<string, any>] }>()
</script>

<template>
  <div class="os-table-wrap">
    <table class="os-table">
      <thead>
        <tr>
          <th v-for="c in columns" :key="c.key" :style="c.width ? { width: c.width } : {}">{{ c.label }}</th>
          <th class="ops"><slot name="ops-header" /></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, i) in rows" :key="i" @click="$emit('rowClick', row)">
          <td v-for="c in columns" :key="c.key">{{ row[c.key] }}</td>
          <td class="ops"><slot name="ops" :row="row" :index="i" /></td>
        </tr>
        <tr v-if="!rows.length"><td :colspan="columns.length + 1" class="empty">暂无数据</td></tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
.os-table-wrap { overflow: auto; border: 1px solid var(--os-border); border-radius: 10px; }
.os-table { width: 100%; border-collapse: collapse; font-size: 13px; }
th, td { padding: 8px 12px; text-align: left; }
thead th {
  background: var(--os-sidebar); font-weight: 600; font-size: 12px;
  color: var(--os-text-secondary); border-bottom: 1px solid var(--os-border); position: sticky; top: 0;
}
tbody tr { border-bottom: 1px solid var(--os-border); cursor: default; }
tbody tr:hover { background: var(--os-hover); }
tbody tr:last-child { border-bottom: none; }
.ops { width: 1%; white-space: nowrap; }
.empty { text-align: center; color: var(--os-text-secondary); padding: 22px; }
</style>
