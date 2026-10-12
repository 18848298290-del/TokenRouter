import { apiClient } from '../client'
import type { ModelAttributes } from '@/types/modelAttributes'

export interface AttributeRule {
  id?: string
  models: string[]
  attributes: ModelAttributes
  match?: AttributeRouteMatch
}

export interface AttributeRouteMatch {
  platform?: string
  provider_type?: string
  provider_mode?: string
  site?: string
  client_protocol?: string
  upstream_protocol?: string
}

export interface ResolvedAttributes {
  target: AttributeRouteMatch & { model: string; catalog_provider?: string; endpoint?: string }
  attributes: ModelAttributes
  sources: Record<string, string>
  rules: Record<string, unknown>
  version: string
}

export interface AttributeConfig {
  id?: number
  name: string
  description: string
  status: string
  group_ids: number[]
  rules: AttributeRule[]
  created_at?: string
  updated_at?: string
}

export interface DefaultAttributes {
  model: string
  provider: string
  source: string
  canonical_model_id?: string
  attributes: ModelAttributes
}

export interface AttributeCatalogResponse {
  items: DefaultAttributes[]
  total: number
  providers: string[]
  version: string
  last_updated: string
  last_error?: string
}

const base = '/admin/model-attributes'

// 默认查询和档案编辑分开，普通读取不触发目录更新。
export const modelAttributesAPI = {
  async preview(groupID: number, model: string, config?: AttributeConfig, clientProtocol?: string) {
    return (await apiClient.post<{ routes: ResolvedAttributes[] }>(`${base}/preview`, {
      group_id: groupID, model, config, client_protocol: clientProtocol,
    })).data
  },
  async list(params: Record<string, string | number>) {
    return (await apiClient.get<{ items: AttributeConfig[]; total: number }>(`${base}/configs`, { params })).data
  },
  async save(config: AttributeConfig) {
    return config.id
      ? (await apiClient.put<AttributeConfig>(`${base}/configs/${config.id}`, config)).data
      : (await apiClient.post<AttributeConfig>(`${base}/configs`, config)).data
  },
  async remove(id: number) { await apiClient.delete(`${base}/configs/${id}`) },
  async defaults(params: Record<string, string | number>) {
    return (await apiClient.get<AttributeCatalogResponse>(`${base}/defaults`, { params })).data
  },
  async getModelDefaultAttributes(model: string) {
    return (await apiClient.get<ModelAttributes>(`${base}/defaults/model`, { params: { model } })).data
  },
  async update() { await apiClient.post(`${base}/defaults/update`, undefined, { timeout: 120_000 }) },
}
