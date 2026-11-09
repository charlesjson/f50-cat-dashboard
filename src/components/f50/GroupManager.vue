<script setup lang="ts">
import type { F50State, GroupConfig, GroupDetail } from '@/assembly/f50'
import { computed, reactive, ref, toRaw } from 'vue'

const props = defineProps<{
  state: F50State
  busy: boolean
  error: string
  apply: (action: Record<string, unknown>) => Promise<boolean>
}>()
const emit = defineEmits<{ clearError: [] }>()
const kinds = [
  { value: 'select', label: '手动选择' },
  { value: 'url-test', label: '自动选择' },
  { value: 'fallback', label: '故障转移' },
  { value: 'load-balance', label: '负载均衡' },
]
const kindLabel = (kind: string) => kinds.find((item) => item.value === kind)?.label ?? kind
const editor = ref<'edit' | 'delete' | null>(null)
const current = ref<GroupDetail>()
const original = ref<GroupConfig>({ name: '', type: 'select' })
const groupSearch = ref('')
const memberSearch = ref('')
const memberLimit = ref(80)
const replacement = ref('')
const localError = ref('')
const form = reactive({
  name: '',
  type: 'select',
  members: [] as string[],
  providers: [] as string[],
  url: 'https://www.gstatic.com/generate_204',
  interval: 300,
  tolerance: 50,
  lazy: true,
  strategy: 'consistent-hashing',
  includeNodes: false,
  includeProviders: false,
  filter: '',
  excludeFilter: '',
})
const details = computed(() => props.state.groupDetails ?? [])
const available = computed(() => Array.isArray(props.state.groupDetails))
const groups = computed(() =>
  details.value.filter((item) =>
    item.config.name.toLowerCase().includes(groupSearch.value.toLowerCase()),
  ),
)
const candidates = computed(() => [
  ...['DIRECT', 'REJECT', 'REJECT-DROP'].map((name) => ({ name, kind: '内置策略' })),
  ...details.value
    .filter((item) => item.id !== current.value?.id)
    .map((item) => ({ name: item.config.name, kind: '策略组 · ' + kindLabel(item.config.type) })),
  ...props.state.nodes
    .filter((node) => node.enabled)
    .map((node) => ({ name: node.name, kind: node.source + ' · ' + node.type })),
])
const matchingMembers = computed(() =>
  candidates.value.filter((item) =>
    `${item.name} ${item.kind}`.toLowerCase().includes(memberSearch.value.toLowerCase()),
  ),
)
const displayedMembers = computed(() => matchingMembers.value.slice(0, memberLimit.value))
const deleteTargets = computed(() =>
  candidates.value.filter((item) => item.name !== current.value?.config.name),
)
const error = computed(() => localError.value || props.error)
const activeLabel = computed(() =>
  props.state.active === 'current'
    ? '设备原有配置'
    : (props.state.sources.find((source) => source.id === props.state.active)?.name ??
      '当前完整配置'),
)

