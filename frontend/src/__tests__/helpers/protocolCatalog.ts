import { beforeEach } from 'vitest'
import {
  protocolCatalog,
  protocolCatalogError,
  protocolCatalogLoading,
  type ProtocolCatalog,
} from '@/api/admin/protocolCapabilities'
import fixture from '../fixtures/protocol-catalog.json'

// protocolCatalog.ts initializes shared protocol-catalog refs for suites that
// opt into the fixture. Each case receives a cloned catalog and cleared
// loading and error flags so mutations in one case do not contaminate
// another.

// useProtocolCatalogFixture registers per-test initialization for the shared
// protocol catalog. Call it while defining the suite; each case receives a
// structured clone and starts with loading and error flags cleared.
export function useProtocolCatalogFixture() {
  beforeEach(() => {
    // Clone the fixture before publishing it to shared refs so a test that
    // edits nested catalog entries leaves later cases independent.
    protocolCatalog.value = structuredClone(fixture) as ProtocolCatalog
    protocolCatalogError.value = false
    protocolCatalogLoading.value = false
  })
}
