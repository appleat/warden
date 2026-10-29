<template>
  <div>
    <!-- 未配置连接 -->
    <div v-if="!conn.configured" class="card" style="text-align: center; padding: 48px 0">
      <el-icon :size="34" style="color: var(--ink-3)"><Box /></el-icon>
      <div style="font-size: 14px; font-weight: 600; margin: 12px 0 6px">当前主机还没有配置 Nacos 连接</div>
      <div class="muted" style="font-size: 12.5px; line-height: 1.9">
        填写当前主机上 Nacos 服务端的地址与账号，即可在这里管理服务注册与配置。
        <br />
        Nacos 只监听 <code class="mono">127.0.0.1</code> 时，地址填 <code class="mono">127.0.0.1:8848</code> 即可（请求由该主机的 Agent 发起）。
      </div>
      <el-button type="primary" style="margin-top: 18px" @click="openConn">配置连接</el-button>
    </div>

    <template v-else>
      <!-- 状态条 -->
      <div class="card" style="padding: 14px 16px">
        <div class="row" style="flex-wrap: wrap">
          <span class="stat-pill" :class="status.online ? 'ok' : 'bad'">
            <span class="dot" :class="status.online ? 'green' : 'red'"></span>
            {{ status.online ? '已连接' : '连接失败' }}
          </span>
          <span class="mono" style="font-size: 12.5px">{{ conn.addr }}</span>
          <el-tag v-if="status.version" size="small" effect="plain">v{{ status.version }}</el-tag>
          <el-tag v-if="status.mode" size="small" effect="plain" type="info">
            {{ status.mode === 'standalone' ? '单机模式' : '集群模式' }}
          </el-tag>
          <el-tag v-if="status.auth_enabled" size="small" effect="plain" type="warning">已开启鉴权</el-tag>
          <span v-if="status.online && status.latency_ms" class="muted" style="font-size: 12px">
            {{ status.latency_ms }} ms
          </span>
          <span v-if="!status.online && status.error" class="muted" style="font-size: 12px">{{ status.error }}</span>
          <div class="spacer"></div>
          <span class="muted" style="font-size: 12px">命名空间</span>
          <el-select v-model="nsSel" style="width: 210px" :loading="switching" placeholder="public">
            <el-option v-for="n in nsOptions" :key="n.value" :label="n.label" :value="n.value">
              <div class="row" style="justify-content: space-between; gap: 14px">
                <span>{{ n.label }}</span>
                <span class="mono muted" style="font-size: 11.5px">
                  {{ n.value === PUBLIC_NS ? 'public' : n.value }}
                </span>
              </div>
            </el-option>
          </el-select>
          <el-button :icon="Refresh" :loading="statusLoading" @click="reloadAll">刷新</el-button>
          <el-button :icon="Tools" :loading="diagLoading" @click="openDiagnose">诊断</el-button>
          <el-button :icon="Setting" @click="openConn">连接设置</el-button>
        </div>

        <div v-if="status.online" class="grid" style="grid-template-columns: repeat(4, 1fr); margin-top: 14px">
          <div class="metric">
            <div class="metric-head"><span>服务数</span></div>
            <div class="metric-value">{{ status.service_count ?? '-' }}</div>
            <div class="metric-foot">当前服务端注册的服务总数</div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>实例数</span></div>
            <div class="metric-value">{{ status.instance_count ?? '-' }}</div>
            <div class="metric-foot">全部服务下的实例总数</div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>当前命名空间</span></div>
            <div class="metric-value" style="font-size: 17px">{{ nsLabel }}</div>
            <div class="metric-foot">{{ ns || 'public' }}</div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>服务端</span></div>
            <div class="metric-value" style="font-size: 17px">{{ status.datasource || '-' }}</div>
            <div class="metric-foot">
              {{ status.server_env || '默认环境' }}
              <span v-if="status.cpu !== undefined"> · CPU {{ Math.round((status.cpu || 0) * 100) }}%</span>
            </div>
          </div>
        </div>
      </div>

      <el-tabs v-model="tab" style="margin-top: 4px">
        <!-- 配置管理 -->
        <el-tab-pane label="配置管理" name="configs">
          <div class="card" style="padding: 0">
            <div class="toolbar" style="padding: 12px 14px 0; margin-bottom: 8px">
              <el-input
                v-model="cfgKeyword"
                placeholder="Data ID 关键字"
                clearable
                style="width: 200px"
                @keyup.enter="loadConfigs(1)"
                @clear="loadConfigs(1)"
              >
                <template #prefix><el-icon><Search /></el-icon></template>
              </el-input>
              <el-input
                v-model="cfgGroup"
                placeholder="分组（留空=全部）"
                clearable
                style="width: 168px"
                @keyup.enter="loadConfigs(1)"
                @clear="loadConfigs(1)"
              />
              <el-button size="small" @click="loadConfigs(1)">查询</el-button>
              <div class="spacer"></div>
              <el-button size="small" :icon="Refresh" @click="loadConfigs()" />
              <el-button type="primary" size="small" :icon="Plus" @click="openEditor('create')">新建配置</el-button>
            </div>
            <el-table
              :data="configs"
              v-loading="cfgLoading"
              size="small"
              max-height="calc(100vh - 402px)"
              empty-text="没有配置"
            >
              <el-table-column label="Data ID" min-width="220">
                <template #default="{ row }"><span class="mono" style="font-size: 12.5px">{{ row.data_id }}</span></template>
              </el-table-column>
              <el-table-column label="分组" width="170" show-overflow-tooltip>
                <template #default="{ row }"><span class="mono muted">{{ row.group }}</span></template>
              </el-table-column>
              <el-table-column label="格式" width="110">
                <template #default="{ row }">
                  <el-tag size="small" effect="plain">{{ row.type || 'text' }}</el-tag>
                </template>
              </el-table-column>
              <el-table-column label="应用" width="140" show-overflow-tooltip>
                <template #default="{ row }"><span class="muted">{{ row.app_name || '-' }}</span></template>
              </el-table-column>
              <el-table-column label="操作" width="150" align="right">
                <template #default="{ row }">
                  <el-button link type="primary" size="small" @click="openEditor('edit', row)">编辑</el-button>
                  <el-button link type="danger" size="small" @click="removeConfig(row)">删除</el-button>
                </template>
              </el-table-column>
            </el-table>
            <div class="row" style="padding: 8px 12px; justify-content: flex-end">
              <el-pagination
                size="small"
                layout="total, prev, pager, next"
                :total="cfgTotal"
                :page-size="cfgSize"
                :current-page="cfgPage"
                @current-change="onCfgPage"
              />
            </div>
          </div>
        </el-tab-pane>

        <!-- 服务列表 -->
        <el-tab-pane label="服务列表" name="services">
          <div class="grid" style="grid-template-columns: minmax(0, 1.02fr) minmax(0, 1fr)">
            <div class="card" style="padding: 0">
              <div class="toolbar" style="padding: 12px 14px 0; margin-bottom: 8px">
                <el-input
                  v-model="svcKeyword"
                  placeholder="服务名关键字"
                  clearable
                  style="width: 176px"
                  @keyup.enter="loadServices(1)"
                  @clear="loadServices(1)"
                >
                  <template #prefix><el-icon><Search /></el-icon></template>
                </el-input>
                <el-input
                  v-model="svcGroup"
                  placeholder="分组（留空=全部）"
                  clearable
                  style="width: 168px"
                  @keyup.enter="loadServices(1)"
                  @clear="loadServices(1)"
                />
                <div class="spacer"></div>
                <el-button size="small" :icon="Refresh" @click="loadServices()" />
              </div>
              <el-table
                :data="services"
                v-loading="svcLoading"
                size="small"
                max-height="calc(100vh - 402px)"
                empty-text="该命名空间下没有服务"
                highlight-current-row
                @row-click="selectService"
              >
                <el-table-column label="服务名" min-width="160">
                  <template #default="{ row }">
                    <span class="mono" style="font-size: 12.5px">{{ row.name }}</span>
                  </template>
                </el-table-column>
                <el-table-column label="分组" width="128" show-overflow-tooltip>
                  <template #default="{ row }"><span class="mono muted">{{ row.group_name || '-' }}</span></template>
                </el-table-column>
                <el-table-column label="实例" width="92">
                  <template #default="{ row }">
                    <span v-if="row.ip_count < 0" class="muted">-</span>
                    <span v-else class="mono">{{ row.healthy_count }}/{{ row.ip_count }}</span>
                  </template>
                </el-table-column>
                <el-table-column label="状态" width="88">
                  <template #default="{ row }">
                    <el-tag v-if="row.ip_count < 0" size="small" effect="plain" type="info">未知</el-tag>
                    <el-tag v-else-if="row.ip_count === 0" size="small" effect="light" type="warning">无实例</el-tag>
                    <el-tag v-else-if="row.healthy_count === 0" size="small" effect="light" type="danger">不健康</el-tag>
                    <el-tag v-else size="small" effect="light" type="success">正常</el-tag>
                  </template>
                </el-table-column>
              </el-table>
              <div class="row" style="padding: 8px 12px; justify-content: flex-end">
                <el-pagination
                  size="small"
                  layout="total, prev, pager, next"
                  :total="svcTotal"
                  :page-size="svcSize"
                  :current-page="svcPage"
                  @current-change="onSvcPage"
                />
              </div>
              <div class="muted" style="padding: 0 14px 10px; font-size: 11.5px; line-height: 1.7">
                数据来源：<span class="mono">{{ svcSource || '-' }}</span>
                <template v-if="svcSource === 'service-list'">
                  · catalog 接口在该版本上不可用，已回落，实例数显示为 -
                </template>
                <template v-if="!services.length && svcHint">
                  <div class="mono" style="margin-top: 4px; word-break: break-all; color: #a86a12">
                    {{ svcHint }}
                  </div>
                </template>
              </div>
            </div>

            <div class="card" style="padding: 0">
              <div class="card-title" style="padding: 14px 16px 8px; margin: 0">
                <div class="row" style="gap: 8px">
                  <span>{{ selected ? selected.name : '实例详情' }}</span>
                  <span v-if="selected" class="mono muted" style="font-size: 11.5px">{{ selected.group_name }}</span>
                </div>
                <div class="row">
                  <el-checkbox v-model="healthyOnly" size="small" :disabled="!selected" @change="loadInstances">
                    仅健康
                  </el-checkbox>
                  <el-button size="small" :icon="Refresh" :disabled="!selected" @click="loadInstances" />
                </div>
              </div>
              <div v-if="!selected" class="empty-hint">点击左侧任意服务查看实例</div>
              <template v-else>
                <el-table
                  :data="inst.items"
                  v-loading="instLoading"
                  size="small"
                  max-height="calc(100vh - 444px)"
                  empty-text="该服务暂无实例"
                >
                  <el-table-column label="地址" min-width="140">
                    <template #default="{ row }">
                      <div class="mono" style="font-size: 12.5px">{{ row.ip }}:{{ row.port }}</div>
                      <div class="muted" style="font-size: 11px">
                        {{ row.ephemeral ? '临时实例' : '持久实例' }}
                      </div>
                    </template>
                  </el-table-column>
                  <el-table-column label="健康" width="72">
                    <template #default="{ row }">
                      <el-tag size="small" :type="row.healthy ? 'success' : 'danger'" effect="light">
                        {{ row.healthy ? '健康' : '异常' }}
                      </el-tag>
                    </template>
                  </el-table-column>
                  <el-table-column label="集群" width="88" show-overflow-tooltip>
                    <template #default="{ row }">{{ row.cluster_name || 'DEFAULT' }}</template>
                  </el-table-column>
                  <el-table-column label="权重" width="64">
                    <template #default="{ row }"><span class="mono">{{ row.weight }}</span></template>
                  </el-table-column>
                  <el-table-column label="元数据" width="70">
                    <template #default="{ row }">
                      <el-tooltip v-if="metaText(row)" placement="top">
                        <template #content>
                          <div v-for="(v, k) in row.metadata" :key="k" class="mono" style="font-size: 11.5px">
                            {{ k }} = {{ v }}
                          </div>
                        </template>
                        <span class="muted" style="font-size: 11.5px; cursor: help">查看</span>
                      </el-tooltip>
                      <span v-else class="muted">-</span>
                    </template>
                  </el-table-column>
                </el-table>
                <div class="muted" style="padding: 8px 16px; font-size: 11.5px">
                  共 {{ inst.items?.length || 0 }} 个实例
                  <span v-if="inst.clusters"> · 集群 {{ inst.clusters }}</span>
                  <span v-if="inst.trigger_flag" style="color: #a86a12"> · 已触发保护阈值</span>
                </div>
              </template>
            </div>
          </div>
        </el-tab-pane>
      </el-tabs>
    </template>

    <!-- 连接设置 -->
    <el-dialog v-model="connVisible" title="Nacos 连接设置" width="520px">
      <el-form :model="connForm" label-width="96px">
        <el-form-item label="服务地址" required>
          <el-input v-model="connForm.addr" placeholder="127.0.0.1:8848" />
          <div class="muted" style="font-size: 11.5px; line-height: 1.7; margin-top: 4px">
            默认补全 <code class="mono">http://</code> 与 <code class="mono">/nacos</code> 上下文路径；
            地址由当前主机（或被管端 Agent）访问。
          </div>
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="connForm.username" placeholder="未开启鉴权可留空" autocomplete="off" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="connForm.password"
            type="password"
            show-password
            autocomplete="new-password"
            :placeholder="conn.has_password ? '已保存，留空表示不修改' : '未开启鉴权可留空'"
          />
        </el-form-item>
        <el-form-item v-if="conn.has_password" label=" ">
          <el-checkbox v-model="connForm.clear_password">清除已保存的密码</el-checkbox>
        </el-form-item>
        <el-form-item label="默认命名空间">
          <el-select v-model="connForm.namespace" style="width: 100%" filterable allow-create clearable placeholder="public">
            <el-option v-for="n in nsOptions" :key="n.value" :label="n.label" :value="n.value" />
          </el-select>
          <div class="muted" style="font-size: 11.5px; margin-top: 4px">仅用于打开页面时的默认值，可随时在顶部切换</div>
        </el-form-item>
        <el-form-item label="跳过证书校验">
          <el-switch v-model="connForm.insecure" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">HTTPS + 自签证书时开启</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="connVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveConn">保存并测试</el-button>
      </template>
    </el-dialog>

    <!-- 配置编辑 -->
    <el-dialog
      v-model="editorVisible"
      :title="editor.mode === 'create' ? '新建配置' : '编辑配置'"
      width="720px"
      top="7vh"
    >
      <el-form :model="editor" label-width="80px">
        <div class="row" style="gap: 12px; align-items: flex-start">
          <el-form-item label="Data ID" required style="flex: 1">
            <el-input v-model="editor.data_id" :disabled="editor.mode === 'edit'" placeholder="application-dev.yaml" />
          </el-form-item>
          <el-form-item label="分组" style="width: 200px">
            <el-select
              v-model="editor.group"
              :disabled="editor.mode === 'edit'"
              filterable
              allow-create
              default-first-option
              placeholder="DEFAULT_GROUP"
            >
              <el-option v-for="g in groupOptions" :key="g" :label="g" :value="g" />
            </el-select>
            <div v-if="editor.mode === 'edit'" class="muted" style="font-size: 11px; margin-top: 2px">
            </div>
          </el-form-item>
        </div>
        <el-form-item label="格式">
          <el-select v-model="editor.type" style="width: 180px">
            <el-option v-for="t in configTypes" :key="t" :label="t" :value="t" />
          </el-select>
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">
            命名空间：{{ nsLabel }}
          </span>
        </el-form-item>
        <el-form-item label="内容">
          <el-input
            v-model="editor.content"
            type="textarea"
            :rows="18"
            :disabled="editorLoading"
            :input-style="editorStyle"
            placeholder="配置内容"
          />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editorVisible = false">取消</el-button>
        <el-button type="primary" :loading="publishing" @click="publishConfig">发布</el-button>
      </template>
    </el-dialog>

    <!-- 接口兼容诊断 -->
    <el-dialog v-model="diagVisible" title="接口兼容诊断" width="800px" top="7vh">
      <div class="muted" style="font-size: 12px; line-height: 1.8; margin-bottom: 10px">
        逐个探测面板依赖的 OpenAPI，用于定位不同 Nacos 版本（1.x / 2.x / 3.x）的差异。
        当前命名空间：<span class="mono">{{ ns || 'public' }}</span>
      </div>
      <el-table :data="diagItems" v-loading="diagLoading" size="small" max-height="calc(100vh - 320px)">
        <el-table-column label="接口" min-width="230">
          <template #default="{ row }">
            <div style="font-size: 12.5px">{{ row.name }}</div>
            <div class="mono" style="font-size: 11.5px; margin-top: 2px">{{ row.path }}</div>
            <div class="muted" style="font-size: 11px; margin-top: 2px">{{ row.note }}</div>
          </template>
        </el-table-column>
        <el-table-column label="结果" width="96">
          <template #default="{ row }">
            <el-tag size="small" effect="plain" :type="row.status >= 200 && row.status < 300 ? 'success' : 'danger'">
              {{ row.status || 'ERR' }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="返回片段" min-width="330">
          <template #default="{ row }">
            <div class="mono" style="font-size: 11.5px; white-space: pre-wrap; word-break: break-all">
              {{ row.snippet || '-' }}
            </div>
          </template>
        </el-table-column>
      </el-table>
      <template #footer>
        <el-button @click="diagVisible = false">关闭</el-button>
        <el-button type="primary" :loading="diagLoading" @click="runDiagnose">重新探测</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Box, Plus, Refresh, Search, Setting, Tools } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'

