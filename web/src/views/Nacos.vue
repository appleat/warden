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
      <!-- 状态条：只放连接信息 -->
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
          <el-button :icon="Setting" @click="openConn">连接设置</el-button>
        </div>
      </div>

      <el-tabs v-model="tab" style="margin-top: 4px">
        <!-- 服务浏览 -->
        <el-tab-pane label="服务浏览" name="services">
          <div class="split" :style="svcSplitStyle" ref="svcSplitEl">
            <!-- 左：分组 / 服务 树 -->
            <div class="card side" style="padding: 0">
              <div class="side-head">
                <span class="card-title">服务</span>
                <div class="spacer"></div>
                <span class="muted" style="font-size: 11.5px">共 {{ fmtNum(svcTotal) }}</span>
                <el-button size="small" text :icon="Refresh" :loading="svcLoading" @click="loadServices" />
              </div>

              <div style="padding: 8px 10px 0">
                <el-input
                  v-model="svcKeyword"
                  size="small"
                  placeholder="服务名关键字"
                  clearable
                  @keyup.enter="loadServices"
                  @clear="loadServices"
                >
                  <template #prefix><el-icon><Search /></el-icon></template>
                </el-input>
              </div>

              <div class="row" style="padding: 6px 10px 0; gap: 6px">
                <el-input
                  v-model="svcGroup"
                  size="small"
                  placeholder="分组（留空=全部）"
                  clearable
                  @keyup.enter="loadServices"
                  @clear="loadServices"
                />
                <el-button size="small" @click="loadServices">查询</el-button>
              </div>

              <div class="row" style="padding: 2px 10px 2px; gap: 2px; align-items: center">
                <el-button link size="small" @click="expandSvcAll">展开全部</el-button>
                <el-button link size="small" @click="collapseSvcAll">折叠全部</el-button>
              </div>

              <el-scrollbar height="calc(100vh - 424px)">
                <el-tree
                  v-if="svcTree.length"
                  ref="svcTreeRef"
                  :data="svcTree"
                  node-key="key"
                  :props="{ label: 'label', children: 'children' }"
                  :expand-on-click-node="true"
                  highlight-current
                  style="padding: 2px 8px 8px 4px"
                  @node-click="onSvcClick"
                  @node-expand="onSvcExpand"
                  @node-collapse="onSvcCollapse"
                >
                  <template #default="{ data }">
                    <div class="tree-row">
                      <el-icon :size="13" class="db-ico">
                        <Folder v-if="data.kind === 'group'" />
                        <Connection v-else />
                      </el-icon>
                      <span class="mono db-name" :title="data.label">{{ data.label }}</span>
                      <div class="spacer"></div>
                      <el-tag
                        v-if="data.kind === 'service' && data.svc.ip_count >= 0"
                        size="small"
                        effect="plain"
                        class="type-tag"
                        :type="healthType(data.svc)"
                      >
                        {{ data.svc.healthy_count }}/{{ data.svc.ip_count }}
                      </el-tag>
                      <span v-else-if="data.kind === 'group'" class="muted tree-meta">
                        {{ fmtNum(data.children.length) }} 服务
                      </span>
                      <span v-else class="muted tree-meta">-</span>
                    </div>
                  </template>
                </el-tree>
                <div v-else-if="!svcLoading" class="empty-hint" style="padding: 24px 14px">
                  {{ svcHint || '该命名空间下没有服务' }}
                </div>
              </el-scrollbar>

              <div class="muted" style="padding: 0 14px 10px; font-size: 11.5px; line-height: 1.7">
                数据来源：<span class="mono">{{ svcSource || '-' }}</span>
                <template v-if="svcSource === 'service-list'"> · catalog 接口不可用，已回落，实例数显示为 -</template>
                <div v-if="svcTotal > svcItems.length" style="color: #a86a12">
                  共 {{ fmtNum(svcTotal) }} 个服务，仅加载前 {{ fmtNum(svcItems.length) }} 个，可用关键字缩小范围
                </div>
              </div>
            </div>

            <div
              class="split-bar"
              :class="{ 'is-dragging': svcSplitDragging }"
              title="拖动调整宽度，双击恢复默认"
              @pointerdown="svcSplitDown"
              @dblclick="resetSvcSplit"
            ></div>

            <!-- 右：实例详情（内嵌，不再弹抽屉） -->
            <div class="card" style="padding: 0; min-width: 0">
              <div v-if="!selected" class="empty-hint" style="padding: 80px 20px">
                在左侧展开分组并点击一个服务，这里会显示它的实例列表与健康状态
              </div>
              <div v-else v-loading="instLoading">
                <div class="side-head">
                  <span class="card-title mono" style="word-break: break-all">{{ selected.name }}</span>
                  <el-tag size="small" effect="plain" type="info">{{ selected.group_name }}</el-tag>
                  <div class="spacer"></div>
                  <el-checkbox v-model="healthyOnly" size="small" @change="loadInstances">仅健康</el-checkbox>
                  <el-button size="small" :icon="Refresh" @click="loadInstances" />
                </div>

                <div style="padding: 12px 14px 0">
                  <el-descriptions :column="4" border size="small">
                    <el-descriptions-item label="实例数">{{ fmtNum(inst.items?.length || 0) }}</el-descriptions-item>
                    <el-descriptions-item label="健康实例">{{ fmtNum(healthyCount) }}</el-descriptions-item>
                    <el-descriptions-item label="集群">{{ inst.clusters || '-' }}</el-descriptions-item>
                    <el-descriptions-item label="保护阈值">
                      <span :class="{ 'warn-text': inst.trigger_flag }">{{ inst.trigger_flag ? '已触发' : '正常' }}</span>
                    </el-descriptions-item>
                  </el-descriptions>

                  <div class="card-title" style="margin: 14px 0 8px">
                    <span>实例列表</span>
                    <div class="spacer"></div>
                    <span class="muted" style="font-size: 11.5px">
                      {{ healthyOnly ? '仅显示健康实例' : '全部实例' }}
                    </span>
                  </div>

                  <el-table
                    :data="inst.items || []"
                    size="small"
                    border
                    max-height="calc(100vh - 508px)"
                    empty-text="该服务暂无实例"
                  >
                    <el-table-column label="地址" min-width="170">
                      <template #default="{ row }">
                        <div class="mono" style="font-size: 12.5px">{{ row.ip }}:{{ row.port }}</div>
                        <div class="muted" style="font-size: 11px">
                          {{ row.ephemeral ? '临时实例' : '持久实例' }}
                          <span v-if="!row.enabled"> · 已下线</span>
                        </div>
                      </template>
                    </el-table-column>
                    <el-table-column label="健康" width="80">
                      <template #default="{ row }">
                        <el-tag size="small" :type="row.healthy ? 'success' : 'danger'" effect="light">
                          {{ row.healthy ? '健康' : '异常' }}
                        </el-tag>
                      </template>
                    </el-table-column>
                    <el-table-column label="集群" width="100" show-overflow-tooltip>
                      <template #default="{ row }">{{ row.cluster_name || 'DEFAULT' }}</template>
                    </el-table-column>
                    <el-table-column label="权重" width="72" align="right">
                      <template #default="{ row }"><span class="mono">{{ row.weight }}</span></template>
                    </el-table-column>
                    <el-table-column label="元数据" min-width="160">
                      <template #default="{ row }">
                        <div
                          v-for="(v, k) in row.metadata || {}"
                          :key="k"
                          class="mono meta-row"
                          style="font-size: 11.5px"
                        >
                          <span class="muted">{{ k }}</span> = {{ v }}
                        </div>
                        <span v-if="!metaText(row)" class="muted">-</span>
                      </template>
                    </el-table-column>
                  </el-table>
                </div>
              </div>
            </div>
          </div>
        </el-tab-pane>

        <!-- 配置管理 -->
        <el-tab-pane label="配置管理" name="configs">
          <div class="split" :style="cfgSplitStyle" ref="cfgSplitEl">
            <!-- 左：分组 / Data ID 树 -->
            <div class="card side" style="padding: 0">
              <div class="side-head">
                <span class="card-title">配置</span>
                <div class="spacer"></div>
                <span class="muted" style="font-size: 11.5px">共 {{ fmtNum(cfgTotal) }}</span>
                <el-button size="small" text :icon="Refresh" :loading="cfgLoading" @click="loadConfigs" />
                <el-button type="primary" size="small" :icon="Plus" @click="openCreate">新建</el-button>
              </div>

              <div style="padding: 8px 10px 0">
                <el-input
                  v-model="cfgKeyword"
                  size="small"
                  placeholder="Data ID 关键字"
                  clearable
                  @keyup.enter="loadConfigs"
                  @clear="loadConfigs"
                >
                  <template #prefix><el-icon><Search /></el-icon></template>
                </el-input>
              </div>

              <div class="row" style="padding: 6px 10px 0; gap: 6px">
                <el-input
                  v-model="cfgGroup"
                  size="small"
                  placeholder="分组（留空=全部）"
                  clearable
                  @keyup.enter="loadConfigs"
                  @clear="loadConfigs"
                />
                <el-button size="small" @click="loadConfigs">查询</el-button>
              </div>

              <div class="row" style="padding: 2px 10px 2px; gap: 2px; align-items: center">
                <el-button link size="small" @click="expandCfgAll">展开全部</el-button>
                <el-button link size="small" @click="collapseCfgAll">折叠全部</el-button>
              </div>

              <el-scrollbar height="calc(100vh - 424px)">
                <el-tree
                  v-if="cfgTree.length"
                  ref="cfgTreeRef"
                  :data="cfgTree"
                  node-key="key"
                  :props="{ label: 'label', children: 'children' }"
                  :expand-on-click-node="true"
                  highlight-current
                  style="padding: 2px 8px 8px 4px"
                  @node-click="onCfgClick"
                  @node-expand="onCfgExpand"
                  @node-collapse="onCfgCollapse"
                >
                  <template #default="{ data }">
                    <div class="tree-row">
                      <el-icon :size="13" class="db-ico">
                        <Folder v-if="data.kind === 'group'" />
                        <Document v-else />
                      </el-icon>
                      <span class="mono db-name" :title="data.label">{{ data.label }}</span>
                      <div class="spacer"></div>
                      <el-tag
                        v-if="data.kind === 'config' && data.cfg.type"
                        size="small"
                        effect="plain"
                        class="type-tag"
                      >{{ data.cfg.type }}</el-tag>
                      <span v-else-if="data.kind === 'group'" class="muted tree-meta">
                        {{ fmtNum(data.children.length) }} 项
                      </span>
                    </div>
                  </template>
                </el-tree>
                <div v-else-if="!cfgLoading" class="empty-hint" style="padding: 24px 14px">
                  没有配置
                </div>
              </el-scrollbar>

              <div v-if="cfgTotal > cfgItems.length" class="muted" style="padding: 0 14px 10px; font-size: 11.5px">
                共 {{ fmtNum(cfgTotal) }} 项，仅加载前 {{ fmtNum(cfgItems.length) }} 项，可用关键字缩小范围
              </div>
            </div>

            <div
              class="split-bar"
              :class="{ 'is-dragging': cfgSplitDragging }"
              title="拖动调整宽度，双击恢复默认"
              @pointerdown="cfgSplitDown"
              @dblclick="resetCfgSplit"
            ></div>

            <!-- 右：配置内容（内嵌） -->
            <div class="card" style="padding: 0; min-width: 0">
              <div v-if="!selectedCfg" class="empty-hint" style="padding: 80px 20px">
                在左侧点击一个配置，这里会显示它的内容与格式，可直接编辑并发布
              </div>
              <div v-else v-loading="contentLoading">
                <div class="side-head">
                  <span class="card-title mono" style="word-break: break-all">{{ selectedCfg.data_id }}</span>
                  <el-tag size="small" effect="plain">{{ selectedCfg.type || 'text' }}</el-tag>
                  <div class="spacer"></div>
                  <template v-if="cfgEditing">
                    <el-button size="small" @click="cancelEdit">取消</el-button>
                    <el-button type="primary" size="small" :loading="publishing" @click="saveEdit">发布</el-button>
                  </template>
                  <template v-else>
                    <el-button size="small" :disabled="contentLoading" @click="startEdit">编辑</el-button>
                    <el-button size="small" @click="copyContent">复制</el-button>
                    <el-button size="small" type="danger" plain @click="removeConfig(selectedCfg)">删除</el-button>
                    <el-button size="small" :icon="Refresh" @click="openCfg(selectedCfg)" />
                  </template>
                </div>

                <div style="padding: 12px 14px 0">
                  <el-descriptions :column="4" border size="small">
                    <el-descriptions-item label="分组">
                      <span class="mono">{{ selectedCfg.group }}</span>
                    </el-descriptions-item>
                    <el-descriptions-item label="格式">{{ selectedCfg.type || 'text' }}</el-descriptions-item>
                    <el-descriptions-item label="应用">{{ selectedCfg.app_name || '-' }}</el-descriptions-item>
                    <el-descriptions-item label="命名空间">
                      <span class="mono">{{ ns || 'public' }}</span>
                    </el-descriptions-item>
                  </el-descriptions>

                  <div class="card-title" style="margin: 14px 0 8px">
                    <span>{{ cfgEditing ? '编辑内容' : '配置内容' }}</span>
                    <div class="spacer"></div>
                    <span class="muted" style="font-size: 11.5px">
                      {{ selectedCfg.md5 ? 'MD5 ' + selectedCfg.md5 : '' }}
                    </span>
                  </div>

                  <template v-if="cfgEditing">
                    <el-input
                      v-model="cfgDraft"
                      type="textarea"
                      :rows="18"
                      :input-style="editorStyle"
                      placeholder="配置内容"
                    />
                    <div
                      v-if="isYamlCfg(selectedCfg.type, selectedCfg.data_id)"
                      class="yaml-line"
                      :class="cfgYaml.state"
                    >
                      <el-icon v-if="cfgYaml.state === 'checking'" class="is-loading"><Loading /></el-icon>
                      <el-icon v-else-if="cfgYaml.state === 'ok'"><CircleCheck /></el-icon>
                      <el-icon v-else-if="cfgYaml.state === 'error'"><WarningFilled /></el-icon>
                      <span>
                        {{
                          cfgYaml.state === 'idle'
                            ? 'YAML 格式：发布前会自动校验语法'
                            : cfgYaml.state === 'checking'
                              ? '正在校验 YAML 语法…'
                              : cfgYaml.msg
                        }}
                      </span>
                    </div>
                  </template>
                  <pre v-else class="preview mono">{{ cfgContent === '' ? '(空配置)' : cfgContent }}</pre>
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
                服务数 / 实例数来自 <span class="mono">/v1/ns/operator/metrics</span>，
                版本与环境来自 <span class="mono">/v1/console/server/state</span>
              </span>
              <div class="spacer"></div>
              <el-button size="small" :icon="Refresh" :loading="statusLoading" @click="loadStatus">刷新</el-button>
            </div>
            <div v-if="status.online" class="grid" style="grid-template-columns: repeat(auto-fit, minmax(220px, 1fr))">
              <div class="metric">
                <div class="metric-head"><span>服务数</span></div>
                <div class="metric-value">{{ fmtNum(status.service_count) }}</div>
                <div class="metric-foot">当前命名空间 <span class="mono">{{ nsLabel }}</span> 下的注册服务</div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>实例数</span></div>
                <div class="metric-value">{{ fmtNum(status.instance_count) }}</div>
                <div class="metric-foot">全部服务下的实例总数</div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>服务端 CPU</span></div>
                <div class="metric-value">
                  {{ status.cpu !== undefined && status.cpu !== null ? Math.round((status.cpu || 0) * 100) + '%' : '-' }}
                </div>
                <div class="metric-foot">
                  负载 {{ status.load !== undefined && status.load !== null ? status.load.toFixed(2) : '-' }}
                </div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>响应延迟</span></div>
                <div class="metric-value">
                  {{ status.latency_ms !== undefined ? status.latency_ms + ' ms' : '-' }}
                </div>
                <div class="metric-foot">最近一次探测服务端状态的时间</div>
              </div>
            </div>
            <div v-else class="empty-hint" style="padding: 30px">
              {{ status.error || '暂时拿不到运行指标，请检查连接是否正常' }}
            </div>

            <template v-if="status.online">
              <div class="card-title" style="margin: 18px 0 10px">服务端信息</div>
              <el-descriptions :column="2" border size="small">
                <el-descriptions-item label="地址"><span class="mono">{{ conn.addr }}</span></el-descriptions-item>
                <el-descriptions-item label="版本">{{ status.version || '-' }}</el-descriptions-item>
                <el-descriptions-item label="启动模式">{{ status.mode || '-' }}</el-descriptions-item>
                <el-descriptions-item label="鉴权">
                  {{ status.auth_enabled ? '已开启' : '未开启' }}
                </el-descriptions-item>
                <el-descriptions-item label="服务端环境">{{ status.server_env || '默认环境' }}</el-descriptions-item>
                <el-descriptions-item label="数据源">
                  <span class="mono">{{ status.datasource || '-' }}</span>
                </el-descriptions-item>
                <el-descriptions-item label="当前命名空间">
                  {{ nsLabel }} <span class="mono muted">（{{ ns || 'public' }}）</span>
                </el-descriptions-item>
                <el-descriptions-item label="配置项数量">{{ fmtNum(cfgTotal) }}</el-descriptions-item>
              </el-descriptions>
            </template>
          </div>
        </el-tab-pane>

        <!-- 接口诊断 -->
        <el-tab-pane label="接口诊断" name="diagnose">
          <div class="card" style="padding: 0">
            <div class="toolbar" style="padding: 12px 14px 8px; align-items: center">
              <span class="muted" style="font-size: 12px">
                逐个探测面板依赖的 OpenAPI，用于定位不同 Nacos 版本（1.x / 2.x / 3.x）的差异；
                当前命名空间 <span class="mono">{{ ns || 'public' }}</span>
              </span>
              <div class="spacer"></div>
              <el-button size="small" :icon="Tools" :loading="diagLoading" @click="loadDiagnose">重新探测</el-button>
            </div>
            <el-table :data="diagItems" v-loading="diagLoading" size="small" max-height="calc(100vh - 430px)" empty-text="还没有探测结果">
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

    <!-- 新建配置 -->
    <el-dialog v-model="editorVisible" title="新建配置" width="720px" top="7vh">
      <el-form :model="editor" label-width="80px">
        <div class="row" style="gap: 12px; align-items: flex-start">
          <el-form-item label="Data ID" required style="flex: 1">
            <el-input v-model="editor.data_id" placeholder="application-dev.yaml" />
          </el-form-item>
          <el-form-item label="分组" style="width: 200px">
            <el-select
              v-model="editor.group"
              filterable
              allow-create
              default-first-option
              placeholder="DEFAULT_GROUP"
            >
              <el-option v-for="g in groupOptions" :key="g" :label="g" :value="g" />
            </el-select>
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
            :input-style="editorStyle"
            placeholder="配置内容"
          />
          <div v-if="isYamlCfg(editor.type, editor.data_id)" class="yaml-line" :class="newYaml.state">
            <el-icon v-if="newYaml.state === 'checking'" class="is-loading"><Loading /></el-icon>
            <el-icon v-else-if="newYaml.state === 'ok'"><CircleCheck /></el-icon>
            <el-icon v-else-if="newYaml.state === 'error'"><WarningFilled /></el-icon>
            <span>
              {{
                newYaml.state === 'idle'
                  ? 'YAML 格式：发布前会自动校验语法'
                  : newYaml.state === 'checking'
                    ? '正在校验 YAML 语法…'
                    : newYaml.msg
              }}
            </span>
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editorVisible = false">取消</el-button>
        <el-button type="primary" :loading="publishing" @click="publishNew">发布</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Box,
  CircleCheck,
  Connection,
  Document,
  Folder,
  Loading,
  Plus,
  Refresh,
  Search,
  Setting,
  Tools,
  WarningFilled
} from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'
import { useSplitWidth } from '../useSplit'

