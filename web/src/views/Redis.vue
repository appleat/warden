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
      </div>

      <el-tabs v-model="tab" style="margin-top: 4px">
        <!-- 键浏览 -->
        <el-tab-pane label="键浏览" name="keys">
          <div class="split" :style="splitStyle" ref="splitEl">
            <!-- 左：键名树 -->
            <div class="card side" style="padding: 0">
              <div class="side-head">
                <span class="card-title">键名</span>
                <div class="spacer"></div>
                <span class="muted" style="font-size: 11.5px">匹配 {{ fmtNum(matched) }}</span>
                <el-button size="small" text :icon="Refresh" :loading="keysLoading" @click="loadKeys()" />
                <el-button type="primary" size="small" :icon="Plus" @click="openNewKey">新建</el-button>
              </div>

              <div style="padding: 8px 10px 0">
                <el-input
                  v-model="pattern"
                  size="small"
                  placeholder="模糊匹配子串，或 * ? 通配符"
                  clearable
                  @keyup.enter="loadKeys(true)"
                  @clear="loadKeys(true)"
                >
                  <template #prefix><el-icon><Search /></el-icon></template>
                </el-input>
              </div>

              <div class="row" style="padding: 8px 10px 0; gap: 6px; align-items: center">
                <el-select
                  v-model="typeFilter"
                  size="small"
                  style="width: 112px"
                  placeholder="全部类型"
                  @change="loadKeys(true)"
                >
                  <el-option label="全部类型" value="" />
                  <el-option v-for="t in keyTypes" :key="t" :label="t" :value="t" />
                </el-select>
                <span class="muted" style="font-size: 11.5px; flex: none">分隔符</span>
                <el-input
                  v-model="separator"
                  size="small"
                  style="width: 66px"
                  maxlength="8"
                  placeholder="不分层"
                  class="sep-input"
                />
              </div>

              <div class="row" style="padding: 4px 10px 2px; gap: 2px; align-items: center">
                <el-button link size="small" @click="expandAll">展开全部</el-button>
                <el-button link size="small" @click="collapseAll">折叠全部</el-button>
                <div class="spacer"></div>
                <span class="muted" style="font-size: 11px">
                  库内共 {{ fmtNum(keysTotal) }} 个键
                </span>
              </div>

              <el-scrollbar height="calc(100vh - 424px)">
                <el-tree
                  v-if="treeData.length"
                  ref="treeRef"
                  :data="treeData"
                  node-key="path"
                  :props="{ label: 'label', children: 'children' }"
                  :expand-on-click-node="true"
                  highlight-current
                  :empty-text="keysLoading ? '加载中…' : '没有匹配的键'"
                  style="padding: 2px 8px 8px 4px"
                  @node-click="onTreeClick"
                  @node-expand="onNodeExpand"
                  @node-collapse="onNodeCollapse"
                >
                  <template #default="{ data }">
                    <div class="tree-row">
                      <el-icon :size="13" class="db-ico">
                        <Folder v-if="data.children.length" />
                        <Document v-else />
                      </el-icon>
                      <span
                        class="mono db-name"
                        :class="{ 'key-link': data.key && !data.children.length }"
                        :title="data.key || data.path"
                      >{{ data.label }}</span>
                      <el-tag
                        v-if="data.type && !data.children.length"
                        size="small"
                        effect="plain"
                        class="type-tag"
                        :type="typeTag(data.type)"
                      >{{ data.type }}</el-tag>
                      <div class="spacer"></div>
                      <span v-if="data.children.length" class="muted tree-meta">{{ leafCount(data) }}</span>
                    </div>
                  </template>
                </el-tree>
                <div v-else-if="!keysLoading" class="empty-hint" style="padding: 24px 14px">
                  没有匹配的键
                </div>
              </el-scrollbar>

              <div v-if="truncated" class="muted" style="font-size: 11.5px; padding: 6px 12px 8px">
                结果过多，仅显示前 {{ fmtNum(matched) }} 个键，请缩小查找范围
              </div>
            </div>

            <div
              class="split-bar"
              :class="{ 'is-dragging': splitDragging }"
              title="拖动调整宽度，双击恢复默认"
              @pointerdown="onSplitDown"
              @dblclick="resetSplitWidth"
            ></div>

            <!-- 右：键详情（内嵌，不再弹抽屉） -->
            <div class="card" style="padding: 0; min-width: 0">
              <div v-if="!selectedKey" class="empty-hint" style="padding: 80px 20px">
                在左侧点击一个键，这里会显示它的类型、过期时间、内存占用与内容预览
              </div>
              <div v-else v-loading="detailLoading">
                <div class="side-head">
                  <span class="card-title mono" style="word-break: break-all">{{ selectedKey }}</span>
                  <el-tag v-if="detail?.type" size="small" effect="plain" :type="typeTag(detail.type)">
                    {{ detail.type }}
                  </el-tag>
                  <!-- 二进制键名（如 Java 序列化）在页面上只能转义显示，加个标记免得用户以为键名本身长这样 -->
                  <el-tag
                    v-if="detail?.key_raw"
                    size="small"
                    effect="plain"
                    type="info"
                    title="键名含非文本字节，这里以 \xNN 转义显示；查看、改过期、删除都按原始字节执行"
                  >二进制键名</el-tag>
                  <div class="spacer"></div>
                  <el-button v-if="detail?.type === 'string'" size="small" @click="openEditKey">编辑内容</el-button>
                  <el-button v-if="detail" size="small" @click="openTTL">修改过期</el-button>
                  <el-button v-if="detail" size="small" type="danger" plain @click="removeKey(selectedKey, selectedKeyRaw, true)">
                    删除
                  </el-button>
                  <el-button size="small" :icon="Refresh" @click="openKey(selectedKey, selectedKeyRaw)" />
                </div>

                <div v-if="detail" style="padding: 12px 14px 0">
                  <el-descriptions :column="4" border size="small">
                    <el-descriptions-item label="过期时间">{{ fmtTTL(detail.ttl) }}</el-descriptions-item>
                    <el-descriptions-item label="元素数量">{{ fmtNum(detail.length) }}</el-descriptions-item>
                    <el-descriptions-item label="内存占用">{{ fmtSize(detail.size) }}</el-descriptions-item>
                    <el-descriptions-item label="编码">{{ detail.encoding || '-' }}</el-descriptions-item>
                  </el-descriptions>

                  <div class="card-title" style="margin: 14px 0 8px; gap: 8px">
                    <span>内容预览</span>
                    <span v-if="detail.truncated" class="muted" style="font-size: 11.5px">
                      数据较多，仅展示前 100 条
                    </span>
                    <div class="spacer"></div>
                    <el-button
                      v-if="detail.type === 'string' && detail.preview"
                      size="small"
                      @click="copyValue()"
                    >复制内容</el-button>
                  </div>

                  <!-- 字符串 -->
                  <pre v-if="detail.type === 'string'" class="preview mono">{{ detail.preview || '(空字符串)' }}</pre>

                  <!-- hash / zset -->
                  <el-table
                    v-else-if="detail.type === 'hash' || detail.type === 'zset'"
                    :data="detail.preview || []"
                    size="small"
                    border
                    max-height="calc(100vh - 520px)"
                    empty-text="没有字段"
                  >
                    <el-table-column :label="detail.type === 'zset' ? '成员' : '字段'" min-width="220">
                      <template #default="{ row }"><span class="mono">{{ row.field }}</span></template>
                    </el-table-column>
                    <el-table-column :label="detail.type === 'zset' ? '分值' : '值'" min-width="260">
                      <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
                    </el-table-column>
                  </el-table>

                  <!-- list / set / stream -->
                  <div v-else-if="Array.isArray(detail.preview)" class="list-preview">
                    <div v-for="(v, i) in detail.preview" :key="i" class="list-item mono">
                      <span class="muted" style="width: 40px; flex: none">{{ i + 1 }}</span>
                      <span style="word-break: break-all">{{ v }}</span>
                    </div>
                    <div v-if="!detail.preview.length" class="empty-hint">没有元素</div>
                  </div>

                  <div v-else class="empty-hint">该类型暂不支持内容预览，请使用命令台查看</div>
                </div>

                <div v-else-if="!detailLoading" class="empty-hint" style="padding: 40px">
                  没有取到该键的内容，可能已被删除
                </div>
              </div>
            </div>
          </div>
        </el-tab-pane>

        <!-- 运行指标 -->
        <el-tab-pane label="运行指标" name="metrics">
          <div class="card">
            <div class="row" style="margin-bottom: 14px">
              <span class="muted" style="font-size: 12px">
                数值来自 INFO，与刷新时刻一致；当前库 db{{ db }}
              </span>
              <div class="spacer"></div>
              <el-button size="small" :icon="Refresh" :loading="statusLoading" @click="loadStatus">刷新</el-button>
            </div>
            <div
              v-if="status.online"
              class="grid"
              style="grid-template-columns: repeat(auto-fit, minmax(220px, 1fr))"
            >
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
                <div class="metric-value">
                  {{ status.hit_rate !== undefined ? status.hit_rate.toFixed(1) + '%' : '-' }}
                </div>
                <div class="metric-foot">
                  {{ fmtNum(status.hits) }} / {{ fmtNum(status.misses) }}
                  <span v-if="status.evicted"> · 淘汰 {{ fmtNum(status.evicted) }}</span>
                </div>
              </div>
            </div>
            <div v-else class="empty-hint" style="padding: 30px">
              {{ status.error || '暂时拿不到运行指标，请检查连接是否正常' }}
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
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Coin, Document, Folder, Plus, Refresh, Search, Setting } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'
import { useSplitWidth } from '../useSplit'