const conn = ref<any>({ configured: false, addr: '', username: '', namespace: '', has_password: false })
const status = ref<any>({ online: false })
const statusLoading = ref(false)
const switching = ref(false)
const namespaces = ref<any[]>([])
// el-select 把空字符串当成「未选择」，public 命名空间（ID 为空串）需要一个哨兵值
const PUBLIC_NS = '__public__'
const nsSel = ref(PUBLIC_NS)
const ns = computed(() => (nsSel.value === PUBLIC_NS ? '' : nsSel.value))
const tab = ref('configs')

// 服务
const services = ref<any[]>([])
const svcTotal = ref(0)
const svcPage = ref(1)
const svcSize = ref(20)
const svcGroup = ref('')
const svcKeyword = ref('')
const svcSource = ref('')
const svcHint = ref('')
const svcLoading = ref(false)
const selected = ref<any>(null)
const inst = ref<any>({ items: [] })
const instLoading = ref(false)
const healthyOnly = ref(false)

// 配置
const configs = ref<any[]>([])
const cfgTotal = ref(0)
const cfgPage = ref(1)
const cfgSize = ref(20)
const cfgGroup = ref('')
const cfgKeyword = ref('')
const cfgLoading = ref(false)

// 连接设置
const connVisible = ref(false)
const saving = ref(false)
const connForm = reactive<any>({
  addr: '',
  username: '',
  password: '',
  namespace: PUBLIC_NS,
  insecure: false,
  clear_password: false
})