const conn = ref<any>({ configured: false, addr: '', username: '', namespace: '', has_password: false })
const status = ref<any>({ online: false })
const statusLoading = ref(false)
const switching = ref(false)
const namespaces = ref<any[]>([])
// el-select 把空字符串当成「未选择」，public 命名空间（ID 为空串）需要一个哨兵值
const PUBLIC_NS = '__public__'
const nsSel = ref(PUBLIC_NS)
const ns = computed(() => (nsSel.value === PUBLIC_NS ? '' : nsSel.value))
const tab = ref('services')

// 服务浏览与配置管理各自记住自己的左栏宽度
const {
  style: svcSplitStyle,
  dragging: svcSplitDragging,
  container: svcSplitEl,
  onPointerDown: svcSplitDown,
  reset: resetSvcSplit
} = useSplitWidth({ storageKey: 'warden_split_nacos_svc', initial: 320, min: 200, max: 640 })
const {
  style: cfgSplitStyle,
  dragging: cfgSplitDragging,
  container: cfgSplitEl,
  onPointerDown: cfgSplitDown,
  reset: resetCfgSplit
} = useSplitWidth({ storageKey: 'warden_split_nacos_cfg', initial: 320, min: 200, max: 700 })

interface TreeNode {
  key: string
  label: string
  kind: 'group' | 'service' | 'config'
  svc?: any
  cfg?: any
  children: TreeNode[]
}

