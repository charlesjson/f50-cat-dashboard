<script setup lang="ts">
import {
  fetchConfigs,
  flushDNSCache,
  flushFakeIP,
  queryDNS,
  updateGeoData,
} from '@/assembly/config'
import {
  applyF50Action,
  f50Request,
  fetchF50State,
  type Backup,
  type Chain,
  type F50State,
  type ManualNode,
  type RouteRule,
  type Source,
} from '@/assembly/f50'
import { fetchProxies } from '@/assembly/proxies'
import { fetchRules } from '@/assembly/rules'
import GroupManager from '@/components/f50/GroupManager.vue'
import { computed, onMounted, reactive, ref, watch } from 'vue'

type Tab = 'nodes' | 'groups' | 'routes' | 'dns' | 'advanced' | 'resources' | 'preview'
type Editor = 'source' | 'manual' | 'chain' | 'base' | null
const tabs: { id: Tab; label: string }[] = [
  { id: 'nodes', label: '节点' },
  { id: 'groups', label: '策略组' },
  { id: 'routes', label: '路由' },
  { id: 'dns', label: 'DNS 配置' },
  { id: 'advanced', label: '高级' },
  { id: 'resources', label: '外部资源' },
  { id: 'preview', label: '预览' },
]
const tab = ref<Tab>('nodes')
const state = ref<F50State>()
const busy = ref(false)
const message = ref('')
const failed = ref(false)
const editor = ref<Editor>(null)
const search = ref('')
const visibleCount = ref(80)
const routes = ref<RouteRule[]>([])
const dns = reactive<Record<string, unknown>>({})
const dnsFields = [
  'default-nameserver',
  'proxy-server-nameserver',
  'nameserver',
  'direct-nameserver',
  'fallback',
]
const dnsLabels: Record<string, string> = {
  'default-nameserver': '默认 DNS',
  'proxy-server-nameserver': '代理服务器 DNS',
  nameserver: 'DNS 服务器',
  'direct-nameserver': '直连 DNS',
  fallback: 'Fallback DNS',
}
const dnsLists = reactive<Record<string, string>>({})
const dnsExtra = ref('{}')
const dnsDomain = ref('example.com')
const dnsType = ref('A')
const dnsResult = ref('')
const advanced = reactive<Record<string, unknown>>({})
const resources = reactive<Record<string, unknown>>({})
const resourceURLs = reactive<Record<string, string>>({})
const preview = ref('')
const backups = ref<Backup[]>([])
const form = reactive({
  id: '',
  name: '',
  url: '',
  mode: 'pool',
  enabled: true,
  userAgent: '',
  intervalHours: 24,
  content: '',
  via: '',
  exit: '',
})
const selectedSourceID = ref('current')
const title = computed(
  () =>
    ({ source: '导入 / 编辑订阅', manual: '手动节点', chain: '链式代理', base: '编辑配置' })[
      editor.value ?? 'base'
    ],
)
const enabledCount = computed(() => state.value?.nodes.filter((node) => node.enabled).length ?? 0)
const filteredNodes = computed(() =>
  (state.value?.nodes ?? []).filter((node) =>
    `${node.name} ${node.type} ${node.source}`.toLowerCase().includes(search.value.toLowerCase()),
  ),
)
const targets = computed(() => [
  ...new Set([
    'DIRECT',
    'REJECT',
    ...(state.value?.groups ?? []),
    ...(state.value?.nodes.filter((n) => n.enabled).map((n) => n.name) ?? []),
  ]),
])
const enabledNodes = computed(() => state.value?.nodes.filter((n) => n.enabled) ?? [])
const formatTime = (value: number) => (value ? new Date(value * 1000).toLocaleString() : '尚未更新')
const errorText = (error: unknown) => (error instanceof Error ? error.message : String(error))