function open(group?: GroupDetail) {
  emit('clearError')
  current.value = group
  original.value = group ? structuredClone(toRaw(group.config)) : { name: '', type: 'select' }
  const config = original.value
  Object.assign(form, {
    name: config.name,
    type: config.type,
    members: [...(config.proxies ?? [])],
    providers: [...(config.use ?? [])],
    url: String(config.url || 'https://www.gstatic.com/generate_204'),
    interval: Number(config.interval ?? 300),
    tolerance: Number(config.tolerance ?? 50),
    lazy: config.lazy !== false,
    strategy: String(config.strategy || 'consistent-hashing'),
    includeNodes: config['include-all'] === true || config['include-all-proxies'] === true,
    includeProviders: config['include-all'] === true || config['include-all-providers'] === true,
    filter: String(config.filter ?? ''),
    excludeFilter: String(config['exclude-filter'] ?? ''),
  })
  memberSearch.value = ''
  memberLimit.value = 80
  localError.value = ''
  editor.value = 'edit'
}
function openDelete(group: GroupDetail) {
  emit('clearError')
  current.value = group
  replacement.value = ''
  localError.value = ''
  editor.value = 'delete'
}
function close() {
  if (!props.busy) {
    editor.value = null
    emit('clearError')
  }
}
function toggleMember(name: string, selected: boolean) {
  if (selected && !form.members.includes(name)) form.members.push(name)
  else if (!selected) form.members = form.members.filter((member) => member !== name)
}
function move(index: number, offset: number) {
  const to = index + offset
  if (to < 0 || to >= form.members.length) return
  const copy = [...form.members]
  ;[copy[index], copy[to]] = [copy[to]!, copy[index]!]
  form.members = copy
}
function selectMatching() {
  for (const member of matchingMembers.value) toggleMember(member.name, true)
}
async function save() {
  localError.value = ''
  if (editor.value === 'delete') {
    if (!replacement.value) {
      localError.value = '请选择删除后的替代目标'
      return
    }
    if (
      await props.apply({
        op: 'delete-group',
        id: current.value?.id,
        replacement: replacement.value,
      })
    )
      editor.value = null
    return
  }
  if (
    !form.members.length &&
    !form.providers.length &&
    !form.includeNodes &&
    !form.includeProviders
  ) {
    localError.value = '请至少选择一个成员或开启自动包含节点'
    return
  }
  const config: GroupConfig = {
    ...original.value,
    name: form.name.trim(),
    type: form.type,
    proxies: [...form.members],
  }
  delete config['include-all']
  for (const key of [
    'url',
    'interval',
    'tolerance',
    'lazy',
    'strategy',
    'use',
    'include-all-proxies',
    'include-all-providers',
    'filter',
    'exclude-filter',
  ])
    delete config[key]
  if (form.providers.length) config.use = [...form.providers]
  if (form.includeNodes) config['include-all-proxies'] = true
  if (form.includeProviders) config['include-all-providers'] = true
  if (form.filter.trim()) config.filter = form.filter.trim()
  if (form.excludeFilter.trim()) config['exclude-filter'] = form.excludeFilter.trim()
  if (form.type !== 'select')
    Object.assign(config, {
      url: form.url.trim(),
      interval: Number(form.interval),
      lazy: form.lazy,
    })
  if (form.type === 'url-test') config.tolerance = Number(form.tolerance)
  if (form.type === 'load-balance') config.strategy = form.strategy
  if (await props.apply({ op: 'save-group', id: current.value?.id ?? '', group: config }))
    editor.value = null
}
</script>