// 服务列表：后端一页最多 500 条，前端按分组组织成两层树
const svcItems = ref<any[]>([])
const svcTotal = ref(0)
const svcGroup = ref('')
const svcKeyword = ref('')
const svcSource = ref('')
const svcHint = ref('')
const svcLoading = ref(false)
const svcTreeRef = ref<any>(null)
// 展开状态只作「记录」用，**不要绑到 default-expanded-keys**：
// 那个 prop 一变就 setDefaultExpandedKeys → expand(autoExpandParent=true)，折叠会被撑回来
const svcExpanded = ref<string[]>([])
const selected = ref<any>(null)
const inst = ref<any>({ items: [] })
const instLoading = ref(false)
const healthyOnly = ref(false)

// 配置：同样是两层树（分组 → Data ID），右侧内嵌内容
const cfgItems = ref<any[]>([])
const cfgTotal = ref(0)
const cfgGroup = ref('')
const cfgKeyword = ref('')
const cfgLoading = ref(false)
const cfgTreeRef = ref<any>(null)
const cfgExpanded = ref<string[]>([])
const selectedCfg = ref<any>(null)
const cfgContent = ref('')
const contentLoading = ref(false)
const cfgEditing = ref(false)
const cfgDraft = ref('')

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

// 新建配置
const editorVisible = ref(false)
const publishing = ref(false)
const editor = reactive<any>({ data_id: '', group: 'DEFAULT_GROUP', type: 'text', content: '' })

