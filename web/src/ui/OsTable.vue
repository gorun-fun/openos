<script setup lang="ts">
// OsTable：数据表格——单元格渲染插槽、列排序、分页（本地切片或服务端受控）。
//
// 用法：
//   <OsTable :columns="cols" :rows="rows" paginated :page-size="20"
//            @sort-change="onSort" @update:page="onPage">
//     <template #cell-status="{ value }">
//       <span :class="value">{{ value }}</span>
//     </template>
//     <template #ops="{ row }"><OsButton size="sm" @click="edit(row)">编辑</OsButton></template>
//   </OsTable>
//
// 分页两种模式：
//   - 本地：paginated + pageSize，rows 自动切片，翻页内部处理；
//   - 服务端受控：传 page + total（rows 不再切片、不做本地排序），
//     @update:page / @sort-change 由父组件重新拉数据。
import { computed, ref, watch } from 'vue'

export interface TableColumn {
  key: string
  label: string
  width?: string
  align?: 'left' | 'center' | 'right'
  /** 可排序列：表头点击切换 升 → 降 → 取消 */
  sortable?: boolean
  /** 声明式格式化（优先级低于 #cell-<key> 渲染插槽） */
  format?: (value: any, row: Record<string, any>) => string
}

const props = withDefaults(defineProps<{
  columns: TableColumn[]
  rows: Record<string, any>[]
  /** 本地分页：rows 客户端切片 */
  paginated?: boolean
  pageSize?: number
  /** 受控页码（服务端分页时与 total 搭配使用） */
  page?: number
  /** 数据总数：传入后进入服务端模式（不切片、不本地排序） */
  total?: number
}>(), { paginated: false, pageSize: 20 })

const emit = defineEmits<{
  rowClick: [row: Record<string, any>]
  'update:page': [page: number]
  'sort-change': [sort: { key: string; order: 'asc' | 'desc' | '' }]
}>()

// ---- 排序 ----
const sortKey = ref('')
const sortOrder = ref<'asc' | 'desc' | ''>('')

function toggleSort(c: TableColumn) {
  if (!c.sortable) return
  if (sortKey.value !== c.key) { sortKey.value = c.key; sortOrder.value = 'asc' }
  else if (sortOrder.value === 'asc') sortOrder.value = 'desc'
  else { sortKey.value = ''; sortOrder.value = '' }
  emit('sort-change', { key: sortKey.value, order: sortOrder.value })
}

function cmp(a: any, b: any): number {
  if (a == null && b != null) return 1
  if (a != null && b == null) return -1
  if (typeof a === 'number' && typeof b === 'number') return a - b
  return String(a).localeCompare(String(b), 'zh')
}

// 服务端模式（传了 total）：排序与分页都交由父组件，本组件只渲染
const serverMode = computed(() => props.total !== undefined)

const sortedRows = computed(() => {
  if (serverMode.value || !sortKey.value) return props.rows
  const dir = sortOrder.value === 'desc' ? -1 : 1
  return [...props.rows].sort((x, y) => cmp(x[sortKey.value], y[sortKey.value]) * dir)
})

// ---- 分页 ----
const innerPage = ref(1)
const curPage = computed(() => props.page ?? innerPage.value)
const totalRows = computed(() => serverMode.value ? (props.total || 0) : sortedRows.value.length)
const pageCount = computed(() => Math.max(1, Math.ceil(totalRows.value / props.pageSize)))
const showPager = computed(() => serverMode.value || (props.paginated && pageCount.value > 1))

const displayRows = computed(() => {
  if (serverMode.value || !props.paginated) return sortedRows.value
  const start = (curPage.value - 1) * props.pageSize
  return sortedRows.value.slice(start, start + props.pageSize)
})

// 数据源整体替换 / 排序变化时回到第一页（本地模式）
watch([() => props.rows, sortKey, sortOrder], () => { innerPage.value = 1 })

function goPage(p: number) {
  const target = Math.min(Math.max(1, p), pageCount.value)
  if (props.page !== undefined) emit('update:page', target)
  else innerPage.value = target
}

function formatCell(c: TableColumn, row: Record<string, any>) {
  const v = row[c.key]
  return c.format ? c.format(v, row) : (v ?? '')
}
</script>

<template>
  <div class="os-table-wrap">
    <table class="os-table">
      <thead>
        <tr>
          <th
            v-for="c in columns" :key="c.key"
            :style="c.width ? { width: c.width } : {}"
            :class="[
              c.align && c.align !== 'left' ? 'align-' + c.align : '',
              { sortable: c.sortable, sorted: sortKey === c.key },
            ]"
            @click="toggleSort(c)"
          >
            {{ c.label }}
            <span v-if="c.sortable" class="sort" :class="sortKey === c.key ? sortOrder : ''">
              {{ sortKey === c.key ? (sortOrder === 'asc' ? '↑' : '↓') : '↕' }}
            </span>
          </th>
          <th class="ops"><slot name="ops-header" /></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="(row, i) in displayRows" :key="i" @click="$emit('rowClick', row)">
          <td v-for="c in columns" :key="c.key" :class="c.align && c.align !== 'left' ? 'align-' + c.align : ''">
            <slot :name="'cell-' + c.key" :row="row" :value="row[c.key]" :index="i">
              {{ formatCell(c, row) }}
            </slot>
          </td>
          <td class="ops"><slot name="ops" :row="row" :index="i" /></td>
        </tr>
        <tr v-if="!displayRows.length"><td :colspan="columns.length + 1" class="empty">暂无数据</td></tr>
      </tbody>
    </table>
    <div v-if="showPager" class="pager">
      <span class="info">共 {{ totalRows }} 条</span>
      <span class="grow" />
      <button class="pg" :disabled="curPage <= 1" @click="goPage(curPage - 1)">‹</button>
      <span class="pg-num">{{ curPage }} / {{ pageCount }}</span>
      <button class="pg" :disabled="curPage >= pageCount" @click="goPage(curPage + 1)">›</button>
    </div>
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
thead th.sortable { cursor: pointer; user-select: none; }
thead th.sortable:hover { color: var(--os-text); }
thead th.sorted { color: var(--os-accent); }
.sort { margin-left: 2px; font-size: 10px; opacity: .5; }
th.sorted .sort { opacity: 1; }
.align-right { text-align: right !important; }
.align-center { text-align: center !important; }
tbody tr { border-bottom: 1px solid var(--os-border); cursor: default; }
tbody tr:hover { background: var(--os-hover); }
tbody tr:last-child { border-bottom: none; }
.ops { width: 1%; white-space: nowrap; }
.empty { text-align: center; color: var(--os-text-secondary); padding: 22px; }
.pager {
  display: flex; align-items: center; gap: 8px; padding: 8px 12px;
  border-top: 1px solid var(--os-border); background: var(--os-sidebar);
  font-size: 12px; color: var(--os-text-secondary); position: sticky; bottom: 0;
}
.pager .grow { flex: 1; }
.pg {
  border: 1px solid var(--os-border-strong); background: var(--os-panel-solid); color: var(--os-text);
  border-radius: 6px; width: 24px; height: 22px; cursor: pointer; font-size: 13px; line-height: 1;
}
.pg:hover:not(:disabled) { background: var(--os-hover); }
.pg:disabled { opacity: .4; cursor: default; }
.pg-num { font-variant-numeric: tabular-nums; min-width: 52px; text-align: center; }
</style>