<template>
  <section class="groups-card">
    <header class="groups-header">
      <div>
        <h2>策略组</h2>
        <p>当前配置：{{ activeLabel }}。编辑记录跟随此配置保存，订阅更新后继续应用。</p>
      </div>
      <button
        class="btn btn-primary btn-sm"
        :disabled="busy || !available || state.mode !== 'full'"
        @click="open()"
      >
        新增策略组
      </button>
    </header>
    <div
      v-if="!available"
      class="groups-info"
    >
      当前设备的配置管理服务版本较旧，请安装完整升级包后使用策略组编辑。
    </div>
    <div
      v-if="state.mode !== 'full'"
      class="groups-info"
    >
      简单模式使用固定的“节点选择”模板。请先在“节点”中切换为完整模式，再编辑策略组。
    </div>
    <div class="groups-toolbar">
      <input
        v-model="groupSearch"
        class="input input-sm"
        placeholder="搜索策略组"
        aria-label="搜索策略组"
      /><span>{{ groups.length }} 个策略组</span>
    </div>
    <p
      v-if="!groups.length"
      class="groups-empty"
    >
      没有匹配的策略组
    </p>
    <article
      v-for="group in groups"
      :key="group.id"
      class="groups-row"
      :aria-label="`策略组 ${group.config.name}`"
    >
      <div class="groups-grow">
        <div class="groups-name">
          <strong>{{ group.config.name }}</strong
          ><span class="badge badge-sm">{{ kindLabel(group.config.type) }}</span
          ><span
            v-if="group.custom"
            class="badge badge-ghost badge-sm"
            >自定义</span
          ><span
            v-else-if="group.modified"
            class="badge badge-ghost badge-sm"
            >已修改</span
          >
        </div>
        <p>
          {{ group.config.proxies?.length ?? 0 }} 个固定成员<span v-if="group.config.use?.length">
            · {{ group.config.use.length }} 个节点集合</span
          ><span v-if="group.config['include-all'] || group.config['include-all-proxies']">
            · 自动包含全部节点</span
          >
        </p>
        <p v-if="group.config.type !== 'select'">
          测试间隔 {{ group.config.interval ?? 300 }} 秒<span
            v-if="group.config.type === 'url-test'"
          >
            · 容差 {{ group.config.tolerance ?? 50 }} ms</span
          >
        </p>
      </div>
      <div class="groups-actions">
        <button
          class="btn btn-sm"
          :disabled="
            busy || state.mode !== 'full' || !kinds.some((kind) => kind.value === group.config.type)
          "
          :aria-label="`编辑策略组 ${group.config.name}`"
          @click="open(group)"
        >
          编辑</button
        ><button
          class="btn btn-sm btn-ghost text-error"
          :disabled="busy || state.mode !== 'full'"
          :aria-label="`删除策略组 ${group.config.name}`"
          @click="openDelete(group)"
        >
          删除
        </button>
      </div>
    </article>
  </section>
  <div
    v-if="editor"
    class="groups-modal"
    role="dialog"
    aria-modal="true"
    :aria-label="editor === 'delete' ? '删除策略组' : current ? '编辑策略组' : '新增策略组'"
    @click.self="close"
  >
    <form
      class="groups-dialog"
      @submit.prevent="save"
    >
      <header class="groups-header">
        <div>
          <h2>{{ editor === 'delete' ? '删除策略组' : current ? '编辑策略组' : '新增策略组' }}</h2>
          <p>{{ activeLabel }}</p>
        </div>
        <button
          type="button"
          class="btn btn-sm btn-ghost"
          :disabled="busy"
          aria-label="关闭策略组编辑器"
          @click="close"
        >
          关闭
        </button>
      </header>
      <fieldset
        :disabled="busy"
        class="groups-fields"
      >
        <template v-if="editor === 'edit'">
          <div class="groups-grid">
            <label
              >策略组名称<input
                v-model="form.name"
                required
                maxlength="80"
                class="input input-sm"
                aria-label="策略组名称" /></label
            ><label
              >策略组类型<select
                v-model="form.type"
                class="select select-sm"
                aria-label="策略组类型"
              >
                <option
                  v-for="kind in kinds"
                  :key="kind.value"
                  :value="kind.value"
                >
                  {{ kind.label }}
                </option>
              </select></label
            >
          </div>
          <p
            v-if="current && form.name.trim() !== current.config.name"
            class="groups-info"
          >
            重命名会同步更新分流规则、嵌套组及链式节点中的引用。
          </p>
          <div
            v-if="form.type !== 'select'"
            class="groups-test-settings"
          >
            <div class="groups-grid">
              <label
                >测试地址<input
                  v-model="form.url"
                  required
                  type="url"
                  class="input input-sm"
                  aria-label="策略组测试地址" /></label
              ><label
                >测试间隔（秒）<input
                  v-model.number="form.interval"
                  required
                  type="number"
                  min="1"
                  max="604800"
                  class="input input-sm"
                  aria-label="策略组测试间隔" /></label
              ><label v-if="form.type === 'url-test'"
                >延迟容差（毫秒）<input
                  v-model.number="form.tolerance"
                  required
                  type="number"
                  min="0"
                  max="10000"
                  class="input input-sm"
                  aria-label="策略组延迟容差" /></label
              ><label v-if="form.type === 'load-balance'"
                >负载均衡策略<select
                  v-model="form.strategy"
                  class="select select-sm"
                  aria-label="负载均衡策略"
                >
                  <option value="consistent-hashing">一致性哈希</option>
                  <option value="round-robin">轮询</option>
                  <option value="sticky-sessions">会话保持</option>
                </select></label
              >
            </div>
            <label class="groups-check"
              ><input
                v-model="form.lazy"
                type="checkbox"
                class="checkbox checkbox-sm"
              />仅在使用时进行健康检查</label
            >
          </div>
          <section class="groups-members">
            <div class="groups-section-header">
              <h3>
                组内成员 <span>{{ form.members.length }}</span>
              </h3>
              <button
                type="button"
                class="btn btn-xs btn-ghost"
                :disabled="!form.members.length"
                @click="form.members = []"
              >
                清空选择
              </button>
            </div>
            <p>支持节点、内置策略和其他策略组。故障转移按列表顺序优先选择可用成员。</p>
            <ol
              v-if="form.members.length"
              class="groups-selected"
            >
              <li
                v-for="(name, index) in form.members"
                :key="name"
              >
                <span class="groups-order">{{ index + 1 }}</span
                ><span class="groups-member-name">{{ name }}</span
                ><button
                  type="button"
                  class="btn btn-xs btn-ghost"
                  :disabled="index === 0"
                  :aria-label="`上移成员 ${name}`"
                  @click="move(index, -1)"
                >
                  ↑</button
                ><button
                  type="button"
                  class="btn btn-xs btn-ghost"
                  :disabled="index === form.members.length - 1"
                  :aria-label="`下移成员 ${name}`"
                  @click="move(index, 1)"
                >
                  ↓</button
                ><button
                  type="button"
                  class="btn btn-xs btn-ghost"
                  :aria-label="`移除成员 ${name}`"
                  @click="toggleMember(name, false)"
                >
                  移除
                </button>
              </li>
            </ol>
            <div class="groups-toolbar">
              <input
                v-model="memberSearch"
                class="input input-sm"
                placeholder="搜索节点、策略组或来源"
                aria-label="搜索组内候选成员"
                @input="memberLimit = 80"
              /><button
                type="button"
                class="btn btn-xs"
                @click="selectMatching"
              >
                选择全部匹配
              </button>
            </div>
            <div class="groups-candidates">
              <label
                v-for="member in displayedMembers"
                :key="member.name"
                class="groups-candidate"
                ><input
                  type="checkbox"
                  class="checkbox checkbox-sm"
                  :checked="form.members.includes(member.name)"
                  :aria-label="`选择成员 ${member.name}`"
                  @change="toggleMember(member.name, ($event.target as HTMLInputElement).checked)"
                /><span
                  ><strong>{{ member.name }}</strong
                  ><small>{{ member.kind }}</small></span
                ></label
              >
              <p v-if="!matchingMembers.length">没有匹配的候选成员</p>
              <button
                v-if="memberLimit < matchingMembers.length"
                type="button"
                class="btn btn-sm"
                @click="memberLimit += 80"
              >
                显示更多候选成员
              </button>
            </div>
          </section>
          <details class="groups-extra">
            <summary>自动包含与节点过滤</summary>
            <p>手动编辑后按所选列表保存。开启自动包含全部节点，可让今后新增的节点自动入组。</p>
            <div class="groups-actions">
              <label class="groups-check"
                ><input
                  v-model="form.includeNodes"
                  type="checkbox"
                  class="checkbox checkbox-sm"
                />自动包含全部节点</label
              ><label class="groups-check"
                ><input
                  v-model="form.includeProviders"
                  type="checkbox"
                  class="checkbox checkbox-sm"
                />自动包含全部节点集合</label
              >
            </div>
            <div
              v-if="state.providerNames?.length"
              class="groups-providers"
            >
              <span>使用节点集合</span
              ><label
                v-for="provider in state.providerNames"
                :key="provider"
                class="groups-check"
                ><input
                  v-model="form.providers"
                  :value="provider"
                  type="checkbox"
                  class="checkbox checkbox-sm"
                />{{ provider }}</label
              >
            </div>
            <div class="groups-grid">
              <label
                >包含过滤（正则）<input
                  v-model="form.filter"
                  class="input input-sm"
                  aria-label="策略组包含过滤"
                  placeholder="例如 香港|日本" /></label
              ><label
                >排除过滤（正则）<input
                  v-model="form.excludeFilter"
                  class="input input-sm"
                  aria-label="策略组排除过滤"
                  placeholder="例如 到期|剩余流量"
              /></label>
            </div>
          </details>
        </template>
        <template v-else>
          <div class="groups-info">
            <strong>{{ current?.config.name }}</strong>
            <p>删除后，引用此组的规则、嵌套组和链式节点会改用下方选择的目标。</p>
          </div>
          <div v-if="current?.references.length">
            <h3>当前引用</h3>
            <ul class="groups-references">
              <li
                v-for="reference in current.references"
                :key="reference"
              >
                {{ reference }}
              </li>
            </ul>
          </div>
          <label
            >删除后的替代目标<select
              v-model="replacement"
              required
              class="select select-sm"
              aria-label="删除后的替代目标"
            >
              <option
                value=""
                disabled
              >
                请选择替代目标
              </option>
              <option
                v-for="target in deleteTargets"
                :key="target.name"
                :value="target.name"
              >
                {{ target.name }}
              </option>
            </select></label
          >
        </template>
        <div
          v-if="error"
          class="groups-error"
          role="alert"
        >
          {{ error }}
        </div>
        <footer class="groups-footer">
          <span>{{ busy ? '正在校验并应用…' : '保存前自动备份，校验失败保留原配置' }}</span
          ><button
            type="button"
            class="btn btn-sm"
            @click="close"
          >
            取消</button
          ><button
            type="submit"
            class="btn btn-sm"
            :class="editor === 'delete' ? 'btn-error' : 'btn-primary'"
          >
            {{ editor === 'delete' ? '确认删除策略组' : '保存策略组' }}
          </button>
        </footer>
      </fieldset>
    </form>
  </div>