const configTypes = ['text', 'json', 'yaml', 'properties', 'xml', 'html']

// ---------- YAML 格式校验 ----------
// Nacos 的发布接口不校验内容语法，缩进写错也能存进去，等应用拉取配置时才炸。
// 这里在编辑过程中（防抖）和发布前各调一次后端解析（internal/nacos/yaml.go），
// 只对 yaml 生效：格式字段是 yaml/yml，或格式留空但 Data ID 后缀是 .yaml/.yml。

interface YamlState {
  state: 'idle' | 'checking' | 'ok' | 'error'
  msg: string
}

const cfgYaml = ref<YamlState>({ state: 'idle', msg: '' })
const newYaml = ref<YamlState>({ state: 'idle', msg: '' })
let yamlTimer: ReturnType<typeof setTimeout> | null = null
let newYamlTimer: ReturnType<typeof setTimeout> | null = null

function isYamlCfg(typ?: string, dataId?: string): boolean {
  const t = (typ || '').toLowerCase().trim()
  if (t === 'yaml' || t === 'yml') return true
  // 明确选了别的格式就不按 YAML 处理，避免 properties/json 内容被误判
  if (t && t !== 'text') return false
  const d = (dataId || '').toLowerCase().trim()
  return d.endsWith('.yaml') || d.endsWith('.yml')
}

