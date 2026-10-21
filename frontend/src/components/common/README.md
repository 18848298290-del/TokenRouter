# Common Components

This directory contains reusable Vue 3 components built with Composition API, TypeScript, and TailwindCSS.

## Components

### table/DataTable.vue

A generic data table component with sorting, loading states, and custom cell rendering.

**Props:**

- `columns: Column[]` - Array of column definitions with key, label, sortable, and formatter
- `data: any[]` - Array of data objects to display
- `loading?: boolean` - Show loading skeleton
- `defaultSortKey?: string` - Default sort key (only used if no persisted sort state)
- `defaultSortOrder?: 'asc' | 'desc'` - Default sort order (default: `asc`)
- `sortStorageKey?: string` - Persist sort state (key + order) to localStorage
- `columnOrderStorageKey?: string` - Enable header drag handles and persist column order under a stable, table-specific key. Left/right arrow keys also move a focused handle. Selection and action columns keep their positions; hidden columns retain their saved order.
- `rowKey?: string | (row: any) => string | number` - Row key field or resolver (defaults to `row.id`, falls back to index)

**Slots:**

- `empty` - Custom empty state content
- `cell-{key}` - Custom cell renderer for specific column (receives `row` and `value`)

**Usage:**

```vue
<DataTable
  :columns="[
    { key: 'name', label: 'Name', sortable: true },
    { key: 'email', label: 'Email' },
    { key: 'status', label: 'Status', formatter: (val) => val.toUpperCase() }
  ]"
  :data="users"
  :loading="isLoading"
>
  <template #cell-actions="{ row }">
    <button @click="editUser(row)">Edit</button>
  </template>
</DataTable>
```

---

### Pagination.vue

Pagination component with page numbers, navigation, and page size selector.

**Props:**

- `total: number` - Total number of items
- `page: number` - Current page (1-indexed)
- `pageSize: number` - Items per page
- `pageSizeOptions?: number[]` - Available page size options (default: [10, 20, 50, 100])

**Events:**

- `update:page` - Emitted when page changes
- `update:pageSize` - Emitted when page size changes

**Usage:**

```vue
<Pagination
  :total="totalUsers"
  :page="currentPage"
  :pageSize="pageSize"
  @update:page="currentPage = $event"
  @update:pageSize="pageSize = $event"
/>
```

---

### BaseDialog.vue

通用弹窗提供标题、内容和页脚插槽，并管理焦点与关闭后的清理。

属性：

- `show: boolean`：控制弹窗显示。
- `title: string`：弹窗标题。
- `width?: 'narrow' | 'normal' | 'wide' | 'extra-wide' | 'full'`：宽度档位，默认为 `normal`。
- `closeOnEscape?: boolean`：按 Escape 触发关闭，默认开启。
- `closeOnClickOutside?: boolean`：点击遮罩触发关闭，默认关闭。

事件：

- `close`：调用方收到事件后关闭弹窗。
- `after-leave`：关闭动画完成。

插槽：

- `default`：弹窗正文。
- `footer`：页脚操作。

用法：

```vue
<BaseDialog :show="showDialog" title="Edit User" width="wide" @close="showDialog = false">
  <form @submit.prevent="saveUser">
    <!-- 用户资料表单 -->
  </form>

  <template #footer>
    <button @click="showDialog = false">Cancel</button>
    <button @click="saveUser">Save</button>
  </template>
</BaseDialog>
```

### ConfirmDialog.vue

ConfirmDialog 使用 BaseDialog 显示确认内容和操作按钮。

**Props:**

- `show: boolean` - Control dialog visibility
- `title: string` - Dialog title
- `message: string` - Confirmation message
- `confirmText?: string` - Confirm button text (default: 'Confirm')
- `cancelText?: string` - Cancel button text (default: 'Cancel')
- `danger?: boolean` - Use danger/red styling (default: false)

**Events:**

- `confirm` - Emitted when user confirms
- `cancel` - Emitted when user cancels

**Usage:**

```vue
<ConfirmDialog
  :show="showDeleteConfirm"
  title="Delete User"
  message="Are you sure you want to delete this user? This action cannot be undone."
  confirm-text="Delete"
  cancel-text="Cancel"
  danger
  @confirm="deleteUser"
  @cancel="showDeleteConfirm = false"
/>
```

