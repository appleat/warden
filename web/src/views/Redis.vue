<template>
  <div>
    <!-- 未配置连接 -->
    <div v-if="!conn.configured" class="card" style="text-align: center; padding: 48px 0">
      <el-icon :size="34" style="color: var(--ink-3)"><Coin /></el-icon>
      <div style="font-size: 14px; font-weight: 600; margin: 12px 0 6px">当前主机还没有配置 Redis 连接</div>
      <div class="muted" style="font-size: 12.5px; line-height: 1.9">
        填写当前主机上 Redis 的地址与密码，即可在这里浏览键、查看运行指标与执行命令。
        <br />
        Redis 只监听 <code class="mono">127.0.0.1</code> 时，地址填 <code class="mono">127.0.0.1:6379</code> 即可（请求由该主机的 Agent 发起）。
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
          <el-tag v-if="status.role" size="small" effect="plain" :type="status.role === 'master' ? 'success' : 'info'">
            {{ status.role === 'master' ? '主节点' : '从节点' }}
          </el-tag>
          <el-tag v-if="status.mode" size="small" effect="plain" type="info">
            {{ status.mode === 'cluster' ? '集群模式' : '单机模式' }}
          </el-tag>
          <el-tag v-if="conn.tls" size="small" effect="plain" type="warning">TLS</el-tag>
          <span v-if="status.online && status.latency_ms !== undefined" class="muted" style="font-size: 12px">
            {{ status.latency_ms }} ms
          </span>
          <span v-if="!status.online && status.error" class="muted" style="font-size: 12px">{{ status.error }}</span>
          <div class="spacer"></div>
          <span class="muted" style="font-size: 12px">数据库</span>
          <el-select v-model="db" style="width: 150px" :loading="switching">
            <el-option v-for="d in dbOptions" :key="d.value" :label="`db${d.value}`" :value="d.value">
              <div class="row" style="justify-content: space-between; gap: 14px">
                <span class="mono">db{{ d.value }}</span>
                <span class="mono muted" style="font-size: 11.5px">{{ d.keys }} 键</span>
              </div>
            </el-option>
          </el-select>
          <el-button :icon="Refresh" :loading="statusLoading" @click="reloadAll">刷新</el-button>
          <el-button :icon="Setting" @click="openConn">连接设置</el-button>
        </div>

        <div v-if="status.online" class="grid" style="grid-template-columns: repeat(5, 1fr); margin-top: 14px">
          <div class="metric">
            <div class="metric-head"><span>内存占用</span></div>
            <div class="metric-value" style="font-size: 20px">{{ status.memory_used_human || '-' }}</div>
            <div class="metric-foot">
              峰值 {{ status.memory_peak_human || '-' }}
              <span v-if="status.maxmemory"> · 上限 {{ fmtSize(status.maxmemory) }}</span>
              <span v-else> · 未限制</span>
            </div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>当前库键数</span></div>
            <div class="metric-value">{{ fmtNum(status.keys) }}</div>
            <div class="metric-foot">
              db{{ db }} · 其中 {{ fmtNum(status.expires) }} 个带过期；全库 {{ fmtNum(status.total_keys) }}
            </div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>客户端</span></div>
            <div class="metric-value">{{ fmtNum(status.clients) }}</div>
            <div class="metric-foot">
              阻塞 {{ fmtNum(status.blocked_clients) }} · 拒绝连接 {{ fmtNum(status.rejected) }}
            </div>
          </div>
          <div class="metric">
            <div class="metric-head"><span>每秒命令</span></div>
            <div class="metric-value">{{ fmtNum(status.ops) }}</div>
            <div class="metric-foot">累计 {{ fmtNum(status.total_commands) }} 条</div>
          </div>
          <div class="metric">
            <div class="metric-head">
              <span>命中率</span>
              <span class="muted" style="font-size: 11px">命中/未命中</span>
            </div>
            <div class="metric-value">{{ status.hit_rate !== undefined ? status.hit_rate.toFixed(1) + '%' : '-' }}</div>
            <div class="metric-foot">
              {{ fmtNum(status.hits) }} / {{ fmtNum(status.misses) }}
              <span v-if="status.evicted"> · 淘汰 {{ fmtNum(status.evicted) }}</span>
            </div>
          </div>
        </div>
      </div>

      <el-tabs v-model="tab" style="margin-top: 4px">
        <!-- 键浏览 -->
        <el-tab-pane label="键浏览" name="keys">
          <div class="card" style="padding: 0">
            <div class="toolbar" style="padding: 12px 14px 0; margin-bottom: 8px; flex-wrap: wrap; gap: 8px">
              <el-input
                v-model="pattern"
                placeholder="模糊匹配子串，或 * ? 通配符，例如 user"
                clearable
                style="width: 250px"
                @keyup.enter="loadKeys(true)"
                @clear="loadKeys(true)"
              >
                <template #prefix><el-icon><Search /></el-icon></template>
              </el-input>
              <el-select v-model="typeFilter" style="width: 128px" @change="loadKeys(true)">
                <el-option label="全部类型" value="" />
                <el-option v-for="t in keyTypes" :key="t" :label="t" :value="t" />
              </el-select>
              <el-button size="small" @click="loadKeys(true)">查询</el-button>

              <el-radio-group v-model="keyView" size="small" style="margin-left: 2px">
                <el-radio-button label="list">列表</el-radio-button>
                <el-radio-button label="tree">树形</el-radio-button>
              </el-radio-group>

              <template v-if="keyView === 'tree'">
                <span class="muted" style="font-size: 12px">分隔符</span>
                <el-input v-model="separator" style="width: 86px" maxlength="8" />
              </template>

              <div class="spacer"></div>
              <span class="muted" style="font-size: 12px">
                匹配 <b>{{ fmtNum(matched) }}</b> / 库共 {{ fmtNum(keysTotal) }} 个键
              </span>
              <el-button size="small" :icon="Refresh" @click="loadKeys()" />
              <el-button type="primary" size="small" :icon="Plus" @click="openNewKey">新建键</el-button>
            </div>

            <!-- 列表视图 -->
            <template v-if="keyView === 'list'">
              <el-table
                :data="pagedKeys"
                v-loading="keysLoading"
                size="small"
                max-height="calc(100vh - 470px)"
                empty-text="没有匹配的键"
              >
                <el-table-column label="键名" min-width="260">
                  <template #default="{ row }">
                    <span class="mono key-link" style="font-size: 12.5px" @click="openKey(row.key)">
                      {{ row.key }}
                    </span>
                  </template>
                </el-table-column>
                <el-table-column label="类型" width="100">
                  <template #default="{ row }">
                    <el-tag size="small" effect="plain" :type="typeTag(row.type)">{{ row.type }}</el-tag>
                  </template>
                </el-table-column>
                <el-table-column label="过期时间" width="150">
                  <template #default="{ row }">
                    <span :class="row.ttl === -1 ? 'muted' : ''">{{ fmtTTL(row.ttl) }}</span>
                  </template>
                </el-table-column>
                <el-table-column label="内存占用" width="110" align="right">
                  <template #default="{ row }"><span class="mono">{{ fmtSize(row.size) }}</span></template>
                </el-table-column>
                <el-table-column label="操作" width="140" align="right">
                  <template #default="{ row }">
                    <el-button link type="primary" size="small" @click="openKey(row.key)">查看</el-button>
                    <el-button link type="danger" size="small" @click="removeKey(row.key)">删除</el-button>
                  </template>
                </el-table-column>
              </el-table>
              <div class="row" style="padding: 8px 12px; justify-content: flex-end">
                <span class="muted" style="font-size: 12px">
                  第 {{ listPage }} / {{ listPages }} 页 · 共 {{ fmtNum(keys.length) }} 个
                </span>
                <el-button size="small" :disabled="listPage <= 1" @click="listPage--">上一页</el-button>
                <el-button size="small" :disabled="listPage >= listPages" @click="listPage++">下一页</el-button>
              </div>
            </template>

            <!-- 树形视图：按分隔符把键名分层 -->
            <template v-else>
              <div v-if="treeData.length" class="row" style="padding: 8px 14px; gap: 8px">
                <el-button size="small" @click="expandAll">展开全部</el-button>
                <el-button size="small" @click="collapseAll">折叠全部</el-button>
                <span class="muted" style="font-size: 12px">
                  按「<code class="mono">{{ separator || '(无)' }}</code>」分层 · 点击叶子查看键
                </span>
              </div>
              <el-tree
                v-if="treeData.length"
                ref="treeRef"
                :data="treeData"
                node-key="path"
                :props="{ label: 'label', children: 'children' }"
                :expand-on-click-node="true"
                :default-expanded-keys="expandedKeys"
                v-loading="keysLoading"
                style="padding: 4px 14px 12px; max-height: calc(100vh - 470px); overflow: auto"
              >
                <template #default="{ node, data }">
                  <div class="tree-node">
                    <template v-if="data.children.length">
                      <el-icon class="t-ico"><Folder /></el-icon>
                      <span class="t-label">{{ data.label }}</span>
                      <span class="muted t-count">{{ leafCount(data) }} 个键</span>
                      <span v-if="data.key" class="t-self-tag">本身也是键</span>
                    </template>
                    <template v-else>
                      <el-icon class="t-ico"><Document /></el-icon>
                      <span class="mono key-link" @click.stop="openKey(data.key)">{{ data.key }}</span>
                      <el-tag size="small" effect="plain" :type="typeTag(data.type)">{{ data.type }}</el-tag>
                      <span class="muted" style="font-size: 12px">{{ fmtTTL(data.ttl) }}</span>
                      <span class="mono muted" style="font-size: 12px">{{ fmtSize(data.size) }}</span>
                    </template>
                    <span class="tree-actions">
                      <el-button v-if="data.key" link type="primary" size="small" @click.stop="openKey(data.key)">查看</el-button>
                      <el-button v-if="data.key" link type="danger" size="small" @click.stop="removeKey(data.key)">删除</el-button>
                    </span>
                  </div>
                </template>
              </el-tree>
              <div v-else-if="!keysLoading" class="empty-hint" style="padding: 24px">没有匹配的键</div>
            </template>

            <div v-if="truncated" class="muted" style="font-size: 12px; padding: 0 14px 10px">
              结果过多，仅显示前 {{ fmtNum(matched) }} 条；请缩小查找范围以查看完整列表。
            </div>
          </div>
        </el-tab-pane>

        <!-- 服务信息 -->
        <el-tab-pane label="服务信息" name="info">
          <div v-loading="infoLoading">
            <div class="grid" style="grid-template-columns: repeat(2, minmax(0, 1fr))">
              <div v-for="sec in infoSections" :key="sec.name" class="card" style="padding: 14px 16px">
                <div class="card-title" style="margin-bottom: 10px">{{ sec.name }}</div>
                <div class="kv">
                  <div v-for="f in sec.fields" :key="f.key" class="kv-row">
                    <span class="kv-key mono">{{ f.key }}</span>
                    <span class="kv-val mono">{{ f.value || '-' }}</span>
                  </div>
                </div>
              </div>
            </div>
            <div v-if="!infoLoading && !infoSections.length" class="empty-hint">没有取到服务信息</div>
          </div>
        </el-tab-pane>

        <!-- 命令台 -->
        <el-tab-pane label="命令台" name="console">
          <div class="card">
            <div class="row" style="margin-bottom: 10px">
              <el-input
                v-model="cmdInput"
                placeholder="输入 Redis 命令，例如 HGETALL user:1"
                class="mono"
                @keyup.enter="runCommand"
              />
              <el-button type="primary" :loading="cmdLoading" @click="runCommand">执行</el-button>
              <el-button :disabled="!history.length" @click="history = []">清空</el-button>
            </div>
            <div class="muted" style="font-size: 12px; margin-bottom: 10px">
              当前库 db{{ db }}；SHUTDOWN / FLUSHALL / FLUSHDB / CONFIG SET 等危险命令已被面板禁用，请登录服务器执行。
            </div>
            <div
              v-for="(h, i) in history"
              :key="i"
              class="cmd-block"
            >
              <div class="cmd-line">
                <span class="mono cmd-prompt">redis:{{ db }}&gt;</span>
                <span class="mono">{{ h.cmd }}</span>
                <div class="spacer"></div>
                <span class="muted" style="font-size: 11px">{{ h.cost_ms }} ms</span>
              </div>
              <pre class="cmd-out" :class="{ bad: !h.ok }">{{ h.output }}</pre>
            </div>
            <div v-if="!history.length" class="empty-hint">执行过的命令与返回会显示在这里</div>
          </div>
        </el-tab-pane>
      </el-tabs>
    </template>

    <!-- 连接设置 -->
    <el-dialog v-model="connVisible" title="Redis 连接设置" width="480px">
      <el-form label-width="92px">
        <el-form-item label="服务地址" required>
          <el-input v-model="connForm.addr" placeholder="127.0.0.1:6379" class="mono" />
          <div class="muted" style="font-size: 11.5px; margin-top: 4px">
            支持 host:port、redis://host:port/2、rediss://host:port；只对本机开放时填 127.0.0.1:6379
          </div>
        </el-form-item>
        <el-form-item label="用户名">
          <el-input v-model="connForm.username" placeholder="Redis 6+ ACL 用户名，留空表示默认用户" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="connForm.password"
            type="password"
            show-password
            :placeholder="conn.has_password ? '已保存，留空则不修改' : '未设置密码可留空'"
          />
          <el-checkbox v-if="conn.has_password" v-model="connForm.clear_password" style="margin-top: 4px">
            清除已保存的密码
          </el-checkbox>
        </el-form-item>
        <el-form-item label="默认库">
          <el-input-number v-model="connForm.db" :min="0" :max="255" controls-position="right" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">页面上可临时切库，不必改这里</span>
        </el-form-item>
        <el-form-item label="TLS">
          <el-switch v-model="connForm.tls" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">服务端启用 TLS 时打开（rediss://）</span>
        </el-form-item>
        <el-form-item v-if="connForm.tls" label="跳过校验">
          <el-switch v-model="connForm.insecure" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">自签证书场景可打开</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="connVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveConn">保存并测试</el-button>
      </template>
    </el-dialog>

    <!-- 新建 / 编辑键 -->
    <el-dialog v-model="keyVisible" :title="keyForm.editing ? '编辑字符串键' : '新建键'" width="520px">
      <el-form label-width="86px">
        <el-form-item label="键名" required>
          <el-input v-model="keyForm.key" :disabled="keyForm.editing" placeholder="user:1" class="mono" />
        </el-form-item>
        <el-form-item v-if="!keyForm.editing" label="类型">
          <el-select v-model="keyForm.type" style="width: 160px">
            <el-option v-for="t in keyTypes" :key="t" :label="t" :value="t" />
          </el-select>
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">
            集合类只追加单条，不会清空已有数据
          </span>
        </el-form-item>
        <el-form-item v-if="keyForm.type === 'hash' || keyForm.type === 'zset'" label="字段/成员" required>
          <el-input v-model="keyForm.field" :placeholder="keyForm.type === 'zset' ? 'member' : 'field'" class="mono" />
        </el-form-item>
        <el-form-item :label="valueLabel" required>
          <el-input v-model="keyForm.value" type="textarea" :rows="6" :placeholder="valuePlaceholder" />
        </el-form-item>
        <el-form-item label="过期时间">
          <el-input-number v-model="keyForm.ttl" :min="0" controls-position="right" style="width: 150px" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">秒；0 表示永不过期</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="keyVisible = false">取消</el-button>
        <el-button type="primary" :loading="keySaving" @click="saveKey">保存</el-button>
      </template>
    </el-dialog>

    <!-- 键详情 -->
    <el-drawer v-model="detailVisible" :title="detail?.key || '键详情'" size="620px">
      <div v-if="detailLoading" v-loading="true" style="height: 200px"></div>
      <template v-else-if="detail">
        <el-descriptions :column="2" border size="small">          <el-descriptions-item label="类型">
            <el-tag size="small" effect="plain" :type="typeTag(detail.type)">{{ detail.type }}</el-tag>
          </el-descriptions-item>
          <el-descriptions-item label="过期时间">{{ fmtTTL(detail.ttl) }}</el-descriptions-item>
          <el-descriptions-item label="元素数量">{{ fmtNum(detail.length) }}</el-descriptions-item>
          <el-descriptions-item label="内存占用">{{ fmtSize(detail.size) }}</el-descriptions-item>
          <el-descriptions-item label="编码">{{ detail.encoding || '-' }}</el-descriptions-item>
          <el-descriptions-item label="所在库">db{{ db }}</el-descriptions-item>
        </el-descriptions>

        <div class="card-title" style="margin: 18px 0 8px">
          <span>内容预览</span>
          <span v-if="detail.truncated" class="muted" style="font-size: 11.5px">
            数据较多，仅展示前 100 条
          </span>
        </div>

        <!-- 字符串 -->
        <pre v-if="detail.type === 'string'" class="preview mono">{{ detail.preview || '(空字符串)' }}</pre>

        <!-- hash / zset -->
        <el-table
          v-else-if="detail.type === 'hash' || detail.type === 'zset'"
          :data="detail.preview || []"
          size="small"
          max-height="380"
          empty-text="没有字段"
        >
          <el-table-column :label="detail.type === 'zset' ? '成员' : '字段'" min-width="200">
            <template #default="{ row }"><span class="mono">{{ row.field }}</span></template>
          </el-table-column>
          <el-table-column :label="detail.type === 'zset' ? '分值' : '值'" min-width="240">
            <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
          </el-table-column>
        </el-table>

        <!-- list / set / stream -->
        <div v-else-if="Array.isArray(detail.preview)" class="list-preview">
          <div v-for="(v, i) in detail.preview" :key="i" class="list-item mono">
            <span class="muted" style="width: 34px; flex: none">{{ i + 1 }}</span>
            <span style="word-break: break-all">{{ v }}</span>
          </div>
          <div v-if="!detail.preview.length" class="empty-hint">没有元素</div>
        </div>

        <div v-else class="empty-hint">该类型暂不支持内容预览，请使用命令台查看</div>
      </template>

      <!-- 底部操作独立成槽，data 为空时整体隐藏 -->
      <template #footer>
        <div v-if="detail" class="row" style="width: 100%">
          <el-button v-if="detail.type === 'string'" @click="openEditKey">编辑内容</el-button>
          <el-button @click="openTTL">修改过期时间</el-button>
          <div class="spacer"></div>
          <el-button type="danger" plain @click="removeKey(detail.key, true)">删除键</el-button>
        </div>
      </template>
    </el-drawer>

    <!-- 过期时间 -->
    <el-dialog v-model="ttlVisible" title="修改过期时间" width="380px">
      <el-form label-width="80px">
        <el-form-item label="秒数">
          <el-input-number v-model="ttlValue" :min="0" controls-position="right" style="width: 150px" />
        </el-form-item>
      </el-form>
      <div class="muted" style="font-size: 12px">填 0 表示移除过期时间（永久保留）。</div>
      <template #footer>
        <el-button @click="ttlVisible = false">取消</el-button>
        <el-button type="primary" :loading="ttlSaving" @click="saveTTL">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Coin, Document, Folder, Plus, Refresh, Search, Setting } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'