// 返回空串表示通过，否则是错误文案（后端已整理成「YAML 格式错误：第 N 行：...」）
async function checkYaml(content: string, typ: string, dataId: string): Promise<string> {
  if (!isYamlCfg(typ, dataId)) return ''
  try {
    const res = await api<any>('/api/nacos/config/validate', {
      method: 'POST',
      body: { type: typ, data_id: dataId, content }
    })
    // 校验不通过时接口仍是 200，用 valid 字段表示结果，避免边输入边刷 4xx
    return res?.valid === false ? res.msg || 'YAML 格式错误' : ''
  } catch (e) {
    return errMsg(e)
  }
}

function clearYamlTimer() {
  if (yamlTimer) {
    clearTimeout(yamlTimer)
    yamlTimer = null
  }
}

// 编辑区内容变化后防抖校验，避免每敲一个字都发请求
function scheduleYamlCheck() {
  clearYamlTimer()
  const row = selectedCfg.value
  if (!cfgEditing.value || !row || !isYamlCfg(row.type, row.data_id)) {
    cfgYaml.value = { state: 'idle', msg: '' }
    return
  }
  cfgYaml.value = { state: 'checking', msg: '' }
  yamlTimer = setTimeout(async () => {
    const err = await checkYaml(cfgDraft.value, row.type, row.data_id)
    cfgYaml.value = err ? { state: 'error', msg: err } : { state: 'ok', msg: 'YAML 语法正确' }
  }, 600)
}

