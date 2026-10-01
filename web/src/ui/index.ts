// OpenOS 统一基础 UI 组件库 —— 第三方应用与内置应用共用。
// 用法：import { OsButton, OsInput, toast } from '@/ui'
import OsButton from './OsButton.vue'
import OsInput from './OsInput.vue'
import OsSelect from './OsSelect.vue'
import OsToggle from './OsToggle.vue'
import OsCheckbox from './OsCheckbox.vue'
import OsTextarea from './OsTextarea.vue'
import OsPanel from './OsPanel.vue'
import OsModal from './OsModal.vue'
import OsTabs from './OsTabs.vue'
import OsTable from './OsTable.vue'
import OsLayout from './OsLayout.vue'
import OsToolbar from './OsToolbar.vue'
import OsEmpty from './OsEmpty.vue'
import OsForm from './OsForm.vue'
import OsField from './OsField.vue'
import OsUpload from './OsUpload.vue'
import OsToasts from './OsToasts.vue'
import OsFilePicker from './OsFilePicker.vue'
import AppIcon from './AppIcon.vue'

export { toast, toastState } from './toast'
export type { ToastType, ToastItem } from './toast'
export { useMenu, openMenu, closeMenu, installMenuGuard, menuPos } from './menu'
export { fileDialog, resolveFileDialog } from './fileDialog'
export type { FileDialogRequest } from './fileDialog'
export { icons } from './icons'
// OsTable 列定义类型：宿主应用以强类型声明列（export type 不产生运行时代码）
export type { TableColumn } from './OsTable.vue'

export {
  OsButton, OsInput, OsSelect, OsToggle, OsCheckbox, OsTextarea, OsPanel, OsModal,
  OsTabs, OsTable, OsLayout, OsToolbar, OsEmpty, OsForm, OsField, OsUpload, OsToasts, OsFilePicker, AppIcon,
}