const keyTypes = ['string', 'hash', 'list', 'set', 'zset', 'stream']

const conn = ref<any>({ configured: false, addr: '', username: '', db: 0, tls: false })
const status = ref<any>({ online: false })
const statusLoading = ref(false)
const infoLoading = ref(false)
const infoSections = ref<any[]>([])
const tab = ref('keys')

// 左侧键树宽度可拖动，拖过的宽度记在 localStorage 里
const {
  style: splitStyle,
  dragging: splitDragging,
  container: splitEl,
  onPointerDown: onSplitDown,
  reset: resetSplitWidth
} = useSplitWidth({ storageKey: 'warden_split_redis', initial: 320, min: 200, max: 640 })

const db = ref(0)
const switching = ref(false)

// 键列表：后端一次性返回全部匹配键（最多 5000 条），前端按分隔符组织成树
const keys = ref<any[]>([])
const keysLoading = ref(false)
const pattern = ref('')
const typeFilter = ref('')
const keysTotal = ref(0)
const matched = ref(0)
const truncated = ref(false)
const separator = ref(':')
const treeRef = ref<any>(null)
// expandedKeys 只是「展开状态记录」，**不要再绑到 default-expanded-keys**：
// 那个 prop 一变就会 setDefaultExpandedKeys → node.expand(null, autoExpandParent=true)，
// 折叠父节点时残留的下级路径又会把父节点撑开，表现为「点了上级收不起来」。
const expandedKeys = ref<string[]>([])
// 递归折叠子孙时置位，避免每收一个节点又触发一轮全量子孙扫描
let deepCollapsing = false
// 当前选中的键，右侧内嵌详情展示它。
// selectedKey 是展示名（二进制键已转义），selectedKeyRaw 是它的原始字节（base64）——
// 两者必须成对使用，拿展示名去查二进制键会查不到。
const selectedKey = ref('')
const selectedKeyRaw = ref('')
interface TreeNode {
  label: string
  path: string
  key?: string
  keyRaw?: string
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
  keyRaw: '',
  type: 'string',
  field: '',
  value: '',
  ttl: 0
})