function scheduleNewYamlCheck() {
  if (!isYamlCfg(editor.type, editor.data_id)) {
    newYaml.value = { state: 'idle', msg: '' }
    return
  }
  newYaml.value = { state: 'checking', msg: '' }
  const content = editor.content
  const typ = editor.type
  const dataId = editor.data_id
  const run = async () => {
    const err = await checkYaml(content, typ, dataId)
    newYaml.value = err ? { state: 'error', msg: err } : { state: 'ok', msg: 'YAML 语法正确' }
  }
  // 新建弹窗是独立编辑区，复用同一个防抖定时器会影响现有配置的校验，单独计一个
  if (newYamlTimer) clearTimeout(newYamlTimer)
  newYamlTimer = setTimeout(run, 600)
}

watch(cfgDraft, () => scheduleYamlCheck())
watch(
  () => [editor.content, editor.type, editor.data_id],
  () => {
    if (editorVisible.value) scheduleNewYamlCheck()
  }
)


const editorStyle = {
  fontFamily: "'SFMono-Regular', Consolas, 'Liberation Mono', Menlo, monospace",
  fontSize: '12.5px',
  lineHeight: '1.65'
}

const svcTree = computed<TreeNode[]>(() => buildSvcTree(svcItems.value))
const cfgTree = computed<TreeNode[]>(() => buildCfgTree(cfgItems.value))
const healthyCount = computed(() => (inst.value.items || []).filter((i: any) => i.healthy).length)

// 分组下拉：从已加载的配置里累积去重，切命名空间时清空
const knownGroups = ref<string[]>([])
const groupOptions = computed(() => {
  const set = new Set<string>(['DEFAULT_GROUP'])
  for (const g of knownGroups.value) if (g) set.add(g)
  return [...set].sort()
})

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

// ---------- 格式化 ----------

function fmtNum(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v)) return '-'
  return v.toLocaleString('zh-CN')
}

function healthType(svc: any) {
  if (svc.ip_count < 0) return 'info'
  if (svc.ip_count === 0) return 'warning'
  if (svc.healthy_count === 0) return 'danger'
  return 'success'
}

function metaText(row: any) {
  const md = row?.metadata
  if (!md || !Object.keys(md).length) return ''
  return Object.entries(md)
    .map(([k, v]) => `${k} = ${v}`)
    .join('\n')
}

// ---------- 树构建 ----------

// buildSvcTree 把扁平的服务列表按分组聚成两层树。
// 部分版本回落接口不返回 groupName，用当前过滤的分组兜底，避免全部挤进 DEFAULT_GROUP。
function buildSvcTree(items: any[]): TreeNode[] {
  const map = new Map<string, TreeNode>()
  for (const it of items) {
    const g = it.group_name || svcGroup.value || 'DEFAULT_GROUP'
    let node = map.get(g)
    if (!node) {
      node = { key: 'g:' + g, label: g, kind: 'group', children: [] }
      map.set(g, node)
    }
    node.children.push({
      key: 's:' + g + '/' + it.name,
      label: it.name,
      kind: 'service',
      svc: { ...it, group_name: g },
      children: []
    })
  }
  const out = [...map.values()]
  out.sort((a, b) => a.label.localeCompare(b.label))
  for (const n of out) n.children.sort((a, b) => a.label.localeCompare(b.label))
  return out
}

function buildCfgTree(items: any[]): TreeNode[] {
  const map = new Map<string, TreeNode>()
  for (const it of items) {
    const g = it.group || cfgGroup.value || 'DEFAULT_GROUP'
    let node = map.get(g)
    if (!node) {
      node = { key: 'g:' + g, label: g, kind: 'group', children: [] }
      map.set(g, node)
    }
    node.children.push({
      key: 'c:' + g + '/' + it.data_id,
      label: it.data_id,
      kind: 'config',
      cfg: { ...it, group: g },
      children: []
    })
  }
  const out = [...map.values()]
  out.sort((a, b) => a.label.localeCompare(b.label))
  for (const n of out) n.children.sort((a, b) => a.label.localeCompare(b.label))
  return out
}

function treeNodes(refObj: any): any[] {
  const map = refObj?.store?.nodesMap
  return map ? (Object.values(map) as any[]) : []
}

function groupKeys(nodes: TreeNode[]): string[] {
  return nodes.map((n) => n.key)
}

// applyExpansion 命令式展开：父节点先开，子节点才存在。
// 这是唯一设置展开状态的地方（不走 prop），所以折叠不会被反转。
function applyExpansion(refObj: any, paths: string[], store: 'svc' | 'cfg') {
  const want = [...new Set(paths)].sort((a, b) => a.length - b.length)
  nextTick(() => {
    const ok: string[] = []
    for (const p of want) {
      const node = refObj?.getNode?.(p)
      if (!node?.expand) continue
      node.expand()
      ok.push(p)
    }
    if (store === 'svc') svcExpanded.value = ok
    else cfgExpanded.value = ok
  })
}

function expandSvcAll() {
  applyExpansion(svcTreeRef.value, groupKeys(svcTree.value), 'svc')
}

function collapseSvcAll() {
  for (const n of treeNodes(svcTreeRef.value)) if (n.expanded) n.collapse?.()
  svcExpanded.value = []
}