---

### Toast.vue

Toast notification component that automatically displays toasts from the app store.

**Usage:**

```vue
<!-- Add once in App.vue or layout -->
<Toast />
```

```typescript
// Trigger toasts from anywhere using the app store
import { useAppStore } from '@/stores/app'

const appStore = useAppStore()

appStore.addToast({
  type: 'success',
  title: 'Success!',
  message: 'User created successfully',
  duration: 3000
})

appStore.addToast({
  type: 'error',
  message: 'Failed to delete user'
})
```

---

### EmptyState.vue

Empty state placeholder with icon, message, and optional action button.

**Props:**

- `icon?: Component` - Icon component
- `title: string` - Empty state title
- `description: string` - Empty state description
- `actionText?: string` - Action button text
- `actionTo?: string | object` - Router link destination
- `actionIcon?: boolean` - Show plus icon in button (default: true)

**Slots:**

- `icon` - Custom icon content
- `action` - Custom action button/link

**Usage:**

```vue
<EmptyState
  title="No users found"
  description="Get started by creating your first user account."
  action-text="Add User"
  :action-to="{ name: 'users-create' }"
/>
```

---

### RuleListEditor.vue

Shell for lists edited one row at a time: a header with title, hint and add button, a dashed empty state, and per-row move and delete buttons. It does not own the data. The parent performs every change in response to its events.

**Props:**

- `items: T[]` - Rows to render
- `itemKey?: (item, index) => string | number` - Row key (defaults to object identity; primitive rows fall back to the index)
- `title?: string` / `hint?: string` - Header text
- `titleStyle?: 'label' | 'section'` - Form label style or group-section heading style (default: `label`)
- `addLabel?: string` - Add button text (default: `common.add`)
- `addPlacement?: 'header' | 'footer'` - Add button position (default: `header`; use `footer` when there is no title)
- `addDisabled?: boolean` - Disable the add button on its own
- `removeLabel?: string` - Delete button label (default: `common.delete`)
- `emptyText?: string` - Empty state text; omit it to hide the empty state
- `min?: number` / `max?: number` - Row count limits; delete is disabled at `min`, add at `max`
- `disabled?: boolean` - Disable every action
- `removable?: boolean` - Show the delete button (default: true)
- `reorderable?: boolean` - Show move up/down buttons
- `variant?: 'line' | 'card'` - Rows separated by dividers, or bordered cards (default: `line`)
- `itemLabel?: (index) => string` - Row heading such as "Rule #1"
- `animated?: boolean` - List enter/leave motion (default: true; turn it off when keys are indexes)
- `error?: string` - List-level error shown as an alert
- `testId?: string` - Prefix for `data-testid` hooks: `-add`, `-row`, `-remove-{i}`, `-move-up-{i}`, `-move-down-{i}`

**Events:** `add`, `remove(index)`, `move(from, to)`

**Slots:** `row({ item, index })`, `header-actions`, `header-extra`, `footer`

Clicking the add button focuses the first input of the new row. Rows appended by presets or imports do not take focus.

---

### ModelMappingEditor.vue

Source → target mapping rows built on `RuleListEditor`. `v-model` is an array of `{ from, to }` (`ModelMappingRow` in `@/utils/modelMappingRules`). Typing edits the row object in place and emits a new array; adding and removing also emit `add(row)` and `remove(row, index)`.

Validation is opt-in. Compute issues with `validateModelMappingRows(rows, options)`, translate them, and pass the result as `fieldErrors`.

```vue
<ModelMappingEditor
  v-model="rows"
  :title="t('keys.modelRedirect.label')"
  :add-label="t('keys.modelRedirect.addRule')"
  :empty-text="t('keys.modelRedirect.empty')"
  :source-label="t('keys.modelRedirect.source')"
  :target-label="t('keys.modelRedirect.target')"
  :field-errors="fieldErrors"
/>
```

## Import

组件按文件路径导入：

```typescript
import DataTable from '@/components/common/table/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
```

## Features

All components include:

- **TypeScript support** with proper type definitions
- **Accessibility** with ARIA attributes and keyboard navigation
- **Responsive design** with mobile-friendly layouts
- **TailwindCSS styling** for consistent design
- **Vue 3 Composition API** with `<script setup>`
- **Slot support** for customization