// 键详情（内嵌在右侧，不再用抽屉）
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
    // 刷新保留展开状态（preserveExpansion 会裁掉已不存在的目录），
    // 只有切库才清空，因为那是另一套键空间
    clearSelection()
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
    expandedKeys.value = []
    clearSelection()
    await loadStatus()
    await Promise.all([loadKeys(), loadInfo()])
  } finally {
    switching.value = false
  }
})

watch(tab, (v) => {
  if (v === 'info' && !infoSections.value.length) loadInfo()
  if (v === 'metrics') loadStatus()
})

// 换分隔符等于换了一套层级，旧的展开路径全失效，按新层级重算
watch(separator, () => {
  resetExpansion()
  syncCurrent()
})

// ---------- 键列表 ----------

// reset=true 用于「换了查找条件」：展开状态与右侧详情都清空。
// 否则（刷新、增删改之后）尽量保留原来展开的目录与选中的键，避免视野跳回顶部。
async function loadKeys(reset = false) {
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
    if (reset) {
      clearSelection()
      resetExpansion()
    } else {
      // 选中的键已被删掉/过滤掉时，右侧详情要一并收起
      if (selectedKey.value && !keys.value.some((k) => k.key === selectedKey.value)) clearSelection()
      // 两者都只调一次：都走 applyExpansion，重复调会把刚设好的记录又清空
      if (expandedKeys.value.length) {
        preserveExpansion()
      } else {
        resetExpansion()
      }
      syncCurrent()
    }
  } catch (e) {
    keys.value = []
    keysTotal.value = 0
    matched.value = 0
    truncated.value = false
    clearSelection()
    ElMessage.error(errMsg(e))
  } finally {
    keysLoading.value = false
  }
}

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
        node.keyRaw = it.key_raw || ''
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