function notice(text: string, error = false) {
  message.value = text
  failed.value = error
}
function syncForms(next: F50State) {
  routes.value = structuredClone(next.routes)
  for (const key of Object.keys(dns)) delete dns[key]
  Object.assign(dns, next.dns)
  for (const key of dnsFields) dnsLists[key] = ((next.dns[key] as string[]) ?? []).join('\n')
  dnsExtra.value = JSON.stringify(
    Object.fromEntries(
      Object.entries(next.dns).filter(
        ([key]) =>
          ![
            ...dnsFields,
            'listen',
            'enable',
            'enhanced-mode',
            'ipv6',
            'respect-rules',
            'use-hosts',
          ].includes(key),
      ),
    ),
    null,
    2,
  )
  for (const key of Object.keys(advanced)) delete advanced[key]
  Object.assign(advanced, next.advanced)
  Object.assign(resources, next.resources)
  Object.assign(resourceURLs, (next.resources['geox-url'] as Record<string, string>) ?? {})
}
async function load() {
  const next = await fetchF50State()
  state.value = next
  syncForms(next)
}
async function refresh() {
  busy.value = true
  try {
    await load()
    notice('已读取设备配置')
  } catch (error) {
    notice(errorText(error), true)
  } finally {
    busy.value = false
  }
}
async function run(action: Record<string, unknown>, closeEditor = false) {
  if (!state.value || busy.value) return false
  busy.value = true
  try {
    await applyF50Action(state.value.revision, action)
    if (closeEditor) editor.value = null
    await load()
    await Promise.allSettled([fetchConfigs(), fetchProxies(), fetchRules()])
    notice('已保存并应用到设备')
    if (tab.value === 'advanced') await loadBackups()
    return true
  } catch (error) {
    notice(errorText(error), true)
    return false
  } finally {
    busy.value = false
  }
}
async function coreAction(action: () => Promise<unknown>, success: string) {
  busy.value = true
  try {
    await action()
    notice(success)
  } catch (error) {
    notice(errorText(error), true)
  } finally {
    busy.value = false
  }
}
function resetForm() {
  notice('')
  Object.assign(form, {
    id: '',
    name: '',
    url: '',
    mode: 'pool',
    enabled: true,
    userAgent: '',
    intervalHours: 24,
    content: '',
    via: '',
    exit: '',
  })
}
function editSource(source?: Source) {
  resetForm()
  if (source) Object.assign(form, source)
  editor.value = 'source'
}
function editManual(node?: ManualNode) {
  resetForm()
  if (node) {
    form.id = node.id
    form.content = JSON.stringify(node.node, null, 2)
  }
  editor.value = 'manual'
}
function editChain(chain?: Chain) {
  resetForm()
  if (chain) Object.assign(form, chain)
  editor.value = 'chain'
}
async function editBase(id = 'current') {
  busy.value = true
  try {
    const data = await f50Request<{ content: string }>(`source?id=${encodeURIComponent(id)}`)
    resetForm()
    form.content = data.content
    selectedSourceID.value = id
    editor.value = 'base'
  } catch (error) {
    notice(errorText(error), true)
  } finally {
    busy.value = false
  }
}
async function readFile(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0]
  if (!file) return
  if (file.size > 5 * 1024 * 1024) {
    notice('文件超过 5 MiB', true)
    return
  }
  form.content = await file.text()
  if (!form.name) form.name = file.name.replace(/\.ya?ml$/i, '')
  form.mode = 'full'
  form.url = ''
}
async function submitEditor() {
  if (editor.value === 'source') {
    await run(
      {
        op: 'save-source',
        id: form.id,
        name: form.name.trim(),
        url: form.url.trim(),
        mode: form.mode,
        enabled: form.enabled,
        userAgent: form.userAgent,
        intervalHours: Number(form.intervalHours),
        content: form.content,
      },
      true,
    )
  } else if (editor.value === 'manual') {
    await run({ op: 'save-manual', id: form.id, content: form.content }, true)
  } else if (editor.value === 'chain') {
    await run(
      {
        op: 'save-chain',
        id: form.id,
        name: form.name.trim(),
        via: form.via,
        exit: form.exit,
        enabled: form.enabled,
      },
      true,
    )
  } else if (editor.value === 'base') {
    await run(
      {
        op: selectedSourceID.value === 'current' ? 'save-base' : 'save-source-content',
        id: selectedSourceID.value,
        content: form.content,
      },
      true,
    )
  }
}
async function validateEditor() {
  await coreAction(() => f50Request('validate', { content: form.content }), 'Mihomo 配置校验通过')
}
async function switchMode(mode: string) {
  if (
    mode === 'simple' &&
    !window.confirm(
      '简单模式将使用一个节点选择组和基础局域网直连规则。原配置会自动备份，可切回完整模式。',
    )
  )
    return
  await run({ op: 'mode', mode })
}
async function remove(op: string, id: string) {
  if (window.confirm('确认删除？本次变更前会自动保留备份。')) await run({ op, id })
}
async function toggleNode(name: string, enabled: boolean) {
  const disabled = new Set(state.value?.disabled ?? [])
  if (enabled) disabled.delete(name)
  else disabled.add(name)
  await run({ op: 'nodes', names: [...disabled] })
}
function addRoute() {
  routes.value.push({
    type: 'DOMAIN-SUFFIX',
    value: '',
    target: state.value?.groups[0] ?? 'DIRECT',
    noResolve: false,
  })
}
async function saveDNS() {
  try {
    const extra = JSON.parse(dnsExtra.value)
    if (!extra || Array.isArray(extra) || typeof extra !== 'object')
      throw new Error('DNS 扩展设置需要 JSON 对象')
    const values: Record<string, unknown> = { ...dns, ...extra }
    delete values.listen
    for (const key of dnsFields)
      values[key] =
        dnsLists[key]
          ?.split('\n')
          .map((x) => x.trim())
          .filter(Boolean) ?? []
    await run({ op: 'dns', values })
  } catch (error) {
    notice(errorText(error), true)
  }
}
async function lookupDNS() {
  await coreAction(async () => {
    dnsResult.value = JSON.stringify(
      await queryDNS({ name: dnsDomain.value, type: dnsType.value }),
      null,
      2,
    )
  }, 'DNS 查询完成')
}
async function loadBackups() {
  backups.value = (await f50Request<{ items: Backup[] }>('backups')).items
}
async function loadPreview() {
  preview.value = (await f50Request<{ content: string }>('preview')).content
}
function downloadPreview() {
  const url = URL.createObjectURL(new Blob([preview.value], { type: 'application/yaml' }))
  const link = document.createElement('a')
  link.href = url
  link.download = 'f50-config.yaml'
  link.click()
  URL.revokeObjectURL(url)
}
async function restore(backup: Backup) {
  if (window.confirm(`恢复到 ${formatTime(backup.createdAt)} 的配置？当前状态会先自动备份。`))
    await run({ op: 'restore', id: backup.id })
}
async function adopt() {
  if (
    window.confirm(
      '将设备当前配置作为新的基础配置。现有管理记录和覆盖项会归档到备份，以避免重复合并。',
    )
  )
    await run({ op: 'adopt' })
}
watch(tab, async (value) => {
  if (!state.value) return
  try {
    if (value === 'advanced') await loadBackups()
    if (value === 'preview') await loadPreview()
  } catch (error) {
    notice(errorText(error), true)
  }
})
watch(search, () => {
  visibleCount.value = 80
})
onMounted(refresh)
</script>