const keyTypes = ['string', 'hash', 'list', 'set', 'zset', 'stream']

const conn = ref<any>({ configured: false, addr: '', username: '', db: 0, tls: false })
const status = ref<any>({ online: false })
const statusLoading = ref(false)
const infoLoading = ref(false)
const infoSections = ref<any[]>([])
const tab = ref('keys')

const db = ref(0)
const switching = ref(false)

// 键列表
const keys = ref<any[]>([])
const keysLoading = ref(false)
const pattern = ref('')
const typeFilter = ref('')
const keysTotal = ref(0)
const matched = ref(0)
const truncated = ref(false)
// 视图：列表 / 树形
const keyView = ref<'list' | 'tree'>('list')
const separator = ref(':')
// 列表视图的客户端分页（后端已一次性返回全部匹配键，最多 5000 条）
const listPage = ref(1)
const listPageSize = 100
// 树形视图
const treeRef = ref<any>(null)
const expandedKeys = ref<string[]>([])
interface TreeNode {
  label: string
  path: string
  key?: string
  type?: string
  ttl?: number
  size?: number
  leaf: boolean
  children: TreeNode[]
}
const treeData = computed<TreeNode[]>(() => buildTree(keys.value, separator.value))

// 连接设置
const connVisible = ref(false)
const saving = ref(false)
const connForm = reactive<any>({
  addr: '',
  username: '',
  password: '',
  db: 0,
  tls: false,
  insecure: false,
  clear_password: false
})