// allNodes 取出树里已渲染的所有节点（含折叠未展开的子孙），折叠/展开全部要用。
function allNodes(): any[] {
  const map = treeRef.value?.store?.nodesMap
  return map ? (Object.values(map) as any[]) : []
}

// applyExpansion 命令式展开：由浅到深逐个展开，父节点先开子节点才存在。
// 这是唯一设置展开状态的地方（不走 prop），所以折叠不会再被反转。
function applyExpansion(paths: string[]) {
  const want = [...new Set(paths)].sort((a, b) => a.length - b.length)
  nextTick(() => {
    const ok: string[] = []
    for (const p of want) {
      const node = treeRef.value?.getNode?.(p)
      if (!node?.expand) continue
      node.expand()
      ok.push(p)
    }
    expandedKeys.value = ok
  })
}

function expandAll() {
  applyExpansion(allPaths(treeData.value))
}

function collapseAll() {
  // 由深到浅收，父节点收起后子节点的展开状态也就没意义了。
  // 期间置位 deepCollapsing，避免每次 collapse 都触发一遍全量子孙扫描（键多时是 O(n²)）
  deepCollapsing = true
  try {
    for (const n of allNodes().sort((a, b) => (b.level || 0) - (a.level || 0))) {
      if (n.expanded) n.collapse?.()
    }
  } finally {
    deepCollapsing = false
  }
  expandedKeys.value = []
}

// 键不多（≤120）时默认全展开，进来就能看到键；键多则保持折叠，避免一屏塞满
function resetExpansion() {
  applyExpansion(keys.value.length && keys.value.length <= 120 ? allPaths(treeData.value) : [])
}

// ancestorsOf 返回某个键名在分隔符下的各级父目录（由浅到深）。
function ancestorsOf(key: string): string[] {
  const sep = separator.value
  if (!sep || !key.includes(sep)) return []
  const parts = key.split(sep)
  const out: string[] = []
  let prefix = ''
  for (let i = 0; i < parts.length - 1; i++) {
    prefix = prefix ? prefix + sep + parts[i] : parts[i]
    out.push(prefix)
  }
  return out
}

// preserveExpansion 在键列表刷新后，只保留仍然存在的目录展开状态，
// 并把当前选中键的父目录补上，否则右侧详情还开着、左边却找不到对应节点。
function preserveExpansion() {
  const folders = new Set(allPaths(treeData.value))
  const keep = new Set(expandedKeys.value.filter((p) => folders.has(p)))
  if (selectedKey.value) {
    for (const a of ancestorsOf(selectedKey.value)) if (folders.has(a)) keep.add(a)
  }
  applyExpansion([...keep])
}

// 树重建后高亮会丢，选中键还在列表里就补回来
function syncCurrent() {
  if (!selectedKey.value) return
  nextTick(() => treeRef.value?.setCurrentKey?.(selectedKey.value))
}

function onTreeClick(data: TreeNode) {
  if (data.key) openKey(data.key, data.keyRaw)
}

// 手动展开/折叠也记进 expandedKeys，刷新后能还原
function onNodeExpand(data: TreeNode) {
  if (!expandedKeys.value.includes(data.path)) expandedKeys.value = [...expandedKeys.value, data.path]
}

// 折叠一个目录时，把它下面所有已展开的子孙一并收起。
// 只靠 el-tree 自己收，隐藏起来的子节点内部仍是 expanded，
// 下次再点开父节点，下级还是原样摊开——用户预期是「点上级 = 整体折叠」。
function onNodeCollapse(data: TreeNode) {
  if (deepCollapsing) {
    expandedKeys.value = expandedKeys.value.filter((p) => p !== data.path)
    return
  }
  const sep = separator.value
  const prefix = sep ? data.path + sep : ''
  if (prefix) {
    deepCollapsing = true
    try {
      for (const n of allNodes()) {
        if (n.expanded && n.key !== data.path && String(n.key).startsWith(prefix)) n.collapse?.()
      }
    } finally {
      deepCollapsing = false
    }
  }
  expandedKeys.value = expandedKeys.value.filter(
    (p) => p !== data.path && !(prefix && p.startsWith(prefix))
  )
}

// ---------- 键详情 ----------

