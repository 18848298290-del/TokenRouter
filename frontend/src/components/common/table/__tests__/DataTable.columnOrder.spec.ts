import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import DataTable from '../DataTable.vue'

// DataTable.columnOrder.spec.ts checks persisted column movement through
// rendered headers, cells, and keyboard focus. Fixtures exercise structural
// columns, hidden keys, independent table identities, and selection so path
// aggregation retains the table module's caller-visible behavior.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

// Column and row fixtures include fixed structural columns and sortable data so
// movement can be observed through rendered cells.
const columns = [
  { key: 'select', label: '' },
  { key: 'name', label: '名称', sortable: true },
  { key: 'email', label: '邮箱' },
  { key: 'cost', label: '费用' },
  { key: 'actions', label: '操作' },
]
const data = [{ id: 1, name: 'Alpha', email: 'alpha@example.test', cost: 10 }]
// wrappers retains each mounted table until afterEach releases its document
// listeners, observers, and teleported component resources.
const wrappers: VueWrapper[] = []

// mountTable mounts a column-order fixture in the document and tracks it for
// cleanup. Callers override props to exercise storage identity, sorting, or
// controlled selection through the real component.
const mountTable = (props = {}) => {
  const wrapper = mount(DataTable, {
    attachTo: document.body,
    props: { columns, data, columnOrderStorageKey: 'test-column-order', ...props },
  })
  wrappers.push(wrapper)
  return wrapper
}

// headerKeys reads the rendered data-column key order from table headers.
const headerKeys = (wrapper: VueWrapper) =>
  wrapper.findAll('th[data-column-key]').map((header) => header.attributes('data-column-key'))

// dragColumn dispatches browser drag events to exercise a requested relative column move.
// The target rectangle fixes its midpoint, and callers choose before or after
// to assert the resulting header and cell order.
const dragColumn = async (
  wrapper: VueWrapper,
  source: string,
  target: string,
  side: 'before' | 'after',
) => {
  const handle = wrapper.get(`th[data-column-key="${source}"] button.column-drag-handle`)
  const targetHeader = wrapper.get(`th[data-column-key="${target}"]`)
  const transfer = { setData: vi.fn(), effectAllowed: '', dropEffect: '' }
  vi.spyOn(targetHeader.element, 'getBoundingClientRect').mockReturnValue({
    left: 100,
    width: 100,
  } as DOMRect)
  vi.spyOn(wrapper.get('.table-wrapper').element, 'getBoundingClientRect').mockReturnValue({
    left: 0,
    right: 1000,
  } as DOMRect)
  await handle.trigger('dragstart', { dataTransfer: transfer })
  await targetHeader.trigger('dragover', {
    dataTransfer: transfer,
    clientX: side === 'before' ? 120 : 180,
  })
  await targetHeader.trigger('drop', { dataTransfer: transfer })
  await handle.trigger('dragend')
}

