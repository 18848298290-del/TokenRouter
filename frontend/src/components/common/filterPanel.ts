import type { ComputedRef, InjectionKey } from 'vue'

// filterPanel.ts defines the injected connections between a filter panel, its fields,
// and their controls. Reactive chip records carry visible text and clearing actions,
// while shared empty-value helpers let Select report whether a field contributes an
// active filter.

// FilterControlState reports a nested control selection to its owning field.
export interface FilterControlState {
  active: boolean // Contributing filter selection flag.
  text: string // Visible selected value text.
  // clear requests that the owning control clear its selection through the registered
  // handler. The parent remains responsible for accepting emitted changes and updating
  // the selected value.
  clear: () => void
}

// FilterChip describes an active field label and its clearing action.
export interface FilterChip {
  label: string // Visible field label text.
  text: string // Visible selected value text.
  // clear requests that the owning control clear its selection through the registered
  // handler. The parent remains responsible for accepting emitted changes and updating
  // the selected value.
  clear: () => void
  // Field root element used for chip order; null leaves the pair ordering unchanged in
  // the panel comparator.
  el: HTMLElement | null
}

// FilterPanelContext registers reactive chips for the lifetime of their fields.
export interface FilterPanelContext {
  // register adds a reactive chip to the panel and returns its removal callback. The
  // field calls that cleanup before unmounting so the panel stops displaying its
  // selection.
  register: (chip: ComputedRef<FilterChip | null>) => () => void
}

// FilterFieldContext connects nested controls with their field reset and chip state.
export interface FilterFieldContext {
  // emptyValue returns the field reset value for its nested control. Undefined tells
  // Select to infer it from the first available option, while other values override
  // that inference.
  emptyValue: () => unknown
  // bindControl installs a computed control state on the owning field. A later
  // registration replaces the previous state, and the field reads the current computed
  // value when building its chip.
  bindControl: (state: ComputedRef<FilterControlState>) => void
}

// FILTER_PANEL_KEY connects fields to their chip registry, while FILTER_FIELD_KEY
// connects controls to the reset value and selected-state registration of one field.
export const FILTER_PANEL_KEY: InjectionKey<FilterPanelContext> = Symbol('filterPanel')
export const FILTER_FIELD_KEY: InjectionKey<FilterFieldContext> = Symbol('filterField')

// isBlank recognizes null, undefined, and empty strings as unselected values.
const isBlank = (value: unknown) => value === null || value === undefined || value === ''

// resolveEmptyValue infers the reset value from the first option. Blank values and the
// literal all are retained, while any other first option produces null for an
// unselected field.
export function resolveEmptyValue(firstOptionValue: unknown): unknown {
  return isBlank(firstOptionValue) || firstOptionValue === 'all' ? firstOptionValue : null
}

// isSameFilterValue compares a selected value against its reset value. Null,
// undefined, and an empty string are interchangeable blanks; other values require
// strict equality to count as inactive.
export function isSameFilterValue(value: unknown, emptyValue: unknown): boolean {
  return value === emptyValue || (isBlank(value) && isBlank(emptyValue))
}