<template>
  <main
    class="f50-page"
    data-page-swipe-ignore
  >
    <header class="f50-heading">
      <div>
        <div class="f50-eyebrow">F50 · 猫猫增强面板</div>
        <h1>配置</h1>
        <p>管理订阅、节点和分流，保存后直接应用到设备。</p>
      </div>
      <button
        class="btn btn-sm"
        :disabled="busy"
        @click="refresh"
      >
        {{ busy ? '处理中…' : '刷新' }}
      </button>
    </header>
    <div
      v-if="message"
      class="f50-notice"
      :class="failed ? 'f50-error' : 'f50-success'"
      :role="failed ? 'alert' : 'status'"
    >
      {{ message }}
    </div>
    <template v-if="state">
      <div class="f50-stats">
        <div>
          <span>节点</span><strong>{{ enabledCount }} / {{ state.nodes.length }}</strong
          ><small>{{ state.sources.length }} 个订阅 · {{ state.manual.length }} 个手动节点</small>
        </div>
        <div>
          <span>路由</span><strong>{{ state.routes.length }} 条自定义规则</strong
          ><small>共 {{ state.ruleCount }} 条规则 · {{ state.groups.length }} 个策略组</small>
        </div>
        <div>
          <span>运行</span
          ><strong>{{ state.transport }} · {{ state.advanced['mixed-port'] || 7890 }}</strong
          ><small>配置已持久化 · 管理服务 {{ state.version }}</small>
        </div>
      </div>
      <div
        v-if="state.drift"
        class="f50-notice f50-error"
        role="alert"
      >
        设备配置已在其他工具中修改。请先在“高级”中采用设备当前配置，再继续编辑。
      </div>
      <nav
        class="f50-tabs"
        aria-label="配置分类"
      >
        <button
          v-for="item in tabs"
          :key="item.id"
          class="btn btn-sm"
          :class="tab === item.id ? 'btn-primary' : 'btn-ghost'"
          :aria-current="tab === item.id ? 'page' : undefined"
          @click="tab = item.id"
        >
          {{ item.label }}
        </button>
      </nav>
      <fieldset
        :disabled="busy"
        class="f50-fieldset"
      >
        <GroupManager
          v-if="tab === 'groups'"
          :state="state"
          :busy="busy"
          :error="failed ? message : ''"
          :apply="run"
          @clear-error="notice('')"
        />
        <template v-if="tab === 'nodes'">
          <div class="f50-grid">
            <section class="f50-card">
              <h2>应用模式</h2>
              <div class="f50-actions">
                <button
                  class="btn btn-sm"
                  :class="state.mode === 'simple' ? 'btn-primary' : ''"
                  @click="switchMode('simple')"
                >
                  简单模式</button
                ><button
                  class="btn btn-sm"
                  :class="state.mode === 'full' ? 'btn-primary' : ''"
                  @click="switchMode('full')"
                >
                  完整模式
                </button>
              </div>
              <p>简单模式统一选择节点；完整模式保留配置中的策略组与分流规则。</p>
            </section>
            <section class="f50-card">
              <h2>核心操作</h2>
              <div class="f50-actions">
                <button
                  class="btn btn-sm"
                  @click="run({ op: 'reload' })"
                >
                  重载配置</button
                ><button
                  class="btn btn-sm"
                  @click="run({ op: 'restart' })"
                >
                  重启核心</button
                ><RouterLink
                  class="btn btn-sm btn-ghost"
                  to="/logs"
                  >查看日志</RouterLink
                >
              </div>
              <p>F50 通过猫猫的 TProxy 服务接管流量。</p>
            </section>
          </div>
          <section class="f50-card">
            <div class="f50-card-head">
              <div>
                <h2>机场订阅</h2>
                <p>支持 Clash YAML、Base64 和分享链接订阅；按设定间隔自动更新，失败时保留缓存。</p>
              </div>
              <button
                class="btn btn-sm btn-primary"
                @click="editSource()"
              >
                导入订阅
              </button>
            </div>
            <p
              v-if="!state.sources.length"
              class="f50-empty"
            >
              还没有订阅。可以导入链接，也可以在下方手动添加节点。
            </p>
            <article
              v-for="source in state.sources"
              :key="source.id"
              class="f50-row"
            >
              <div class="f50-grow">
                <strong>{{ source.name }}</strong
                ><span class="badge badge-sm">{{
                  source.mode === 'pool' ? '节点池' : '完整配置'
                }}</span>
                <p>
                  {{ source.mode === 'pool' ? `${source.nodeCount} 个节点 · ` : '' }}每
                  {{ source.intervalHours }} 小时更新 · {{ formatTime(source.updatedAt) }}
                </p>
                <p
                  v-if="source.lastError"
                  class="text-error"
                >
                  {{ source.lastError }}
                </p>
              </div>
              <label class="f50-inline"
                ><input
                  type="checkbox"
                  class="toggle toggle-sm"
                  :checked="source.enabled"
                  :aria-label="`${source.name} ${source.mode === 'full' ? '自动更新' : '启用'}`"
                  @change="run({ op: 'toggle-source', id: source.id, enabled: !source.enabled })"
                />{{ source.mode === 'full' ? '自动更新' : '启用' }}</label
              >
              <div class="f50-actions">
                <button
                  class="btn btn-xs"
                  :disabled="!source.url"
                  @click="run({ op: 'refresh-source', id: source.id })"
                >
                  更新</button
                ><button
                  class="btn btn-xs"
                  @click="editSource(source)"
                >
                  编辑订阅</button
                ><button
                  class="btn btn-xs btn-ghost text-error"
                  :disabled="state.active === source.id"
                  @click="remove('delete-source', source.id)"
                >
                  删除
                </button>
              </div>
            </article>
          </section>
          <section class="f50-card">
            <h2>完整配置</h2>
            <p>完整配置可保留订阅自带的策略组和规则，也支持本地 YAML。使用后会先校验并自动备份。</p>
            <article class="f50-row">
              <div class="f50-grow">
                <strong>设备原有配置</strong
                ><span
                  v-if="state.active === 'current'"
                  class="badge badge-primary badge-sm"
                  >当前使用</span
                >
                <p>保留安装增强面板时的节点、规则和运行参数。</p>
              </div>
              <div class="f50-actions">
                <button
                  class="btn btn-xs"
                  :disabled="state.active === 'current'"
                  @click="run({ op: 'activate', id: 'current' })"
                >
                  使用</button
                ><button
                  class="btn btn-xs"
                  @click="editBase()"
                >
                  编辑
                </button>
              </div>
            </article>
            <article
              v-for="source in state.sources.filter((s) => s.mode === 'full')"
              :key="source.id"
              class="f50-row"
            >
              <div class="f50-grow">
                <strong>{{ source.name }}</strong
                ><span
                  v-if="state.active === source.id"
                  class="badge badge-primary badge-sm"
                  >当前使用</span
                >
              </div>
              <div class="f50-actions">
                <button
                  class="btn btn-xs"
                  :disabled="state.active === source.id"
                  @click="run({ op: 'activate', id: source.id })"
                >
                  使用</button
                ><button
                  class="btn btn-xs"
                  @click="editBase(source.id)"
                >
                  编辑配置
                </button>
              </div>
            </article>
          </section>
          <section class="f50-card">
            <div class="f50-card-head">
              <div>
                <h2>节点池</h2>
                <p>新增订阅和手动节点会合并到首个手动选择组。停用节点会同步移出策略组。</p>
              </div>
              <input
                v-model="search"
                class="input input-sm f50-search"
                placeholder="搜索节点或来源"
                aria-label="搜索节点或来源"
              />
            </div>
            <div class="f50-table">
              <table>
                <thead>
                  <tr>
                    <th>启用</th>
                    <th>节点</th>
                    <th>协议</th>
                    <th>来源</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="node in filteredNodes.slice(0, visibleCount)"
                    :key="node.name"
                  >
                    <td>
                      <input
                        type="checkbox"
                        class="checkbox checkbox-sm"
                        :checked="node.enabled"
                        :disabled="!node.available"
                        :aria-label="`启用 ${node.name}`"
                        @change="toggleNode(node.name, !node.enabled)"
                      />
                    </td>
                    <td>{{ node.name }}</td>
                    <td>
                      <span class="badge badge-ghost badge-sm">{{ node.type }}</span>
                    </td>
                    <td>{{ node.source }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div class="f50-actions f50-footer">
              <span class="f50-muted">{{ filteredNodes.length }} 个匹配节点</span
              ><button
                v-if="visibleCount < filteredNodes.length"
                class="btn btn-sm"
                @click="visibleCount += 80"
              >
                显示更多
              </button>
            </div>
          </section>
          <section class="f50-card">
            <div class="f50-card-head">
              <h2>手动节点</h2>
              <button
                class="btn btn-sm"
                @click="editManual()"
              >
                新增节点
              </button>
            </div>
            <p>支持 VMess、VLESS、SS、Trojan、Hysteria2、TUIC 链接，或直接粘贴 Clash YAML。</p>
            <p
              v-if="!state.manual.length"
              class="f50-empty"
            >
              暂无手动节点
            </p>
            <article
              v-for="node in state.manual"
              :key="node.id"
              class="f50-row"
            >
              <div class="f50-grow">
                <strong>{{ node.node.name }}</strong>
                <p>{{ node.node.type }}</p>
              </div>
              <input
                type="checkbox"
                class="toggle toggle-sm"
                :checked="node.enabled"
                :aria-label="`启用手动节点 ${node.node.name}`"
                @change="run({ op: 'toggle-manual', id: node.id, enabled: !node.enabled })"
              /><button
                class="btn btn-xs"
                @click="editManual(node)"
              >
                编辑</button
              ><button
                class="btn btn-xs btn-ghost text-error"
                @click="remove('delete-manual', node.id)"
              >
                删除
              </button>
            </article>
          </section>
          <section class="f50-card">
            <div class="f50-card-head">
              <div>
                <h2>链式代理</h2>
                <p>连接先经过入口节点，再从出口节点访问目标。</p>
              </div>
              <button
                class="btn btn-sm"
                @click="editChain()"
              >
                新增链式代理
              </button>
            </div>
            <p
              v-if="!state.chains.length"
              class="f50-empty"
            >
              暂无链式代理
            </p>
            <article
              v-for="chain in state.chains"
              :key="chain.id"
              class="f50-row"
            >
              <div class="f50-grow">
                <strong>{{ chain.name }}</strong>
                <p>{{ chain.via }} → {{ chain.exit }}</p>
              </div>
              <input
                type="checkbox"
                class="toggle toggle-sm"
                :checked="chain.enabled"
                :aria-label="`启用链式代理 ${chain.name}`"
                @change="run({ op: 'toggle-chain', id: chain.id, enabled: !chain.enabled })"
              /><button
                class="btn btn-xs"
                @click="editChain(chain)"
              >
                编辑</button
              ><button
                class="btn btn-xs btn-ghost text-error"
                @click="remove('delete-chain', chain.id)"
              >
                删除
              </button>
            </article>
          </section>
        </template>
        <section
          v-if="tab === 'routes'"
          class="f50-card"
        >
          <div class="f50-card-head">
            <div>
              <h2>自定义路由规则</h2>
              <p>自定义规则优先于原配置。选择 DIRECT 直连，或指定代理策略组。</p>
            </div>
            <div class="f50-actions">
              <button
                class="btn btn-sm"
                @click="addRoute"
              >
                添加规则</button
              ><button
                class="btn btn-primary btn-sm"
                @click="run({ op: 'routes', rules: routes })"
              >
                保存路由
              </button>
            </div>
          </div>
          <p
            v-if="!routes.length"
            class="f50-empty"
          >
            还没有自定义规则
          </p>
          <div
            v-for="(rule, index) in routes"
            :key="index"
            class="f50-route-row"
          >
            <select
              v-model="rule.type"
              class="select select-sm"
              aria-label="规则类型"
            >
              <option
                v-for="type in [
                  'DOMAIN',
                  'DOMAIN-SUFFIX',
                  'DOMAIN-KEYWORD',
                  'IP-CIDR',
                  'IP-CIDR6',
                  'SRC-IP-CIDR',
                  'DST-PORT',
                  'SRC-PORT',
                  'GEOIP',
                  'GEOSITE',
                  'RULE-SET',
                ]"
                :key="type"
              >
                {{ type }}
              </option></select
            ><input
              v-model="rule.value"
              class="input input-sm"
              placeholder="example.com 或 192.0.2.0/24"
              aria-label="匹配内容"
            /><select
              v-model="rule.target"
              class="select select-sm"
              aria-label="路由目标"
            >
              <option
                v-for="target in targets"
                :key="target"
              >
                {{ target }}
              </option></select
            ><label class="f50-inline"
              ><input
                v-model="rule.noResolve"
                type="checkbox"
                class="checkbox checkbox-xs"
              />不解析</label
            ><button
              class="btn btn-xs btn-ghost text-error"
              aria-label="删除规则"
              @click="routes.splice(index, 1)"
            >
              删除
            </button>
          </div>
        </section>
        <template v-if="tab === 'dns'">
          <section class="f50-card">
            <div class="f50-card-head">
              <h2>DNS 配置</h2>
              <button
                class="btn btn-primary btn-sm"
                @click="saveDNS"
              >
                保存 DNS 配置
              </button>
            </div>
            <div class="f50-actions">
              <label class="f50-inline"
                >DNS 模式<select
                  v-model="dns['enhanced-mode']"
                  class="select select-sm"
                >
                  <option value="redir-host">Redir Host</option>
                  <option value="fake-ip">Fake IP</option>
                </select></label
              ><label
                v-for="[key, label] in [
                  ['enable', '启用 DNS'],
                  ['ipv6', 'IPv6'],
                  ['respect-rules', '遵循规则'],
                  ['use-hosts', '使用 hosts'],
                ]"
                :key="key"
                class="f50-inline"
                ><input
                  v-model="dns[key!]"
                  type="checkbox"
                  class="toggle toggle-sm"
                />{{ label }}</label
              >
            </div>
            <div class="f50-grid f50-space">
              <label
                v-for="key in dnsFields"
                :key="key"
                class="f50-label"
                >{{ dnsLabels[key]
                }}<textarea
                  v-model="dnsLists[key]"
                  class="textarea f50-code"
                  rows="3"
                  placeholder="每行一个 DNS 地址"
                ></textarea>
              </label>
            </div>
            <details class="f50-space">
              <summary>DNS 策略、Hosts 与扩展设置</summary>
              <p>
                使用 JSON 对象编辑 nameserver-policy、fake-ip-filter、fallback-filter
                等字段。监听端口由 F50 管理。
              </p>
              <textarea
                v-model="dnsExtra"
                class="textarea f50-code f50-wide"
                rows="12"
                aria-label="DNS 扩展设置"
              ></textarea>
            </details>
          </section>
          <section class="f50-card">
            <h2>DNS 查询</h2>
            <div class="f50-actions">
              <input
                v-model="dnsDomain"
                class="input input-sm"
                aria-label="DNS 查询域名"
              /><select
                v-model="dnsType"
                class="select select-sm"
                aria-label="DNS 查询类型"
              >
                <option>A</option>
                <option>AAAA</option>
                <option>TXT</option>
                <option>CNAME</option></select
              ><button
                class="btn btn-sm"
                @click="lookupDNS"
              >
                查询
              </button>
            </div>
            <pre
              v-if="dnsResult"
              class="f50-code f50-output"
              >{{ dnsResult }}</pre>
          </section>
        </template>
        <template v-if="tab === 'advanced'">
          <section class="f50-card">
            <div class="f50-card-head">
              <h2>运行设置</h2>
              <button
                class="btn btn-primary btn-sm"
                @click="run({ op: 'advanced', values: advanced })"
              >
                保存运行设置
              </button>
            </div>
            <div class="f50-grid">
              <label
                v-for="[key, label] in [
                  ['mixed-port', 'Mixed 端口'],
                  ['port', 'HTTP 端口'],
                  ['socks-port', 'SOCKS 端口'],
                ]"
                :key="key"
                class="f50-label"
                >{{ label
                }}<input
                  v-model.number="advanced[key!]"
                  type="number"
                  min="0"
                  max="65535"
                  class="input input-sm" /></label
              ><label class="f50-label"
                >日志级别<select
                  v-model="advanced['log-level']"
                  class="select select-sm"
                >
                  <option
                    v-for="level in ['debug', 'info', 'warning', 'error', 'silent']"
                    :key="level"
                  >
                    {{ level }}
                  </option>
                </select></label
              >
            </div>
            <div class="f50-actions f50-space">
              <label
                v-for="[key, label] in [
                  ['allow-lan', '允许局域网'],
                  ['ipv6', 'IPv6'],
                  ['unified-delay', '统一延迟'],
                  ['tcp-concurrent', 'TCP 并发'],
                ]"
                :key="key"
                class="f50-inline"
                ><input
                  v-model="advanced[key!]"
                  type="checkbox"
                  class="toggle toggle-sm"
                />{{ label }}</label
              >
            </div>
            <p>TProxy 和 DNS 监听端口由猫猫的透明代理规则管理。</p>
          </section>
          <section class="f50-card">
            <h2>维护</h2>
            <div class="f50-actions">
              <button
                class="btn btn-sm"
                @click="coreAction(flushDNSCache, 'DNS 缓存已清空')"
              >
                清空 DNS 缓存</button
              ><button
                class="btn btn-sm"
                @click="coreAction(flushFakeIP, 'Fake IP 缓存已清空')"
              >
                清空 Fake IP</button
              ><button
                class="btn btn-sm"
                @click="editBase()"
              >
                编辑基础 YAML</button
              ><button
                class="btn btn-sm"
                @click="adopt"
              >
                采用设备当前配置
              </button>
            </div>
          </section>
          <section class="f50-card">
            <div class="f50-card-head">
              <div>
                <h2>配置备份</h2>
                <p>每次变更前自动备份，保留最近 20 份。恢复前也会备份当前状态。</p>
              </div>
              <button
                class="btn btn-sm"
                @click="loadBackups"
              >
                刷新备份
              </button>
            </div>
            <p
              v-if="!backups.length"
              class="f50-empty"
            >
              首次修改后会在这里显示备份
            </p>
            <article
              v-for="backup in backups"
              :key="backup.id"
              class="f50-row"
            >
              <div class="f50-grow">
                <strong>{{ formatTime(backup.createdAt) }}</strong>
                <p>{{ Math.ceil(backup.bytes / 1024) }} KiB</p>
              </div>
              <button
                class="btn btn-xs"
                @click="restore(backup)"
              >
                恢复
              </button>
            </article>
          </section>
        </template>
        <section
          v-if="tab === 'resources'"
          class="f50-card"
        >
          <div class="f50-card-head">
            <div>
              <h2>外部资源</h2>
              <p>管理 GEO 数据库。代理集合与规则集合可在“代理”“规则”页面刷新。</p>
            </div>
            <button
              class="btn btn-primary btn-sm"
              @click="run({ op: 'resources', values: { ...resources, 'geox-url': resourceURLs } })"
            >
              保存资源设置
            </button>
          </div>
          <div class="f50-grid">
            <label
              v-for="[key, label] in [
                ['geoip', 'GeoIP 数据库'],
                ['geosite', 'GeoSite 数据库'],
                ['mmdb', 'MMDB 数据库'],
                ['asn', 'ASN 数据库'],
              ]"
              :key="key"
              class="f50-label"
              >{{ label
              }}<input
                v-model="resourceURLs[key!]"
                class="input input-sm"
                placeholder="https://…"
            /></label>
          </div>
          <div class="f50-actions f50-space">
            <label class="f50-inline"
              ><input
                v-model="resources['geo-auto-update']"
                type="checkbox"
                class="toggle toggle-sm"
              />自动更新</label
            ><label class="f50-inline"
              >更新间隔（小时）<input
                v-model.number="resources['geo-update-interval']"
                class="input input-sm w-24"
                type="number"
                min="1" /></label
            ><button
              class="btn btn-sm"
              @click="coreAction(updateGeoData, 'GEO 更新完成')"
            >
              立即更新 GEO
            </button>
          </div>
        </section>
        <section
          v-if="tab === 'preview'"
          class="f50-card"
        >
          <div class="f50-card-head">
            <div>
              <h2>最终配置预览</h2>
              <p>导出文件包含节点凭据，请妥善保管。</p>
            </div>
            <div class="f50-actions">
              <button
                class="btn btn-sm"
                @click="loadPreview"
              >
                刷新预览</button
              ><button
                class="btn btn-sm btn-primary"
                :disabled="!preview"
                @click="downloadPreview"
              >
                下载 YAML
              </button>
            </div>
          </div>
          <textarea
            :value="preview"
            readonly
            class="textarea f50-code f50-preview"
            aria-label="最终配置预览"
            spellcheck="false"
          ></textarea>
        </section>
      </fieldset>
    </template>
    <div
      v-if="editor"
      class="f50-modal"
      role="dialog"
      aria-modal="true"
      :aria-label="title"
      @click.self="!busy && (editor = null)"
    >
      <form
        class="f50-dialog"
        @submit.prevent="submitEditor"
      >
        <div class="f50-card-head">
          <h2>{{ title }}</h2>
          <button
            class="btn btn-sm btn-ghost"
            type="button"
            :disabled="busy"
            aria-label="关闭编辑器"
            @click="editor = null"
          >
            关闭
          </button>
        </div>
        <fieldset
          :disabled="busy"
          class="f50-editor-fields"
        >
          <template v-if="editor === 'source'"
            ><label class="f50-label"
              >名称<input
                v-model="form.name"
                class="input"
                required
                maxlength="100" /></label
            ><label class="f50-label"
              >订阅链接<input
                v-model="form.url"
                type="url"
                class="input"
                placeholder="https://…（本地 YAML 可留空）" /></label
            ><label class="f50-label"
              >导入方式<select
                v-model="form.mode"
                class="select"
              >
                <option value="pool">导入节点池（合并节点）</option>
                <option value="full">保存为完整配置（保留分流）</option>
              </select></label
            >
            <div class="f50-grid">
              <label class="f50-label"
                >User-Agent<input
                  v-model="form.userAgent"
                  class="input"
                  placeholder="默认 clash.meta/f50-cat-dashboard" /></label
              ><label class="f50-label"
                >更新间隔（小时）<input
                  v-model.number="form.intervalHours"
                  type="number"
                  min="1"
                  max="720"
                  class="input"
                  required
              /></label>
            </div>
            <label class="f50-inline"
              ><input
                v-model="form.enabled"
                type="checkbox"
                class="toggle toggle-sm"
              />{{ form.mode === 'full' ? '自动更新订阅' : '启用节点' }}</label
            ><template v-if="form.mode === 'full'"
              ><label class="f50-label"
                >本地 YAML<input
                  type="file"
                  accept=".yaml,.yml,.txt"
                  class="file-input file-input-sm"
                  @change="readFile" /></label
              ><textarea
                v-if="!form.url"
                v-model="form.content"
                class="textarea f50-code"
                rows="10"
                placeholder="粘贴完整 Clash YAML"
              ></textarea></template
          ></template>
          <template v-if="editor === 'manual'"
            ><p>可粘贴多个分享链接、Base64 订阅内容、单个节点 YAML 或含 proxies 的 YAML。</p>
            <textarea
              v-model="form.content"
              required
              class="textarea f50-code"
              rows="14"
              aria-label="节点内容"
              placeholder="vmess://… 或 Clash YAML"
              spellcheck="false"
            ></textarea>
          </template>
          <template v-if="editor === 'chain'"
            ><label class="f50-label"
              >链式代理名称<input
                v-model="form.name"
                required
                class="input" /></label
            ><label class="f50-label"
              >入口节点<select
                v-model="form.via"
                required
                class="select"
              >
                <option
                  value=""
                  disabled
                >
                  选择入口
                </option>
                <option
                  v-for="node in enabledNodes"
                  :key="node.name"
                >
                  {{ node.name }}
                </option>
              </select></label
            ><label class="f50-label"
              >出口节点<select
                v-model="form.exit"
                required
                class="select"
              >
                <option
                  value=""
                  disabled
                >
                  选择出口
                </option>
                <option
                  v-for="node in enabledNodes"
                  :key="node.name"
                >
                  {{ node.name }}
                </option>
              </select></label
            >
            <p>流量路径：F50 → 入口 → 出口 → 目标网站。原始节点不会改变。</p></template
          >
          <template v-if="editor === 'base'"
            ><p>保存前会执行 Mihomo 校验。控制器与透明代理所需参数会自动保留。</p>
            <textarea
              v-model="form.content"
              required
              class="textarea f50-code"
              rows="20"
              aria-label="配置 YAML"
              spellcheck="false"
            ></textarea>
          </template>
          <div class="f50-actions f50-editor-footer">
            <span class="f50-muted">{{
              busy ? '正在校验并应用…' : '保存前自动备份，失败时保留原配置'
            }}</span
            ><button
              v-if="editor === 'base'"
              type="button"
              class="btn btn-sm"
              @click="validateEditor"
            >
              校验</button
            ><button
              type="submit"
              class="btn btn-primary btn-sm"
            >
              保存并应用
            </button>
          </div>
        </fieldset>
        <div
          v-if="failed && message"
          class="f50-notice f50-error"
          role="alert"
        >
          {{ message }}
        </div>
      </form>
    </div>
  </main>