function expandCfgAll() {
  applyExpansion(cfgTreeRef.value, groupKeys(cfgTree.value), 'cfg')
}

function collapseCfgAll() {
  for (const n of treeNodes(cfgTreeRef.value)) if (n.expanded) n.collapse?.()
  cfgExpanded.value = []
}

// 列表刷新后尽量保留原展开状态；从没展开过（首次进入）就把分组都摊开
function restoreSvcExpansion() {
  const alive = new Set(groupKeys(svcTree.value))
  const keep = svcExpanded.value.filter((k) => alive.has(k))
  applyExpansion(svcTreeRef.value, keep.length ? keep : [...alive], 'svc')
}

function restoreCfgExpansion() {
  const alive = new Set(groupKeys(cfgTree.value))
  const keep = cfgExpanded.value.filter((k) => alive.has(k))
  applyExpansion(cfgTreeRef.value, keep.length ? keep : [...alive], 'cfg')
}

function onSvcExpand(data: TreeNode) {
  if (!svcExpanded.value.includes(data.key)) svcExpanded.value = [...svcExpanded.value, data.key]
}

function onSvcCollapse(data: TreeNode) {
  svcExpanded.value = svcExpanded.value.filter((k) => k !== data.key)
}

function onCfgExpand(data: TreeNode) {
  if (!cfgExpanded.value.includes(data.key)) cfgExpanded.value = [...cfgExpanded.value, data.key]
}

function onCfgCollapse(data: TreeNode) {
  cfgExpanded.value = cfgExpanded.value.filter((k) => k !== data.key)
}

// 树数据变化后（DOM 已经更新）再命令式恢复展开状态。
// 不能放在 loadServices/loadConfigs 里：首次加载时树还挂在 v-if 上，getNode 拿不到节点。
watch(svcTree, (list) => {
  if (list.length) restoreSvcExpansion()
}, { flush: 'post' })
watch(cfgTree, (list) => {
  if (list.length) restoreCfgExpansion()
}, { flush: 'post' })

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