// 新建 / 编辑键
const keyVisible = ref(false)
const keySaving = ref(false)
const keyForm = reactive<any>({
  editing: false,
  key: '',
  type: 'string',
  field: '',
  value: '',
  ttl: 0
})

// 键详情
const detailVisible = ref(false)
const detailLoading = ref(false)
const detail = ref<any>(null)

// 过期时间
const ttlVisible = ref(false)
const ttlSaving = ref(false)
const ttlValue = ref(0)

// 命令台
const cmdInput = ref('')
const cmdLoading = ref(false)
const history = ref<any[]>([])

const dbOptions = computed(() => {
  const count = status.value.databases || 16
  const stats = new Map<number, any>()
  for (const d of status.value.dbs || []) stats.set(d.index, d)
  const out: any[] = []
  for (let i = 0; i < count; i++) out.push({ value: i, keys: stats.get(i)?.keys || 0 })
  return out
})

const valueLabel = computed(() => (keyForm.type === 'zset' ? '分值' : keyForm.type === 'string' ? '内容' : '值'))
const valuePlaceholder = computed(() => {
  if (keyForm.type === 'zset') return '数字分值，例如 100'
  if (keyForm.type === 'string') return '字符串内容'
  return '要追加的值'
})

// ---------- 格式化 ----------