</template>

<style scoped>
.f50-page {
  width: 100%;
  max-width: 1440px;
  margin: 0 auto;
  padding: 28px 28px 100px;
}
.f50-heading,
.f50-card-head,
.f50-row,
.f50-actions,
.f50-inline {
  display: flex;
  align-items: center;
  gap: 12px;
}
.f50-heading,
.f50-card-head {
  justify-content: space-between;
}
.f50-heading {
  margin-bottom: 22px;
}
.f50-eyebrow {
  font-size: 11px;
  letter-spacing: 0.08em;
  opacity: 0.6;
  margin-bottom: 6px;
}
h1 {
  font-size: 28px;
  font-weight: 700;
  line-height: 1.3;
}
h2 {
  font-size: 16px;
  font-weight: 650;
}
p {
  font-size: 12px;
  opacity: 0.65;
  margin-top: 7px;
  line-height: 1.7;
  overflow-wrap: anywhere;
}
.f50-stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
  margin-bottom: 20px;
}
.f50-stats > div,
.f50-card {
  border: 1px solid color-mix(in srgb, var(--color-base-content) 9%, transparent);
  background: var(--color-base-100);
  border-radius: 16px;
  padding: 20px;
}
.f50-stats span,
.f50-stats strong,
.f50-stats small {
  display: block;
}
.f50-stats span {
  font-size: 11px;
  opacity: 0.6;
}
.f50-stats strong {
  font-size: 19px;
  margin: 8px 0;
  font-weight: 650;
}
.f50-stats small {
  font-size: 11px;
  opacity: 0.55;
}
.f50-tabs {
  display: flex;
  gap: 6px;
  padding-bottom: 17px;
  overflow-x: auto;
}
.f50-tabs button {
  flex-shrink: 0;
}
.f50-fieldset {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 16px;
  border: 0;
  padding: 0;
}
.f50-fieldset:disabled {
  opacity: 0.7;
}
.f50-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.f50-card {
  min-width: 0;
}
.f50-actions {
  flex-wrap: wrap;
}
.f50-row {
  padding: 15px 0;
  flex-wrap: wrap;
  border-bottom: 1px solid color-mix(in srgb, var(--color-base-content) 7%, transparent);
}
.f50-row:last-child {
  border-bottom: 0;
  padding-bottom: 0;
}
.f50-row strong {
  font-size: 13px;
  overflow-wrap: anywhere;
}
.f50-row .badge {
  margin-left: 8px;
}
.f50-grow {
  flex: 1;
  min-width: 160px;
}
.f50-inline {
  font-size: 12px;
  gap: 7px;
}
.f50-label {
  display: flex;
  flex-direction: column;
  gap: 8px;
  font-size: 12px;
}
.f50-label .input,
.f50-label .select,
.f50-label .textarea {
  width: 100%;
}
.f50-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
  line-height: 1.65;
  tab-size: 2;
}
.f50-wide {
  width: 100%;
}
.f50-preview {
  width: 100%;
  height: 65vh;
  margin-top: 16px;
  white-space: pre;
}
.f50-output {
  max-height: 300px;
  overflow: auto;
  padding-top: 15px;
}
.f50-space {
  margin-top: 20px;
}
.f50-search {
  width: 240px;
  max-width: 100%;
}
.f50-table {
  overflow: auto;
  margin-top: 16px;
  max-height: 560px;
}
.f50-table table {
  border-collapse: collapse;
  width: 100%;
  text-align: left;
  font-size: 12px;
}
.f50-table th {
  font-weight: 500;
  opacity: 0.6;
  background: var(--color-base-100);
  position: sticky;
  top: 0;
}
.f50-table td,
.f50-table th {
  padding: 11px 9px;
  border-bottom: 1px solid color-mix(in srgb, var(--color-base-content) 6%, transparent);
}
.f50-table td:nth-child(2) {
  min-width: 200px;
}
.f50-footer {
  justify-content: space-between;
  margin-top: 15px;
}
.f50-muted,
.f50-empty {
  font-size: 12px;
  opacity: 0.55;
}
.f50-empty {
  padding: 24px 0 12px;
}
.f50-route-row {
  display: grid;
  grid-template-columns: 150px minmax(160px, 1fr) minmax(130px, 1fr) auto auto;
  gap: 10px;
  align-items: center;
  margin-top: 14px;
}
.f50-notice {
  font-size: 12px;
  line-height: 1.7;
  padding: 12px 16px;
  margin-bottom: 16px;
  border-radius: 10px;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.f50-error {
  background: color-mix(in srgb, var(--color-error) 12%, var(--color-base-100));
  color: var(--color-error);
}
.f50-success {
  background: color-mix(in srgb, var(--color-success) 10%, var(--color-base-100));
}
.f50-modal {
  position: fixed;
  z-index: 100;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: #0008;
  backdrop-filter: blur(3px);
}
.f50-dialog {
  width: min(880px, 100%);
  max-height: 92dvh;
  overflow: auto;
  border-radius: 18px;
  padding: 24px;
  background: var(--color-base-100);
  box-shadow: 0 24px 80px #0003;
}
.f50-editor-fields {
  display: flex;
  flex-direction: column;
  gap: 16px;
  border: 0;
  padding: 20px 0 0;
  min-width: 0;
}
.f50-editor-footer {
  justify-content: flex-end;
}
.f50-editor-footer span {
  margin-right: auto;
}
summary {
  cursor: pointer;
  font-size: 12px;
}
@media (max-width: 760px) {
  .f50-page {
    padding: 18px 14px 110px;
  }
  .f50-stats {
    grid-template-columns: 1fr;
    gap: 8px;
  }
  .f50-stats > div {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 12px 15px;
  }
  .f50-stats strong {
    font-size: 14px;
    margin: 0;
  }
  .f50-stats small {
    margin-left: auto;
    text-align: right;
  }
  .f50-grid {
    grid-template-columns: 1fr;
  }
  .f50-card {
    padding: 16px;
  }
  .f50-card-head {
    flex-wrap: wrap;
  }
  .f50-route-row {
    grid-template-columns: 1fr 1fr;
  }
  .f50-heading h1 {
    font-size: 24px;
  }
  .f50-modal {
    padding: 10px;
  }
  .f50-dialog {
    padding: 18px;
  }
}
</style>