// 配置编辑
const editorVisible = ref(false)
const editorLoading = ref(false)
const publishing = ref(false)
const editor = reactive<any>({ mode: 'create', data_id: '', group: 'DEFAULT_GROUP', type: 'text', content: '' })

const configTypes = ['text', 'json', 'yaml', 'properties', 'xml', 'html']

// 分组下拉：从已加载的配置里累积去重（翻页会越攒越多），切命名空间时清空
const knownGroups = ref<string[]>([])
const groupOptions = computed(() => {
  const set = new Set<string>(['DEFAULT_GROUP'])
  for (const g of knownGroups.value) if (g) set.add(g)
  if (editor.group) set.add(editor.group)
  return [...set].sort()
})

const editorStyle = {
  fontFamily: "'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace",
  fontSize: '12.5px',
  lineHeight: '1.65'
}

const nsOptions = computed(() => {
  const list = namespaces.value.length
    ? namespaces.value
    : [{ namespace: '', namespace_show_name: 'public', config_count: null, quota: null }]
  return list.map((n: any) => ({
    value: n.namespace || PUBLIC_NS,
    label: n.namespace_show_name || n.namespace || 'public',
    config_count: n.config_count,
    quota: n.quota
  }))
})

const nsLabel = computed(() => {
  const hit = nsOptions.value.find((n) => n.value === nsSel.value)
  return hit?.label || (ns.value || 'public')
})

