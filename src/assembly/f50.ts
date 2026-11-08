import { activeBackend } from '@/store/setup'

export type Source = {
  id: string
  name: string
  url: string
  mode: 'pool' | 'full'
  enabled: boolean
  userAgent: string
  intervalHours: number
  updatedAt: number
  lastError: string
  nodeCount: number
}

export type ManualNode = {
  id: string
  enabled: boolean
  node: Record<string, unknown>
}

export type Chain = {
  id: string
  name: string
  via: string
  exit: string
  enabled: boolean
}

export type RouteRule = {
  type: string
  value: string
  target: string
  noResolve: boolean
}

export type NodeEntry = {
  name: string
  type: string
  source: string
  enabled: boolean
  available: boolean
}

export type F50State = {
  version: string
  revision: number
  mode: 'simple' | 'full'
  active: string
  sources: Source[]
  manual: ManualNode[]
  chains: Chain[]
  routes: RouteRule[]
  nodes: NodeEntry[]
  groups: string[]
  disabled: string[]
  dns: Record<string, unknown>
  advanced: Record<string, unknown>
  resources: Record<string, unknown>
  drift: boolean
  ruleCount: number
  transport: string
}

export type Backup = { id: string; createdAt: number; bytes: number }

// Keep device authentication local; subscriptions never use a public converter.
export async function f50Request<T>(path: string, body?: Record<string, unknown>): Promise<T> {
  const backend = activeBackend.value
  if (!backend || backend.type !== 'clash') throw new Error('请先连接 F50 的 Mihomo 控制器')
  const url = new URL(`${backend.protocol}://${backend.host}:${backend.port}`)
  url.port = '7790'
  url.pathname = `/api/${path}`
  // The few read endpoints with a query are kept separate from pathname encoding.
  if (path.includes('?')) {
    const [pathname, query] = path.split('?')
    url.pathname = `/api/${pathname}`
    url.search = query ?? ''
  }
  let response: Response
  try {
    response = await fetch(url, {
      method: body ? 'POST' : 'GET',
      headers: {
        Authorization: `Bearer ${backend.password}`,
        ...(body ? { 'Content-Type': 'application/json' } : {}),
      },
      body: body ? JSON.stringify(body) : undefined,
      signal: AbortSignal.timeout(145000),
      cache: 'no-store',
      credentials: 'omit',
      referrerPolicy: 'no-referrer',
    })
  } catch {
    throw new Error('无法连接 F50 配置管理服务，请检查设备上的管理服务及 7790 端口')
  }
  const data = await response.json()
  if (!response.ok) throw new Error(data.error || `请求失败：HTTP ${response.status}`)
  return data as T
}

export const fetchF50State = () => f50Request<F50State>('state')
export const applyF50Action = (revision: number, action: Record<string, unknown>) =>
  f50Request<{ ok: boolean; revision: number }>('mutate', { ...action, revision })