function typeTag(t: string) {
  const map: Record<string, any> = {
    string: 'primary',
    hash: 'success',
    list: 'warning',
    set: 'info',
    zset: 'danger',
    stream: 'info'
  }
  return map[t] || 'info'
}

function fmtNum(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v)) return '-'
  return v.toLocaleString('zh-CN')
}

function fmtSize(n: number) {
  if (n === undefined || n === null || n < 0) return '-'
  if (n < 1024) return n + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return v.toFixed(v >= 100 ? 0 : 1) + ' ' + units[i]
}

function fmtTTL(ttl: number) {
  if (ttl === undefined || ttl === null) return '-'
  if (ttl === -1) return '永久'
  if (ttl < 0) return '已过期'
  if (ttl < 60) return ttl + ' 秒'
  if (ttl < 3600) return Math.floor(ttl / 60) + ' 分 ' + (ttl % 60) + ' 秒'
  if (ttl < 86400) return Math.floor(ttl / 3600) + ' 小时 ' + Math.floor((ttl % 3600) / 60) + ' 分'
  return Math.floor(ttl / 86400) + ' 天 ' + Math.floor((ttl % 86400) / 3600) + ' 小时'
}

// ---------- 连接 ----------

async function loadConn() {
  try {
    const res = await api<any>('/api/redis/conn')
    conn.value = res || { configured: false }
    db.value = res?.db || 0
  } catch {
    conn.value = { configured: false }
  }
}