describe('DataTable 列顺序', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    wrappers.splice(0).forEach((wrapper) => wrapper.unmount())
    vi.restoreAllMocks()
  })

  // A data column is dragged before another while server sorting is active.
  // Headers and cells must move together, structural columns stay fixed, and
  // sort state remains independently controllable.
  it('拖动后同步表头与单元格，结构列留在两端，并保留数据排序', async () => {
    const wrapper = mountTable({ defaultSortKey: 'name', serverSideSort: true })
    await dragColumn(wrapper, 'cost', 'name', 'before')

    expect(headerKeys(wrapper)).toEqual(['select', 'cost', 'name', 'email', 'actions'])
    expect(
      wrapper
        .findAll('tbody td[data-column-key]')
        .map((cell) => cell.attributes('data-column-key')),
    ).toEqual(headerKeys(wrapper))
    expect(wrapper.get('tbody td[data-column-key="cost"]').text()).toBe('10')
    expect(wrapper.get('th[data-column-key="cost"]').classes()).toContain('sticky-col-left')
    expect(wrapper.get('th[data-column-key="select"]').find('button').exists()).toBe(false)
    expect(wrapper.get('th[data-column-key="actions"]').find('button').exists()).toBe(false)
    expect(wrapper.emitted('sort')).toBeUndefined()
    expect(wrapper.get('th[data-column-key="name"]').attributes('aria-sort')).toBe('ascending')

    await wrapper.get('th[data-column-key="name"]').trigger('click')
    expect(wrapper.emitted('sort')?.at(-1)).toEqual(['name', 'desc'])
  })

  // A column is dropped after its target and the table is mounted again. The
  // second instance must restore the moved order from its browser preference.
  it('支持插入目标列右侧，重新挂载后恢复顺序', async () => {
    const wrapper = mountTable()
    await dragColumn(wrapper, 'name', 'cost', 'after')
    expect(headerKeys(mountTable())).toEqual(['select', 'email', 'cost', 'name', 'actions'])
  })

  // A hidden column returns after another column moves and a new column appears.
  // Its previous position must survive while the new data key appends before
  // actions.
  it('隐藏列在重新显示和刷新后保留位置，新列追加到末尾', async () => {
    const wrapper = mountTable()
    await wrapper.setProps({ columns: columns.filter((column) => column.key !== 'email') })
    await dragColumn(wrapper, 'cost', 'name', 'before')
    const expanded = [...columns.slice(0, -1), { key: 'status', label: '状态' }, columns.at(-1)!]
    await wrapper.setProps({ columns: expanded })
    expect(headerKeys(wrapper)).toEqual(['select', 'cost', 'name', 'email', 'status', 'actions'])
    expect(headerKeys(mountTable({ columns: expanded }))).toEqual(headerKeys(wrapper))
  })

  // Two table identities share the same candidate columns. Their browser
  // preferences must remain independent, and changing the identity must reload
  // the corresponding saved order.
  it('不同表格的存储互相独立，切换表格标识时重新读取', async () => {
    const first = mountTable()
    await dragColumn(first, 'cost', 'name', 'before')
    const second = mountTable({ columnOrderStorageKey: 'another-table' })
    expect(headerKeys(second)).toEqual(columns.map((column) => column.key))
    await first.setProps({ columnOrderStorageKey: 'another-table' })
    expect(headerKeys(first)).toEqual(headerKeys(second))
  })

  // The preference contains invalid JSON or a non-array JSON value. The rendered
  // header keys must recover to the caller supplied order for each unusable
  // value.
  it.each(['null', '{}', 'invalid json'])('损坏的存储 %s 回退到默认顺序', (stored) => {
    localStorage.setItem('test-column-order', stored)
    expect(headerKeys(mountTable())).toEqual(columns.map((column) => column.key))
  })

  // The preference includes duplicates, non-string values, deleted keys, and
  // structural keys. Visible columns must use the valid movable preference while
  // structural positions remain fixed.
  it('忽略缓存中的重复、失效和结构列键', () => {
    localStorage.setItem(
      'test-column-order',
      JSON.stringify(['cost', 9, 'cost', 'deleted', 'actions', 'select']),
    )
    expect(headerKeys(mountTable())).toEqual(['select', 'cost', 'name', 'email', 'actions'])
  })

  // The user moves a focused handle with arrow keys and also clicks it. Column
  // order must change while focus stays on that handle and sorting remains
  // untouched.
  it('键盘换位后保留手柄焦点，点击手柄不触发数据排序', async () => {
    const wrapper = mountTable({ serverSideSort: true })
    const handle = wrapper.get('th[data-column-key="name"] button.column-drag-handle')
    await handle.trigger('click')
    await handle.trigger('keydown', { key: 'ArrowRight' })
    expect(headerKeys(wrapper)).toEqual(['select', 'email', 'name', 'cost', 'actions'])
    expect(document.activeElement).toBe(handle.element)
    expect(wrapper.emitted('sort')).toBeUndefined()
    await handle.trigger('keydown', { key: 'ArrowLeft' })
    expect(headerKeys(wrapper)).toEqual(columns.map((column) => column.key))
  })

  // A data column is dropped on the fixed actions column. The table must reject
  // the move, clear drag styling, and leave browser preferences unwritten.
  it('放到结构列上取消移动，并清理拖拽标记', async () => {
    const wrapper = mountTable()
    await dragColumn(wrapper, 'name', 'actions', 'before')
    expect(headerKeys(wrapper)).toEqual(columns.map((column) => column.key))
    expect(wrapper.find('.opacity-50').exists()).toBe(false)
    expect(localStorage.getItem('test-column-order')).toBeNull()
  })

  // Column ordering has no storage key and the caller replaces its column array.
  // The headers must follow that array and expose no movement handles.
  it('未启用时跟随调用方的列顺序，不显示拖拽手柄', async () => {
    const wrapper = mountTable({ columnOrderStorageKey: undefined })
    const updated = [columns[0], columns[3], columns[1], columns[2], columns[4]]
    await wrapper.setProps({ columns: updated })
    expect(headerKeys(wrapper)).toEqual(updated.map((column) => column.key))
    expect(wrapper.find('.column-drag-handle').exists()).toBe(false)
  })

  // Built-in row selection is enabled while a data column moves. The selection
  // header must remain first, and emitted keys must retain off-page selection
  // and the selected record identity.
  it('内置行选择不参与换位，选择事件保持原来的行键', async () => {
    const wrapper = mountTable({ columns: columns.slice(1), selectable: true, selectedKeys: [99] })
    await dragColumn(wrapper, 'cost', 'name', 'before')
    expect(wrapper.findAll('th')[0].find('[data-test="select-all"]').exists()).toBe(true)
    await wrapper.get('[data-test="select-row"]').setValue(true)
    expect(wrapper.emitted('update:selectedKeys')?.at(-1)?.[0]).toEqual([99, 1])
  })
})