function clearSelection() {
  selectedKey.value = ''
  selectedKeyRaw.value = ''
  detail.value = null
}

// openKey 打开键详情。key 是展示名，keyRaw 是原始字节（base64，二进制键才有）；
// 请求时优先带 key_raw，后端据此还原真正的键名。
async function openKey(key: string, keyRaw = '') {
  selectedKey.value = key
  selectedKeyRaw.value = keyRaw
  detailLoading.value = true
  detail.value = null
  try {
    const res = await api<any>('/api/redis/key', {
      query: { db: db.value, key, key_raw: keyRaw }
    })
    detail.value = res
    // 以后端回带的为准：展示名与原始字节都以前端列表可能缺字段的情况兜住
    if (res?.key) selectedKey.value = res.key
    if (res?.key_raw) selectedKeyRaw.value = res.key_raw
    syncCurrent()
  } catch (e) {
    clearSelection()
    ElMessage.error(errMsg(e))
  } finally {
    detailLoading.value = false
  }
}

async function copyValue() {
  const v = typeof detail.value?.preview === 'string' ? detail.value.preview : ''
  const ok = await copyText(v)
  if (ok) ElMessage.success('已复制到剪贴板')
  else ElMessage.warning('复制失败，请手动选择内容')
}

function openNewKey() {
  keyForm.editing = false
  keyForm.key = ''
  keyForm.keyRaw = ''
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
  // 二进制键名在输入框里只能显示转义形式，真正写回时用原始字节
  keyForm.keyRaw = detail.value.key_raw || ''
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
    const res = await api<any>('/api/redis/key', {
      method: 'PUT',
      query: { db: db.value },
      body: {
        key: keyForm.key,
        key_raw: keyForm.keyRaw || '',
        type: keyForm.type,
        field: keyForm.field,
        value: keyForm.value,
        ttl: keyForm.ttl || 0
      }
    })
    ElMessage.success('已保存')
    keyVisible.value = false
    await loadKeys()
    // 新建的键直接打开详情；编辑中的键刷新内容
    if (!keyForm.editing || selectedKey.value) {
      await openKey(res?.key || keyForm.key, res?.key_raw || keyForm.keyRaw || '')
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    keySaving.value = false
  }
}

// removeKey 删除键。key 是展示名（用于确认框），keyRaw 是原始字节。
async function removeKey(key: string, keyRaw = '', fromPanel = false) {
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
    await api('/api/redis/key', { method: 'DELETE', query: { db: db.value, key, key_raw: keyRaw } })
    ElMessage.success('已删除')
    // 先收起详情，否则刷新键列表时 preserveExpansion 会把这个已删的键的父目录又补回来
    if (fromPanel || selectedKey.value === key) clearSelection()
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
  const key = detail.value.key
  const keyRaw = detail.value.key_raw || ''
  ttlSaving.value = true
  try {
    await api('/api/redis/key/ttl', {
      method: 'POST',
      query: { db: db.value },
      body: { key, key_raw: keyRaw, ttl: ttlValue.value || 0 }
    })
    ElMessage.success('已更新')
    ttlVisible.value = false
    await openKey(key, keyRaw)
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
.split {
  display: grid;
  /* 列宽由 useSplitWidth 的 :style 内联给出，这里只作兜底 */
  grid-template-columns: 320px 12px minmax(0, 1fr);
  gap: 0;
}
.side-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px 8px;
  border-bottom: 1px solid var(--line);
}
/* 左侧键名树 */
.tree-row {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  min-width: 0;
  padding-right: 8px;
  overflow: hidden;
}
.db-ico {
  color: var(--ink-3);
  flex: none;
}
.db-name {
  font-size: 12.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tree-meta {
  font-size: 11px;
  flex: none;
}
.type-tag {
  flex: none;
  transform: scale(0.88);
  transform-origin: left center;
}
.sep-input :deep(.el-input__inner) {
  text-align: center;
  font-family: 'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace;
}
:deep(.el-tree-node__content) {
  height: 30px;
}
:deep(.el-tree-node.is-current > .el-tree-node__content) {
  background: #eef3ff;
}
.key-link {
  cursor: pointer;
  color: var(--brand);
}
.key-link:hover {
  text-decoration: underline;
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
  max-height: calc(100vh - 520px);
  overflow-y: auto;
  margin: 0;
}
.list-preview {
  border: 1px solid var(--line);
  border-radius: 8px;
  max-height: calc(100vh - 520px);
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