function openConn() {
  connForm.addr = conn.value.addr || '127.0.0.1:6379'
  connForm.username = conn.value.username || ''
  connForm.password = ''
  connForm.db = conn.value.db || 0
  connForm.tls = !!conn.value.tls
  connForm.insecure = !!conn.value.insecure
  connForm.clear_password = false
  connVisible.value = true
}

async function saveConn() {
  if (!connForm.addr) return ElMessage.warning('请填写 Redis 地址')
  saving.value = true
  try {
    await api('/api/redis/conn', { method: 'PUT', body: { ...connForm } })
    connVisible.value = false
    ElMessage.success('已保存，正在测试连接')
    await loadConn()
    await loadStatus()
    if (status.value.online) {
      ElMessage.success(`连接正常${status.value.version ? '（Redis ' + status.value.version + '）' : ''}`)
    } else {
      ElMessage.warning(status.value.error || '连接失败')
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

// ---------- 状态与库 ----------

async function loadStatus() {
  statusLoading.value = true
  try {
    status.value = await api<any>('/api/redis/status', { query: { db: db.value } })
  } catch (e) {
    status.value = { online: false, error: errMsg(e) }
  } finally {
    statusLoading.value = false
  }
}

async function loadInfo() {
  infoLoading.value = true
  try {
    const res = await api<any>('/api/redis/info', { query: { db: db.value } })
    infoSections.value = res?.sections || []
  } catch (e) {
    infoSections.value = []
    ElMessage.error(errMsg(e))
  } finally {
    infoLoading.value = false
  }
}

async function reloadAll() {
  switching.value = true
  try {
    await loadStatus()
    if (!status.value.online) return
    listPage.value = 1
    expandedKeys.value = []
    await Promise.all([loadKeys(), loadInfo()])
  } finally {
    switching.value = false
  }
}

// 切库后所有页签的数据都失效，必须整体重拉
watch(db, async () => {
  if (!conn.value.configured) return
  switching.value = true
  try {
    listPage.value = 1
    expandedKeys.value = []
    detailVisible.value = false
    await loadStatus()
    await Promise.all([loadKeys(), loadInfo()])
  } finally {
    switching.value = false
  }
})

watch(tab, (v) => {
  if (v === 'info' && !infoSections.value.length) loadInfo()
})

// ---------- 键列表 ----------

async function loadKeys(reset = false) {
  if (reset) {
    listPage.value = 1
    expandedKeys.value = []
  }
  keysLoading.value = true
  try {
    const res = await api<any>('/api/redis/keys', {
      query: {
        db: db.value,
        pattern: pattern.value || '*',
        type: typeFilter.value,
        count: 3000
      }
    })
    keys.value = res?.items || []
    keysTotal.value = res?.total || 0
    matched.value = res?.matched || keys.value.length
    truncated.value = !!res?.truncated
    listPage.value = 1
    expandedKeys.value = []
  } catch (e) {
    keys.value = []
    keysTotal.value = 0
    matched.value = 0
    truncated.value = false
    ElMessage.error(errMsg(e))
  } finally {
    keysLoading.value = false
  }
}

const listPages = computed(() => Math.max(1, Math.ceil(keys.value.length / listPageSize)))
const pagedKeys = computed(() =>
  keys.value.slice((listPage.value - 1) * listPageSize, listPage.value * listPageSize)
)

// ---------- 树形分层 ----------

// buildTree 按分隔符把扁平的键名列表组织成嵌套树。
// 例如 user:1、user:profile:name 在分隔符「:」下会变成 user → (1, profile → name)。
function buildTree(items: any[], sep: string): TreeNode[] {
  const nodes = new Map<string, TreeNode>()
  const get = (prefix: string): TreeNode => {
    let n = nodes.get(prefix)
    if (!n) {
      n = { label: '', path: prefix, leaf: true, children: [] }
      nodes.set(prefix, n)
    }
    return n
  }
  for (const it of items) {
    const parts = sep ? String(it.key).split(sep) : [String(it.key)]
    let prefix = ''
    for (let i = 0; i < parts.length; i++) {
      const seg = parts[i]
      prefix = prefix ? prefix + sep + seg : seg
      const node = get(prefix)
      node.label = seg
      if (i === parts.length - 1) {
        node.key = it.key
        node.type = it.type
        node.ttl = it.ttl
        node.size = it.size
      }
    }
  }
  // 把每个节点挂到其父节点下
  for (const node of nodes.values()) {
    if (sep && node.path.includes(sep)) {
      const parent = nodes.get(node.path.slice(0, node.path.lastIndexOf(sep)))
      if (parent && !parent.children.some((c) => c.path === node.path)) {
        parent.children.push(node)
      }
    }
  }
  for (const node of nodes.values()) node.leaf = node.children.length === 0
  const roots = [...nodes.values()].filter((n) => !(sep && n.path.includes(sep)))
  sortTree(roots)
  return roots
}

function sortTree(nodes: TreeNode[]) {
  nodes.sort((a, b) => {
    const af = a.children.length ? 0 : 1
    const bf = b.children.length ? 0 : 1
    if (af !== bf) return af - bf
    return a.label.localeCompare(b.label)
  })
  for (const n of nodes) sortTree(n.children)
}

// leafCount 统计某节点下所有叶子（即实际键）的数量。
function leafCount(node: TreeNode): number {
  if (node.children.length === 0) return 1
  return node.children.reduce((s, c) => s + leafCount(c), 0)
}

function allPaths(nodes: TreeNode[]): string[] {
  const out: string[] = []
  for (const n of nodes) {
    if (n.children.length) {
      out.push(n.path)
      out.push(...allPaths(n.children))
    }
  }
  return out
}

function expandAll() {
  expandedKeys.value = allPaths(treeData.value)
}
function collapseAll() {
  expandedKeys.value = []
}

// ---------- 键详情 ----------

async function openKey(key: string) {
  detailVisible.value = true
  detailLoading.value = true
  detail.value = null
  try {
    detail.value = await api<any>('/api/redis/key', { query: { db: db.value, key } })
  } catch (e) {
    detailVisible.value = false
    ElMessage.error(errMsg(e))
  } finally {
    detailLoading.value = false
  }
}

function openNewKey() {
  keyForm.editing = false
  keyForm.key = ''
  keyForm.type = 'string'
  keyForm.field = ''
  keyForm.value = ''
  keyForm.ttl = 0
  keyVisible.value = true
}

function openEditKey() {
  if (!detail.value) return
  keyForm.editing = true
  keyForm.key = detail.value.key
  keyForm.type = 'string'
  keyForm.field = ''
  keyForm.value = typeof detail.value.preview === 'string' ? detail.value.preview : ''
  keyForm.ttl = detail.value.ttl > 0 ? detail.value.ttl : 0
  keyVisible.value = true
}

async function saveKey() {
  if (!keyForm.key) return ElMessage.warning('请填写键名')
  if ((keyForm.type === 'hash' || keyForm.type === 'zset') && !keyForm.field) {
    return ElMessage.warning('请填写字段名')
  }
  keySaving.value = true
  try {
    await api('/api/redis/key', {
      method: 'PUT',
      query: { db: db.value },
      body: {
        key: keyForm.key,
        type: keyForm.type,
        field: keyForm.field,
        value: keyForm.value,
        ttl: keyForm.ttl || 0
      }
    })
    ElMessage.success('已保存')
    keyVisible.value = false
    await loadKeys()
    if (detailVisible.value) await openKey(keyForm.key)
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    keySaving.value = false
  }
}

async function removeKey(key: string, fromDrawer = false) {
  try {
    await ElMessageBox.confirm(`确定删除键「${key}」吗？该操作不可恢复。`, '删除键', {
      type: 'warning',
      confirmButtonText: '删除',
      confirmButtonClass: 'el-button--danger'
    })
  } catch {
    return
  }
  try {
    await api('/api/redis/key', { method: 'DELETE', query: { db: db.value, key } })
    ElMessage.success('已删除')
    if (fromDrawer) detailVisible.value = false
    await loadKeys()
    await loadStatus()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

function openTTL() {
  ttlValue.value = detail.value && detail.value.ttl > 0 ? detail.value.ttl : 0
  ttlVisible.value = true
}

async function saveTTL() {
  if (!detail.value) return
  ttlSaving.value = true
  try {
    await api('/api/redis/key/ttl', {
      method: 'POST',
      query: { db: db.value },
      body: { key: detail.value.key, ttl: ttlValue.value || 0 }
    })
    ElMessage.success('已更新')
    ttlVisible.value = false
    await openKey(detail.value.key)
    await loadKeys()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    ttlSaving.value = false
  }
}

// ---------- 命令台 ----------

async function runCommand() {
  const cmd = cmdInput.value.trim()
  if (!cmd) return
  cmdLoading.value = true
  try {
    const res = await api<any>('/api/redis/command', {
      method: 'POST',
      query: { db: db.value },
      body: { command: cmd }
    })
    history.value.unshift({
      cmd,
      output: res?.output || '(无返回)',
      ok: !!res?.ok,
      cost_ms: res?.cost_ms ?? 0
    })
    cmdInput.value = ''
    // 写命令可能改变了键，顺手刷新一下列表
    if (res?.ok) await loadKeys()
  } catch (e) {
    history.value.unshift({ cmd, output: errMsg(e), ok: false, cost_ms: 0 })
  } finally {
    cmdLoading.value = false
  }
}

onMounted(async () => {
  await loadConn()
  if (!conn.value.configured) return
  await reloadAll()
})
</script>

<style scoped>
.key-link {
  cursor: pointer;
  color: var(--brand);
}
.key-link:hover {
  text-decoration: underline;
}
.tree-node {
  display: flex;
  align-items: center;
  gap: 7px;
  width: 100%;
  font-size: 12.5px;
  padding: 2px 0;
}
.t-ico {
  color: var(--ink-3);
}
.t-label {
  font-weight: 600;
}
.t-count {
  font-size: 11.5px;
}
.t-self-tag {
  font-size: 11px;
  padding: 0 6px;
  border-radius: 4px;
  background: #fef3e6;
  color: #b25a00;
  border: 1px solid #f7d9b0;
}
.tree-actions {
  margin-left: auto;
  display: none;
}
.tree-node:hover .tree-actions {
  display: inline-flex;
}
.kv {
  display: flex;
  flex-direction: column;
  gap: 6px;
  max-height: 320px;
  overflow-y: auto;
}
.kv-row {
  display: flex;
  gap: 12px;
  font-size: 12px;
  line-height: 1.6;
}
.kv-key {
  color: var(--ink-3);
  flex: 0 0 168px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.kv-val {
  color: var(--ink);
  word-break: break-all;
  flex: 1;
}
.preview {
  background: #f8f9fc;
  border: 1px solid var(--line);
  border-radius: 8px;
  padding: 12px 14px;
  font-size: 12.5px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 420px;
  overflow-y: auto;
  margin: 0;
}
.list-preview {
  border: 1px solid var(--line);
  border-radius: 8px;
  max-height: 420px;
  overflow-y: auto;
}
.list-item {
  display: flex;
  gap: 8px;
  padding: 7px 12px;
  font-size: 12.5px;
  border-bottom: 1px solid #f2f4f9;
}
.list-item:last-child {
  border-bottom: none;
}
.cmd-block {
  border: 1px solid var(--line);
  border-radius: 8px;
  overflow: hidden;
  margin-bottom: 10px;
}
.cmd-line {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 7px 12px;
  background: #f8f9fc;
  font-size: 12.5px;
}
.cmd-prompt {
  color: var(--brand);
}
.cmd-out {
  margin: 0;
  padding: 10px 12px;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
  font-size: 12.5px;
  line-height: 1.7;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 300px;
  overflow-y: auto;
  background: #fff;
}
.cmd-out.bad {
  color: #c2382f;
}
</style>