// reloadAll 把服务、配置一起拉一遍：两个页签共用同一个命名空间，
// 切命名空间后任何一个页签都不能留旧数据。
async function reloadAll() {
  switching.value = true
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
    selectedCfg.value = null
    cfgContent.value = ''
    cfgEditing.value = false
    svcExpanded.value = []
    cfgExpanded.value = []
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

// ---------- 服务 / 实例 ----------

async function loadServices() {
  svcLoading.value = true
  const prev = selected.value?.name
  try {
    const res = await api<any>('/api/nacos/services', {
      query: {
        namespace: ns.value,
        group: svcGroup.value,
        keyword: svcKeyword.value,
        page: 1,
        size: 500
      }
    })
    svcItems.value = res?.items || []
    svcTotal.value = res?.total || 0
    svcSource.value = res?.source || ''
    svcHint.value = res?.hint || ''
    // 选中项已不在列表里就清空右侧
    if (prev && !svcItems.value.some((s: any) => s.name === prev)) {
      selected.value = null
      inst.value = { items: [] }
    } else if (prev) {
      // 选中的服务还在，重建树后高亮会丢，补回来
      const g = selected.value.group_name
      nextTick(() => svcTreeRef.value?.setCurrentKey?.(`s:${g}/${prev}`))
    }
  } catch (e) {
    svcItems.value = []
    svcTotal.value = 0
    svcSource.value = ''
    svcHint.value = ''
    selected.value = null
    inst.value = { items: [] }
    ElMessage.error(errMsg(e))
  } finally {
    svcLoading.value = false
  }
}

function onSvcClick(data: TreeNode) {
  if (data.kind !== 'service') return
  selectService(data.svc, data.key)
}

async function selectService(row: any, key?: string) {
  selected.value = row
  if (key) nextTick(() => svcTreeRef.value?.setCurrentKey?.(key))
  await loadInstances()
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

async function loadConfigs() {
  cfgLoading.value = true
  const prev = selectedCfg.value
  try {
    const res = await api<any>('/api/nacos/configs', {
      query: {
        namespace: ns.value,
        group: cfgGroup.value,
        data_id: cfgKeyword.value,
        page: 1,
        size: 500
      }
    })
    cfgItems.value = res?.items || []
    cfgTotal.value = res?.total || 0
    for (const c of cfgItems.value) {
      if (c.group && !knownGroups.value.includes(c.group)) knownGroups.value.push(c.group)
    }
    const still = prev && cfgItems.value.find((c: any) => c.data_id === prev.data_id)
    if (!still) {
      selectedCfg.value = null
      cfgContent.value = ''
      cfgEditing.value = false
    } else {
      nextTick(() => cfgTreeRef.value?.setCurrentKey?.(`c:${still.group || cfgGroup.value || 'DEFAULT_GROUP'}/${still.data_id}`))
    }
  } catch (e) {
    cfgItems.value = []
    cfgTotal.value = 0
    ElMessage.error(errMsg(e))
  } finally {
    cfgLoading.value = false
  }
}

function onCfgClick(data: TreeNode) {
  if (data.kind !== 'config') return
  openCfg(data.cfg, data.key)
}

async function openCfg(row: any, key?: string) {
  selectedCfg.value = row
  cfgEditing.value = false
  cfgDraft.value = ''
  contentLoading.value = true
  cfgContent.value = ''
  if (key) nextTick(() => cfgTreeRef.value?.setCurrentKey?.(key))
  try {
    const res = await api<any>('/api/nacos/config', {
      query: { namespace: ns.value, group: row.group, data_id: row.data_id }
    })
    cfgContent.value = res?.content || ''
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    contentLoading.value = false
  }
}

function startEdit() {
  cfgYaml.value = { state: 'idle', msg: '' }
  cfgDraft.value = cfgContent.value
  cfgEditing.value = true
}

function cancelEdit() {
  clearYamlTimer()
  cfgYaml.value = { state: 'idle', msg: '' }
  cfgEditing.value = false
  cfgDraft.value = ''
}

async function saveEdit() {
  if (!selectedCfg.value) return
  const row = selectedCfg.value

  // 发布前再校验一次（后端发布接口也有同样的拦截，这里只是让提示更早、更明确）
  if (isYamlCfg(row.type, row.data_id)) {
    clearYamlTimer()
    cfgYaml.value = { state: 'checking', msg: '' }
    const err = await checkYaml(cfgDraft.value, row.type, row.data_id)
    if (err) {
      cfgYaml.value = { state: 'error', msg: err }
      ElMessage.error('YAML 格式有误，修正后再发布')
      return
    }
    cfgYaml.value = { state: 'ok', msg: 'YAML 语法正确' }
  }

  publishing.value = true
  try {
    await api('/api/nacos/config', {
      method: 'PUT',
      body: {
        namespace: ns.value,
        group: row.group,
        data_id: row.data_id,
        content: cfgDraft.value,
        type: row.type || 'text'
      }
    })
    ElMessage.success('配置已发布')
    cfgEditing.value = false
    cfgContent.value = cfgDraft.value
    cfgDraft.value = ''
    await loadConfigs()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    publishing.value = false
  }
}

function openCreate() {
  newYaml.value = { state: 'idle', msg: '' }
  editor.data_id = ''
  editor.group = cfgGroup.value || selectedCfg.value?.group || 'DEFAULT_GROUP'
  editor.type = 'text'
  editor.content = ''
  editorVisible.value = true
}

async function publishNew() {
  if (!editor.data_id) return ElMessage.warning('请填写 Data ID')
  if (!editor.group) return ElMessage.warning('请填写分组')

  if (isYamlCfg(editor.type, editor.data_id)) {
    if (newYamlTimer) {
      clearTimeout(newYamlTimer)
      newYamlTimer = null
    }
    newYaml.value = { state: 'checking', msg: '' }
    const err = await checkYaml(editor.content, editor.type, editor.data_id)
    if (err) {
      newYaml.value = { state: 'error', msg: err }
      ElMessage.error('YAML 格式有误，修正后再发布')
      return
    }
    newYaml.value = { state: 'ok', msg: 'YAML 语法正确' }
  }

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
    // 新建完直接打开它，省得再点一次
    const hit = cfgItems.value.find((c: any) => c.data_id === editor.data_id)
    if (hit) await openCfg({ ...hit, group: hit.group || editor.group })
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    publishing.value = false
  }
}

async function copyContent() {
  const ok = await copyText(cfgContent.value)
  if (ok) ElMessage.success('已复制到剪贴板')
  else ElMessage.warning('复制失败，请手动选择内容')
}

async function removeConfig(row: any) {
  if (!row) return
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
    // 先收起详情，否则刷新时会把已删的配置又当成选中项
    selectedCfg.value = null
    cfgContent.value = ''
    cfgEditing.value = false
    await loadConfigs()
    await loadStatus()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

// ---------- 接口兼容诊断 ----------

const diagLoading = ref(false)
const diagItems = ref<any[]>([])

async function loadDiagnose() {
  diagLoading.value = true
  try {
    const res = await api<any>('/api/nacos/diagnose', { query: { namespace: ns.value } })
    diagItems.value = res?.items || []
  } catch (e) {
    diagItems.value = []
    ElMessage.error(errMsg(e))
  } finally {
    diagLoading.value = false
  }
}

watch(tab, (v) => {
  if (v === 'metrics') loadStatus()
  if (v === 'diagnose' && !diagItems.value.length) loadDiagnose()
})

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
  clearYamlTimer()
  if (newYamlTimer) clearTimeout(newYamlTimer)
})
</script>

<style scoped>
/* YAML 校验状态行：编辑区下方一行小字，按状态变色 */
.yaml-line {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  margin-top: 8px;
  font-size: 12px;
  line-height: 1.5;
  color: var(--ink-3);
  word-break: break-word;
}
.yaml-line .el-icon {
  flex: none;
  margin-top: 2px;
}
.yaml-line.ok {
  color: var(--el-color-success);
}
.yaml-line.error {
  color: var(--el-color-danger);
}

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
/* 左侧树 */
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
:deep(.el-tree-node__content) {
  height: 30px;
}
:deep(.el-tree-node.is-current > .el-tree-node__content) {
  background: #eef3ff;
}
.meta-row {
  line-height: 1.6;
  word-break: break-all;
}
.warn-text {
  color: #b25a00;
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
  max-height: calc(100vh - 430px);
  overflow-y: auto;
  margin: 0 0 12px;
}
</style>