function metaText(row: any) {
  const md = row?.metadata
  if (!md || !Object.keys(md).length) return ''
  return Object.entries(md)
    .map(([k, v]) => `${k} = ${v}`)
    .join('\n')
}

// ---------- 连接 ----------

// 程序内改 nsSel（如保存连接后套用默认命名空间）时不要再触发一次重载
let suppressNsWatch = false

async function loadConn() {
  try {
    conn.value = await api<any>('/api/nacos/conn')
  } catch {
    conn.value = { configured: false }
  }
  if (!conn.value.configured) return
  suppressNsWatch = true
  nsSel.value = conn.value.namespace || PUBLIC_NS
  suppressNsWatch = false
  await reloadAll()
}

async function loadStatus() {
  statusLoading.value = true
  try {
    status.value = await api<any>('/api/nacos/status', { query: { namespace: ns.value } })
  } catch (e) {
    status.value = { online: false, error: errMsg(e) }
  } finally {
    statusLoading.value = false
  }
}

async function loadNamespaces() {
  try {
    const res = await api<any>('/api/nacos/namespaces', { query: { namespace: ns.value } })
    namespaces.value = res?.items || []
  } catch {
    // 失败时保留上一次的列表，避免下拉框突然只剩 public
  }
}

// reloadAll 会把「服务列表」和「配置管理」一起拉一遍：
// 两个页签共用同一个命名空间，切命名空间后任何一个页签都不能留旧数据。
async function reloadAll() {
  switching.value = true
  svcPage.value = 1
  cfgPage.value = 1
  knownGroups.value = []
  try {
    await loadStatus()
    if (!status.value.online) return
    await Promise.all([loadNamespaces(), loadServices(), loadConfigs()])
  } finally {
    switching.value = false
  }
}