</template>

<style scoped>
.groups-card {
  padding: 22px;
  border-radius: 16px;
  background: var(--color-base-100);
  border: 1px solid color-mix(in srgb, var(--color-base-content) 9%, transparent);
}
.groups-header,
.groups-row,
.groups-actions,
.groups-toolbar,
.groups-section-header,
.groups-footer {
  display: flex;
  gap: 12px;
  align-items: center;
}
.groups-header,
.groups-section-header {
  justify-content: space-between;
}
h2 {
  font-size: 16px;
  font-weight: 650;
}
h3 {
  font-size: 13px;
  font-weight: 600;
}
h3 span {
  opacity: 0.5;
  margin-left: 5px;
}
p {
  font-size: 12px;
  opacity: 0.65;
  line-height: 1.7;
  margin-top: 7px;
  overflow-wrap: anywhere;
}
.groups-toolbar {
  flex-wrap: wrap;
  margin: 18px 0 8px;
}
.groups-toolbar .input {
  min-width: 180px;
  flex: 1;
}
.groups-toolbar > span {
  font-size: 12px;
  opacity: 0.6;
}
.groups-row {
  padding: 17px 0;
  border-bottom: 1px solid color-mix(in srgb, var(--color-base-content) 7%, transparent);
}
.groups-row:last-child {
  border-bottom: 0;
}
.groups-grow {
  flex: 1;
  min-width: 0;
}
.groups-name {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.groups-name strong {
  font-size: 13px;
  overflow-wrap: anywhere;
}
.groups-actions {
  flex-wrap: wrap;
}
.groups-info {
  border-radius: 10px;
  padding: 12px 14px;
  background: var(--color-base-200);
  font-size: 12px;
  line-height: 1.7;
  margin-top: 12px;
}
.groups-empty {
  padding: 24px 0;
}
.groups-modal {
  position: fixed;
  z-index: 110;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
  background: #0008;
  backdrop-filter: blur(3px);
}
.groups-dialog {
  width: min(940px, 100%);
  max-height: 92dvh;
  overflow: auto;
  border-radius: 18px;
  padding: 24px;
  background: var(--color-base-100);
  box-shadow: 0 24px 80px #0003;
}
.groups-fields {
  border: 0;
  padding: 20px 0 0;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.groups-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
}
label:not(.groups-candidate):not(.groups-check) {
  display: flex;
  flex-direction: column;
  gap: 7px;
  font-size: 12px;
}
label .input,
label .select {
  width: 100%;
}
.groups-check {
  display: flex;
  gap: 8px;
  align-items: center;
  font-size: 12px;
}
.groups-test-settings {
  display: grid;
  gap: 15px;
}
.groups-selected {
  margin-top: 12px;
  max-height: 220px;
  overflow: auto;
  border: 1px solid color-mix(in srgb, var(--color-base-content) 9%, transparent);
  border-radius: 10px;
}
.groups-selected li {
  display: flex;
  gap: 8px;
  align-items: center;
  padding: 7px 10px;
}
.groups-selected li + li {
  border-top: 1px solid color-mix(in srgb, var(--color-base-content) 5%, transparent);
}
.groups-order {
  font-size: 10px;
  opacity: 0.45;
  width: 20px;
  flex-shrink: 0;
}
.groups-member-name {
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
  font-size: 12px;
}
.groups-candidates {
  max-height: 260px;
  overflow-y: auto;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 4px;
  border: 1px solid color-mix(in srgb, var(--color-base-content) 9%, transparent);
  border-radius: 10px;
  padding: 8px;
}
.groups-candidate {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 8px;
  border-radius: 7px;
  cursor: pointer;
}
.groups-candidate:hover {
  background: var(--color-base-200);
}
.groups-candidate span {
  min-width: 0;
}
.groups-candidate strong {
  font-size: 12px;
  font-weight: 500;
  overflow-wrap: anywhere;
}
.groups-candidate small {
  display: block;
  font-size: 10px;
  opacity: 0.5;
  margin-top: 3px;
}
.groups-candidate .checkbox {
  flex-shrink: 0;
}
.groups-extra summary {
  cursor: pointer;
  font-size: 12px;
}
.groups-extra .groups-grid,
.groups-extra .groups-actions,
.groups-providers {
  margin-top: 14px;
}
.groups-providers {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  align-items: center;
  font-size: 12px;
}
.groups-references {
  max-height: 180px;
  overflow: auto;
  font-size: 12px;
  opacity: 0.7;
  list-style: disc;
  padding: 10px 20px;
  line-height: 1.8;
}
.groups-error {
  border-radius: 10px;
  padding: 12px 14px;
  color: var(--color-error);
  background: color-mix(in srgb, var(--color-error) 10%, var(--color-base-100));
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
}
.groups-footer {
  justify-content: flex-end;
  flex-wrap: wrap;
  border-top: 1px solid color-mix(in srgb, var(--color-base-content) 9%, transparent);
  padding-top: 16px;
}
.groups-footer span {
  margin-right: auto;
  font-size: 11px;
  opacity: 0.6;
}
@media (max-width: 760px) {
  .groups-card {
    padding: 16px;
  }
  .groups-header {
    flex-wrap: wrap;
  }
  .groups-row {
    align-items: flex-start;
  }
  .groups-row .groups-actions {
    flex-direction: column;
    gap: 4px;
  }
  .groups-modal {
    padding: 10px;
  }
  .groups-dialog {
    padding: 18px;
  }
  .groups-grid,
  .groups-candidates {
    grid-template-columns: 1fr;
  }
  .groups-footer span {
    width: 100%;
  }
}
</style>