// 直接盯住选择器的值，不依赖 el-select 的 @change（程序改动、快速连点都能覆盖到）
watch(
  nsSel,
  () => {
    if (suppressNsWatch) return
    selected.value = null
    inst.value = { items: [] }
    reloadAll()
  },
  { flush: 'sync' }
)

function openConn() {
  connForm.addr = conn.value.addr || '127.0.0.1:8848'
  connForm.username = conn.value.username || ''
  connForm.password = ''
  connForm.namespace = conn.value.namespace || PUBLIC_NS
  connForm.insecure = !!conn.value.insecure
  connForm.clear_password = false
  connVisible.value = true
}

async function saveConn() {
  if (!connForm.addr) return ElMessage.warning('请填写 Nacos 服务地址')
  saving.value = true
  try {
    await api('/api/nacos/conn', {
      method: 'PUT',
      body: {
        ...connForm,
        namespace: connForm.namespace === PUBLIC_NS ? '' : connForm.namespace
      }
    })
    connVisible.value = false
    ElMessage.success('已保存，正在测试连接')
    await loadConn()
    if (status.value.online) {
      ElMessage.success(`连接正常${status.value.version ? '（Nacos ' + status.value.version + '）' : ''}`)
    } else {
      ElMessage.warning(status.value.error || '连接失败')
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

// ---------- 接口兼容诊断 ----------

const diagVisible = ref(false)
const diagLoading = ref(false)
const diagItems = ref<any[]>([])

async function runDiagnose() {
  diagLoading.value = true
  try {
    const res = await api<any>('/api/nacos/diagnose', { query: { namespace: ns.value } })
    diagItems.value = res?.items || []
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    diagLoading.value = false
  }
}

async function openDiagnose() {
  diagVisible.value = true
  diagItems.value = []
  await runDiagnose()
}

// ---------- 服务 / 实例 ----------

async function loadServices(page?: number) {
  if (page) svcPage.value = page
  svcLoading.value = true
  try {
    const res = await api<any>('/api/nacos/services', {
      query: {
        namespace: ns.value,
        group: svcGroup.value,
        keyword: svcKeyword.value,
        page: svcPage.value,
        size: svcSize.value
      }
    })
    services.value = res?.items || []
    svcTotal.value = res?.total || 0
    svcSource.value = res?.source || ''
    svcHint.value = res?.hint || ''
    // 当前选中项已不在列表里就清空右侧
    if (selected.value && !services.value.some((s: any) => s.name === selected.value.name)) {
      selected.value = null
      inst.value = { items: [] }
    }
  } catch (e) {
    services.value = []
    svcTotal.value = 0
    ElMessage.error(errMsg(e))
  } finally {
    svcLoading.value = false
  }
}

function onSvcPage(p: number) {
  svcPage.value = p
  loadServices()
}

function selectService(row: any) {
  selected.value = row
  loadInstances()
}

async function loadInstances() {
  if (!selected.value) return
  instLoading.value = true
  try {
    inst.value = await api<any>('/api/nacos/instances', {
      query: {
        namespace: ns.value,
        group: selected.value.group_name || 'DEFAULT_GROUP',
        name: selected.value.name,
        healthy: healthyOnly.value ? 'true' : ''
      }
    })
  } catch (e) {
    inst.value = { items: [] }
    ElMessage.error(errMsg(e))
  } finally {
    instLoading.value = false
  }
}

// ---------- 配置 ----------

async function loadConfigs(page?: number) {
  if (page) cfgPage.value = page
  cfgLoading.value = true
  try {
    const res = await api<any>('/api/nacos/configs', {
      query: {
        namespace: ns.value,
        group: cfgGroup.value,
        data_id: cfgKeyword.value,
        page: cfgPage.value,
        size: cfgSize.value
      }
    })
    configs.value = res?.items || []
    cfgTotal.value = res?.total || 0
    for (const c of configs.value) {
      if (c.group && !knownGroups.value.includes(c.group)) knownGroups.value.push(c.group)
    }
  } catch (e) {
    configs.value = []
    cfgTotal.value = 0
    ElMessage.error(errMsg(e))
  } finally {
    cfgLoading.value = false
  }
}

function onCfgPage(p: number) {
  cfgPage.value = p
  loadConfigs()
}

async function openEditor(mode: 'create' | 'edit', row?: any) {
  editor.mode = mode
  editor.data_id = mode === 'edit' ? row?.data_id || '' : ''
  editor.group = mode === 'edit' ? row?.group || 'DEFAULT_GROUP' : cfgGroup.value || 'DEFAULT_GROUP'
  editor.type = row?.type || 'text'
  editor.content = ''
  editorVisible.value = true
  if (mode !== 'edit') return

  editorLoading.value = true
  try {
    const res = await api<any>('/api/nacos/config', {
      query: { namespace: ns.value, group: editor.group, data_id: editor.data_id }
    })
    editor.content = res?.content || ''
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    editorLoading.value = false
  }
}

async function publishConfig() {
  if (!editor.data_id) return ElMessage.warning('请填写 Data ID')
  if (!editor.group) return ElMessage.warning('请填写分组')
  publishing.value = true
  try {
    await api('/api/nacos/config', {
      method: 'PUT',
      body: {
        namespace: ns.value,
        group: editor.group,
        data_id: editor.data_id,
        content: editor.content,
        type: editor.type
      }
    })
    ElMessage.success('配置已发布')
    editorVisible.value = false
    await loadConfigs()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    publishing.value = false
  }
}

async function removeConfig(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定删除配置「${row.group}/${row.data_id}」吗？该操作不可恢复。`,
      '删除配置',
      { type: 'warning', confirmButtonText: '删除', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }
  try {
    await api('/api/nacos/config', {
      method: 'DELETE',
      query: { namespace: ns.value, group: row.group, data_id: row.data_id }
    })
    ElMessage.success('已删除')
    await loadConfigs()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

// ---------- 生命周期 ----------

let statusTimer: any = null

onMounted(async () => {
  await loadConn()
  statusTimer = setInterval(() => {
    if (!conn.value.configured || document.hidden) return
    loadStatus()
  }, 30000)
})

onBeforeUnmount(() => {
  if (statusTimer) clearInterval(statusTimer)
})
</script>
