<template>
  <div>
    <!-- 未配置连接 -->
    <div v-if="!conn.configured" class="card" style="text-align: center; padding: 48px 0">
      <el-icon :size="34" style="color: var(--ink-3)"><Coin /></el-icon>
      <div style="font-size: 14px; font-weight: 600; margin: 12px 0 6px">当前主机还没有配置 MySQL 连接</div>
      <div class="muted" style="font-size: 12.5px; line-height: 1.9">
        填写数据库地址与账号，即可在这里浏览库表、查看运行指标、执行 SQL 并管理会话。
        <br />
        MySQL 只监听 <code class="mono">127.0.0.1</code> 时地址填 <code class="mono">127.0.0.1</code> 即可（连接由该主机的 Agent 发起）。
      </div>
      <el-button type="primary" style="margin-top: 18px" @click="openConn">配置连接</el-button>
    </div>

    <template v-else>
      <!-- 状态条 -->
      <div class="card" style="padding: 14px 16px">
        <div class="row" style="flex-wrap: wrap">
          <span class="stat-pill" :class="overview.online ? 'ok' : 'bad'">
            <span class="dot" :class="overview.online ? 'green' : 'red'"></span>
            {{ overview.online ? '已连接' : '连接失败' }}
          </span>
          <span class="mono" style="font-size: 12.5px">{{ conn.host }}:{{ conn.port }}</span>
          <span class="muted" style="font-size: 12px">{{ conn.username }}</span>
          <el-tag v-if="overview.version" size="small" effect="plain">MySQL {{ overview.version }}</el-tag>
          <el-tag v-if="conn.tls" size="small" effect="plain" type="warning">TLS</el-tag>
          <el-tag v-if="conn.read_only" size="small" effect="plain" type="info">只读模式</el-tag>
          <el-tag v-else size="small" effect="plain" type="danger">可写</el-tag>
          <span v-if="overview.online && overview.latency_ms !== undefined" class="muted" style="font-size: 12px">
            {{ overview.latency_ms }} ms
          </span>
          <span v-if="!overview.online && overview.error" class="muted" style="font-size: 12px">
            {{ overview.error }}
          </span>
          <div class="spacer"></div>
          <span class="muted" style="font-size: 12px">当前库</span>
          <el-select v-model="schema" style="width: 190px" filterable :loading="dbLoading" @change="onSchemaChange">
            <el-option v-for="d in databases" :key="d.name" :label="d.name" :value="d.name">
              <div class="row" style="justify-content: space-between; gap: 14px">
                <span class="mono">{{ d.name }}</span>
                <span class="mono muted" style="font-size: 11.5px">{{ fmtSize(d.data_size + d.index_size) }}</span>
              </div>
            </el-option>
          </el-select>
          <el-button :icon="Refresh" :loading="loading" @click="reloadAll">刷新</el-button>
          <el-button :icon="Setting" @click="openConn">连接设置</el-button>
        </div>

      </div>

      <el-tabs v-model="tab" style="margin-top: 4px">
        <!-- 库表浏览 -->
        <el-tab-pane label="库表浏览" name="explore">
          <div class="split" :style="splitStyle" ref="splitEl">
            <!-- 左：库 / 表 树 -->
            <div class="card side" style="padding: 0">
              <div class="side-head">
                <span class="card-title">数据库</span>
                <div class="spacer"></div>
                <el-checkbox v-model="showSystem" size="small" @change="syncTreeRoot(true)">系统库</el-checkbox>
                <el-button size="small" text :icon="Refresh" @click="syncTreeRoot(true)" />
              </div>
              <div style="padding: 8px 10px 2px">
                <el-input v-model="treeFilter" size="small" placeholder="过滤库名 / 表名" clearable>
                  <template #prefix><el-icon><Search /></el-icon></template>
                </el-input>
              </div>
              <el-scrollbar height="calc(100vh - 356px)">
                <el-tree
                  ref="dbTreeRef"
                  :key="treeKey"
                  :props="treeProps"
                  :load="loadNode"
                  :filter-node-method="filterTreeNode"
                  :current-node-key="currentKey"
                  lazy
                  node-key="key"
                  :expand-on-click-node="false"
                  highlight-current
                  empty-text="没有数据库"
                  @node-click="onNodeClick"
                >
                  <template #default="{ data }">
                    <div class="tree-row">
                      <el-icon :size="13" class="db-ico">
                        <Coin v-if="data.kind === 'db'" />
                        <Grid v-else />
                      </el-icon>
                      <span class="mono db-name">{{ data.label }}</span>
                      <el-tag
                        v-if="data.type === 'view'"
                        size="small"
                        effect="plain"
                        type="info"
                        class="view-tag"
                      >视图</el-tag>
                      <div class="spacer"></div>
                      <span v-if="data.kind === 'db'" class="muted tree-meta">{{ fmtNum(data.tables) }} 表</span>
                      <span v-else class="muted tree-meta">{{ fmtSize(data.data_size) }}</span>
                    </div>
                  </template>
                </el-tree>
              </el-scrollbar>
            </div>

            <div
              class="split-bar"
              :class="{ 'is-dragging': splitDragging }"
              title="拖动调整宽度，双击恢复默认"
              @pointerdown="onSplitDown"
              @dblclick="resetSplitWidth"
            ></div>

            <!-- 右：表详情（内嵌，不再弹抽屉） -->
            <div class="card" style="padding: 0; min-width: 0">
              <div v-if="!currentTable" class="empty-hint" style="padding: 80px 20px">
                在左侧展开数据库并点击一张表，这里会显示字段、索引、数据与建表语句
              </div>
              <div v-else v-loading="tableDetailLoading">
                <div class="side-head">
                  <span class="card-title mono">{{ schema }}.{{ currentTable }}</span>
                  <el-tag v-if="currentTableType === 'view'" size="small" effect="plain" type="info">视图</el-tag>
                  <div class="spacer"></div>
                  <span class="muted" style="font-size: 12px">
                    {{ fmtNum((tableDetail?.columns || []).length) }} 字段 ·
                    {{ fmtNum((tableDetail?.indexes || []).length) }} 索引
                  </span>
                  <el-button v-if="currentTableType === 'table'" size="small" @click="gotoExport(currentTable)">
                    导出
                  </el-button>
                  <el-button size="small" :icon="Refresh" @click="selectTable(currentTable)" />
                </div>

                <el-tabs v-model="detailTab" class="detail-tabs">
                  <el-tab-pane label="字段" name="structure">
                    <el-table :data="tableDetail?.columns || []" size="small" max-height="calc(100vh - 420px)">
                      <el-table-column label="字段" min-width="150">
                        <template #default="{ row }">
                          <span class="mono">{{ row.name }}</span>
                          <el-tag v-if="row.key === 'PRI'" size="small" type="danger" effect="plain" style="margin-left: 5px">PK</el-tag>
                          <el-tag v-else-if="row.key === 'UNI'" size="small" type="warning" effect="plain" style="margin-left: 5px">UQ</el-tag>
                          <el-tag v-else-if="row.key === 'MUL'" size="small" effect="plain" style="margin-left: 5px">IDX</el-tag>
                        </template>
                      </el-table-column>
                      <el-table-column label="类型" width="160">
                        <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.type }}</span></template>
                      </el-table-column>
                      <el-table-column label="可空" width="66" align="center">
                        <template #default="{ row }">
                          <span :class="row.nullable ? 'muted' : ''">{{ row.nullable ? 'YES' : 'NO' }}</span>
                        </template>
                      </el-table-column>
                      <el-table-column label="默认值" width="140">
                        <template #default="{ row }">
                          <span class="mono" :class="{ muted: row.default === '' }">
                            {{ row.default === '' ? 'NULL' : row.default }}
                          </span>
                        </template>
                      </el-table-column>
                      <el-table-column label="额外" width="150">
                        <template #default="{ row }">
                          <span class="muted" style="font-size: 11.5px">{{ row.extra || '-' }}</span>
                        </template>
                      </el-table-column>
                      <el-table-column label="注释" min-width="140">
                        <template #default="{ row }">
                          <span class="muted" style="font-size: 12px">{{ row.comment || '-' }}</span>
                        </template>
                      </el-table-column>
                      <el-table-column label="操作" width="72" align="center" fixed="right">
                        <template #default="{ row }">
                          <el-tooltip
                            :disabled="canAlterColumn(row)"
                            :content="columnEditHint(row)"
                            placement="left"
                          >
                            <span>
                              <el-button
                                size="small"
                                text
                                type="primary"
                                :disabled="!canAlterColumn(row)"
                                @click="openColumnEdit(row)"
                              >
                                修改
                              </el-button>
                            </span>
                          </el-tooltip>
                        </template>
                      </el-table-column>
                    </el-table>
                  </el-tab-pane>

                  <el-tab-pane label="索引" name="indexes">
                    <el-table :data="tableDetail?.indexes || []" size="small" empty-text="没有索引">
                      <el-table-column label="索引名" min-width="180">
                        <template #default="{ row }"><span class="mono">{{ row.name }}</span></template>
                      </el-table-column>
                      <el-table-column label="类型" width="110">
                        <template #default="{ row }">
                          <el-tag
                            size="small"
                            effect="plain"
                            :type="row.name === 'PRIMARY' ? 'danger' : row.unique ? 'warning' : 'info'"
                          >
                            {{ row.name === 'PRIMARY' ? '主键' : row.unique ? '唯一' : '普通' }}
                          </el-tag>
                        </template>
                      </el-table-column>
                      <el-table-column label="算法" width="110">
                        <template #default="{ row }">{{ row.type }}</template>
                      </el-table-column>
                      <el-table-column label="列" min-width="220">
                        <template #default="{ row }">
                          <span class="mono" style="font-size: 12px">{{ row.columns.join(', ') }}</span>
                        </template>
                      </el-table-column>
                      <el-table-column label="基数" width="110" align="right">
                        <template #default="{ row }"><span class="mono">{{ fmtNum(row.cardinality) }}</span></template>
                      </el-table-column>
                    </el-table>
                  </el-tab-pane>

                  <el-tab-pane label="数据" name="data">
                    <div class="row" style="margin-bottom: 10px; gap: 8px">
                      <span class="muted" style="font-size: 12px">排序</span>
                      <el-select v-model="rowOrder" style="width: 180px" clearable placeholder="默认顺序" @change="loadRows(true)">
                        <el-option v-for="c in tableDetail?.columns || []" :key="c.name" :label="c.name" :value="c.name" />
                      </el-select>
                      <el-checkbox v-model="rowDesc" @change="loadRows(true)">倒序</el-checkbox>
                      <div class="spacer"></div>
                      <el-tooltip
                        v-if="!rowEditable"
                        content="该表没有主键，无法唯一定位行，因此不提供行编辑"
                        placement="top"
                      >
                        <el-tag size="small" effect="plain" type="warning">无主键 · 不可编辑</el-tag>
                      </el-tooltip>
                      <span class="muted" style="font-size: 12px">
                        第 {{ rowPage.page }} 页 · {{ fmtNum(rowPage.rows.length) }} 行
                        <span v-if="rowPage.cost_ms"> · {{ rowPage.cost_ms }} ms</span>
                      </span>
                      <el-button size="small" :disabled="rowPage.page <= 1" @click="pageRows(-1)">上一页</el-button>
                      <el-button size="small" :disabled="!rowPage.has_more" @click="pageRows(1)">下一页</el-button>
                      <el-button size="small" :icon="Refresh" @click="loadRows()" />
                    </div>
                    <el-table
                      :data="rowPage.rows"
                      v-loading="rowLoading"
                      size="small"
                      border
                      max-height="calc(100vh - 460px)"
                      empty-text="表里没有数据"
                    >
                      <el-table-column label="#" width="56" align="right">
                        <template #default="{ $index }">
                          <span class="muted mono">{{ (rowPage.page - 1) * rowPage.size + $index + 1 }}</span>
                        </template>
                      </el-table-column>
                      <el-table-column v-for="(c, ci) in rowPage.columns" :key="c + ci" :label="c" min-width="140">
                        <template #default="{ row }">
                          <span class="mono cell" :class="{ 'cell-null': row[ci] === null }">
                            {{ row[ci] === null ? 'NULL' : row[ci] }}
                          </span>
                        </template>
                      </el-table-column>
                      <el-table-column label="操作" width="72" align="center" fixed="right">
                        <template #default="{ $index }">
                          <el-tooltip
                            :disabled="rowEditable"
                            content="该表没有主键，无法唯一定位这一行"
                            placement="left"
                          >
                            <span>
                              <el-button
                                size="small"
                                text
                                type="primary"
                                :icon="EditPen"
                                :disabled="!rowEditable"
                                @click="openRowEdit($index)"
                              >
                                修改
                              </el-button>
                            </span>
                          </el-tooltip>
                        </template>
                      </el-table-column>
                    </el-table>
                  </el-tab-pane>

                  <el-tab-pane label="DDL" name="ddl">
                    <div class="row" style="margin-bottom: 8px">
                      <div class="spacer"></div>
                      <el-button size="small" @click="copyDDL">复制</el-button>
                    </div>
                    <pre class="preview mono">{{ tableDetail?.ddl || '没有取到建表语句' }}</pre>
                  </el-tab-pane>
                </el-tabs>
              </div>
            </div>
          </div>
        </el-tab-pane>

        <!-- 运行指标 -->
        <el-tab-pane label="运行指标" name="metrics">
          <div class="card">
            <div class="row" style="margin-bottom: 14px">
              <span class="muted" style="font-size: 12px">
                QPS 为自启动以来的累计平均值；缓冲池命中率来自 InnoDB 状态计数
              </span>
              <div class="spacer"></div>
              <el-button size="small" :icon="Refresh" :loading="loading" @click="loadOverview">刷新</el-button>
            </div>
            <div v-if="overview.online" class="grid" style="grid-template-columns: repeat(auto-fit, minmax(220px, 1fr))">
              <div class="metric">
                <div class="metric-head"><span>连接数</span></div>
                <div class="metric-value">{{ fmtNum(overview.threads_connected) }}</div>
                <div class="metric-foot">
                  活跃 {{ fmtNum(overview.threads_running) }} · 上限 {{ fmtNum(overview.max_connections) }}
                  <span v-if="overview.max_used_connections"> · 峰值 {{ fmtNum(overview.max_used_connections) }}</span>
                </div>
              </div>
              <div class="metric">
                <div class="metric-head">
                  <span>平均 QPS</span>
                  <span class="muted" style="font-size: 11px">自启动累计</span>
                </div>
                <div class="metric-value">{{ fmtFloat(overview.qps) }}</div>
                <div class="metric-foot">累计 {{ fmtNum(overview.queries) }} 条查询</div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>读写次数</span></div>
                <div class="metric-value" style="font-size: 18px">
                  {{ fmtNum(overview.com_select) }}
                  <span class="muted" style="font-size: 11px">SELECT</span>
                </div>
                <div class="metric-foot">
                  增 {{ fmtNum(overview.com_insert) }} · 改 {{ fmtNum(overview.com_update) }} · 删
                  {{ fmtNum(overview.com_delete) }}
                </div>
              </div>
              <div class="metric">
                <div class="metric-head"><span>数据规模</span></div>
                <div class="metric-value" style="font-size: 20px">
                  {{ fmtSize(overview.data_size + overview.index_size) }}
                </div>
                <div class="metric-foot">
                  {{ fmtNum(overview.database_count) }} 库 · {{ fmtNum(overview.table_count) }} 表 · 索引
                  {{ fmtSize(overview.index_size) }}
                </div>
              </div>
              <div class="metric">
                <div class="metric-head">
                  <span>缓冲池命中率</span>
                  <span class="muted" style="font-size: 11px">InnoDB</span>
                </div>
                <div class="metric-value">{{ fmtFloat(overview.buffer_pool_hit_rate) }}%</div>
                <div class="metric-foot">
                  池大小 {{ fmtSize(overview.buffer_pool_size) }} ·
                  磁盘读 {{ fmtNum(overview.buffer_pool_reads) }}
                </div>
              </div>
            </div>
            <div v-else class="empty-hint" style="padding: 30px">
              {{ overview.error || '暂时拿不到运行指标，请检查连接是否正常' }}
            </div>
          </div>
        </el-tab-pane>

        <!-- 会话 -->
        <el-tab-pane label="连接会话" name="sessions">
          <div class="card" style="padding: 0">
            <div class="toolbar" style="padding: 12px 14px 8px">
              <span class="muted" style="font-size: 12px">
                来自 information_schema.PROCESSLIST，共 {{ fmtNum(sessions.length) }} 个连接
              </span>
              <div class="spacer"></div>
              <el-button size="small" :icon="Refresh" :loading="sessionLoading" @click="loadSessions">刷新</el-button>
            </div>
            <el-table
              :data="sessions"
              v-loading="sessionLoading"
              size="small"
              max-height="calc(100vh - 430px)"
              empty-text="没有会话"
            >
              <el-table-column label="ID" width="86">
                <template #default="{ row }">
                  <span class="mono">{{ row.id }}</span>
                  <el-tag v-if="row.self" size="small" effect="plain" type="info" style="margin-left: 4px">本面板</el-tag>
                </template>
              </el-table-column>
              <el-table-column prop="user" label="账号" width="110" />
              <el-table-column prop="host" label="来源" width="170">
                <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.host }}</span></template>
              </el-table-column>
              <el-table-column prop="db" label="库" width="130">
                <template #default="{ row }"><span class="mono" style="font-size: 12px">{{ row.db || '-' }}</span></template>
              </el-table-column>
              <el-table-column prop="command" label="状态" width="100" />
              <el-table-column label="时长" width="90" align="right">
                <template #default="{ row }"><span class="mono">{{ fmtDuration(row.time) }}</span></template>
              </el-table-column>
              <el-table-column prop="state" label="阶段" width="150">
                <template #default="{ row }">
                  <span class="muted" style="font-size: 12px">{{ row.state || '-' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="正在执行" min-width="240">
                <template #default="{ row }">
                  <span class="mono" style="font-size: 11.5px; word-break: break-all">{{ row.info || '-' }}</span>
                </template>
              </el-table-column>
              <el-table-column label="操作" width="90" align="right">
                <template #default="{ row }">
                  <el-button
                    link
                    type="danger"
                    size="small"
                    :disabled="row.self"
                    @click="killSession(row)"
                  >
                    断开
                  </el-button>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-tab-pane>

        <!-- SQL 控制台 -->
        <el-tab-pane label="SQL 控制台" name="console">
          <div class="card">
            <div class="row" style="align-items: flex-start; gap: 10px">
              <el-input
                v-model="sqlInput"
                type="textarea"
                :rows="5"
                class="mono"
                :placeholder="conn.read_only ? '只读模式：仅允许 SELECT / SHOW / EXPLAIN 等查询语句' : '输入一条 SQL，Ctrl+Enter 执行'"
                @keydown.ctrl.enter.prevent="runSQL"
              />
              <div style="display: flex; flex-direction: column; gap: 6px">
                <el-button type="primary" :loading="sqlLoading" @click="runSQL">执行</el-button>
                <el-button :disabled="!history.length" @click="history = []">清空</el-button>
              </div>
            </div>
            <div class="row" style="margin-top: 8px; font-size: 12px; gap: 12px">
              <span class="muted">
                当前库 <code class="mono">{{ schema || '(未选择)' }}</code>
                <span v-if="conn.read_only"> · 只读模式已开启，写操作会被拒绝</span>
              </span>
              <div class="spacer"></div>
              <span class="muted">
                DROP DATABASE / GRANT / SET GLOBAL / 账号管理等危险语句已被面板禁用
              </span>
            </div>

            <div v-for="(h, i) in history" :key="i" class="cmd-block" style="margin-top: 14px">
              <div class="cmd-line">
                <span class="mono cmd-prompt">{{ conn.username }}@{{ schema || '-' }}&gt;</span>
                <span class="mono" style="word-break: break-all">{{ h.statement }}</span>
                <div class="spacer"></div>
                <span v-if="h.ok" class="muted" style="font-size: 11px">{{ h.cost_ms }} ms</span>
              </div>
              <div v-if="!h.ok" class="cmd-err mono">{{ h.error }}</div>
              <template v-else-if="h.kind === 'exec'">
                <div class="cmd-note">
                  执行成功，影响 <b>{{ fmtNum(h.rows_affected) }}</b> 行
                  <span v-if="h.last_insert_id"> · 自增 ID {{ h.last_insert_id }}</span>
                </div>
              </template>
              <template v-else>
                <div class="cmd-note">
                  返回 <b>{{ fmtNum(h.row_count) }}</b> 行
                  <span v-if="h.truncated">（已达上限，仅展示前 {{ h.row_count }} 行）</span>
                </div>
                <el-table :data="h.rows" size="small" max-height="360" border style="width: 100%">
                  <el-table-column
                    v-for="(c, ci) in h.columns"
                    :key="c + ci"
                    :label="c"
                    min-width="130"
                  >
                    <template #default="{ row }">
                      <span class="mono cell" :class="{ 'cell-null': row[ci] === null }">
                        {{ row[ci] === null ? 'NULL' : row[ci] }}
                      </span>
                    </template>
                  </el-table-column>
                  <template #empty><div class="empty-hint" style="padding: 14px">没有返回行</div></template>
                </el-table>
              </template>
            </div>
            <div v-if="!history.length" class="empty-hint" style="padding: 18px">
              执行过的语句与返回会显示在这里
            </div>
          </div>
        </el-tab-pane>

        <!-- 服务信息 -->
        <el-tab-pane label="服务信息" name="info">
          <div class="card" style="padding: 0">
            <div class="toolbar" style="padding: 12px 14px 8px; flex-wrap: wrap; gap: 8px">
              <el-radio-group v-model="infoScope" size="small" @change="loadVariables">
                <el-radio-button label="variables">全局变量</el-radio-button>
                <el-radio-button label="status">运行状态</el-radio-button>
              </el-radio-group>
              <el-input v-model="infoKeyword" placeholder="搜索键名" clearable style="width: 220px">
                <template #prefix><el-icon><Search /></el-icon></template>
              </el-input>
              <div class="spacer"></div>
              <span class="muted" style="font-size: 12px">{{ fmtNum(filteredInfo.length) }} 项</span>
            </div>
            <el-table :data="filteredInfo" v-loading="infoLoading" size="small" max-height="calc(100vh - 430px)">
              <el-table-column label="名称" width="300">
                <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
              </el-table-column>
              <el-table-column label="值">
                <template #default="{ row }">
                  <span class="mono" style="word-break: break-all">{{ row.value || '-' }}</span>
                </template>
              </el-table-column>
            </el-table>
          </div>
        </el-tab-pane>

        <!-- 导入导出 -->
        <el-tab-pane label="导入导出" name="transfer">
          <MySQLTransfer
            :schema="schema"
            :databases="databases"
            :tables="tables"
            :read-only="!!conn.read_only"
            :initial-table="transferTable"
          />
        </el-tab-pane>

        <!-- 建表 / 改表 -->
        <el-tab-pane label="建表改表" name="designer">
          <MySQLDesigner
            :schema="schema"
            :databases="databases"
            :tables="tables"
            :read-only="!!conn.read_only"
            @refresh="onDesignerChanged"
          />
        </el-tab-pane>

        <!-- 慢查询 -->
        <el-tab-pane label="慢查询" name="slow">
          <MySQLSlow :schema="schema" :databases="databases" />
        </el-tab-pane>
      </el-tabs>
    </template>

    <!-- 修改字段 -->
    <el-dialog
      v-model="colEditVisible"
      :title="`修改字段 ${colEditOrigin?.name || ''}`"
      width="620px"
      :close-on-click-modal="false"
    >
      <el-form label-width="88px" size="default">
        <el-form-item label="字段名" required>
          <el-input v-model="colForm.name" class="mono" placeholder="列名" />
        </el-form-item>
        <div class="row" style="gap: 10px; align-items: flex-start">
          <el-form-item label="类型" required style="flex: 1">
            <el-select v-model="colForm.type" filterable allow-create default-first-option style="width: 100%">
              <el-option-group v-for="g in typeGroups" :key="g.label" :label="g.label">
                <el-option v-for="t in g.options" :key="t" :label="t" :value="t" />
              </el-option-group>
            </el-select>
          </el-form-item>
          <el-form-item label="长度" label-width="52px" style="width: 156px">
            <el-input
              v-model="colForm.length"
              class="mono"
              :placeholder="colArgPlaceholder"
              :disabled="colNoArg"
            />
          </el-form-item>
        </div>
        <div class="row" style="gap: 8px; margin-bottom: 18px; padding-left: 4px">
          <el-checkbox v-model="colForm.unsigned" :disabled="!colIsNumeric">无符号</el-checkbox>
          <el-checkbox v-model="colForm.nullable">可空</el-checkbox>
          <el-checkbox v-model="colForm.auto_increment" :disabled="!colIsInteger">自增</el-checkbox>
          <el-checkbox v-model="colForm.on_update" :disabled="!colIsTime">ON UPDATE CURRENT_TIMESTAMP</el-checkbox>
        </div>
        <el-form-item label="默认值">
          <el-input v-model="colForm.default_text" class="mono" placeholder="留空表示不设默认值" />
          <div class="muted" style="font-size: 11.5px; margin-top: 4px">
            留空=不设默认值；<span class="mono">NULL</span>=默认 NULL（需可空）；
            <span class="mono">''</span>=默认空串；<span class="mono">CURRENT_TIMESTAMP</span>=当前时间
          </div>
        </el-form-item>
        <el-form-item label="注释">
          <el-input v-model="colForm.comment" placeholder="字段说明" />
        </el-form-item>
        <el-form-item label="执行语句">
          <div v-if="colPreviewError" class="alter-error">{{ colPreviewError }}</div>
          <pre v-else-if="colPreviewDDL" class="alter-preview mono">{{ colPreviewDDL }}</pre>
          <div v-else class="muted" style="font-size: 12.5px">没有检测到变化</div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="colEditVisible = false">取消</el-button>
        <el-button
          type="primary"
          :loading="colEditSaving"
          :disabled="!colPreviewDDL"
          @click="saveColumnEdit"
        >
          执行修改
        </el-button>
      </template>
    </el-dialog>

    <!-- 修改行数据 -->
    <el-dialog v-model="rowEditVisible" title="修改数据行" width="720px" :close-on-click-modal="false">
      <div class="muted" style="font-size: 12px; margin-bottom: 10px">
        仅提交有变化的字段，按主键
        <span class="mono">{{ (rowPage.pk || []).join(', ') || '-' }}</span>
        定位这一行，最多影响 1 行
      </div>
      <div class="row-edit-list">
        <div v-for="f in rowEditFields" :key="f.name" class="row-edit-item">
          <div class="row-edit-name">
            <span class="mono">{{ f.name }}</span>
            <el-tag v-if="f.pk" size="small" type="danger" effect="plain">PK</el-tag>
            <span class="muted" style="font-size: 11px">{{ f.type }}</span>
          </div>
          <div class="row-edit-value">
            <el-input
              v-model="f.text"
              size="small"
              :type="f.multiline ? 'textarea' : 'text'"
              :rows="2"
              :disabled="f.isNull || f.generated"
              :placeholder="f.generated ? '生成列，不可修改' : ''"
              class="mono"
            />
          </div>
          <div class="row-edit-null">
            <el-checkbox v-model="f.isNull" size="small" :disabled="f.generated || f.pk">NULL</el-checkbox>
          </div>
        </div>
      </div>
      <template #footer>
        <el-button @click="rowEditVisible = false">取消</el-button>
        <el-button type="primary" :loading="rowEditSaving" @click="saveRowEdit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 连接设置 -->
    <el-dialog v-model="connVisible" title="MySQL 连接设置" width="500px">
      <el-form label-width="96px">
        <el-form-item label="地址" required>
          <el-input v-model="connForm.host" placeholder="127.0.0.1" class="mono" />
          <div class="muted" style="font-size: 11.5px; margin-top: 4px">
            只对本机开放时填 127.0.0.1；连远端数据库填对应 IP 或域名
          </div>
        </el-form-item>
        <el-form-item label="端口" required>
          <el-input-number v-model="connForm.port" :min="1" :max="65535" controls-position="right" style="width: 150px" />
        </el-form-item>
        <el-form-item label="账号" required>
          <el-input v-model="connForm.username" placeholder="root" class="mono" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="connForm.password"
            type="password"
            show-password
            :placeholder="conn.has_password ? '已保存，留空则不修改' : '无密码可留空'"
          />
          <el-checkbox v-if="conn.has_password" v-model="connForm.clear_password" style="margin-top: 4px">
            清除已保存的密码
          </el-checkbox>
        </el-form-item>
        <el-form-item label="默认库">
          <el-input v-model="connForm.database" placeholder="可留空" class="mono" />
          <span class="muted" style="font-size: 11.5px; margin-left: 8px">页面上可随时切库</span>
        </el-form-item>
        <el-form-item label="字符集">
          <el-input v-model="connForm.charset" placeholder="utf8mb4" class="mono" style="width: 160px" />
        </el-form-item>
        <el-form-item label="TLS">
          <el-switch v-model="connForm.tls" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">服务端要求加密连接时打开</span>
        </el-form-item>
        <el-form-item v-if="connForm.tls" label="跳过校验">
          <el-switch v-model="connForm.insecure" />
          <span class="muted" style="font-size: 11.5px; margin-left: 10px">自签证书场景可打开</span>
        </el-form-item>
        <el-form-item label="只读模式">
          <el-switch v-model="connForm.read_only" />
          <div class="muted" style="font-size: 11.5px; margin-left: 10px; line-height: 1.6">
            开启后 SQL 控制台只允许 SELECT / SHOW / EXPLAIN，浏览与查看不受影响
          </div>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="connVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveConn">保存并测试</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, reactive, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { Coin, EditPen, Grid, Refresh, Search, Setting } from '@element-plus/icons-vue'
import { api, errMsg } from '../api'
import { copyText } from '../utils'
import { useSplitWidth } from '../useSplit'
import MySQLTransfer from '../components/MySQLTransfer.vue'
import MySQLDesigner from '../components/MySQLDesigner.vue'
import MySQLSlow from '../components/MySQLSlow.vue'

const conn = ref<any>({ configured: false, host: '', port: 3306, username: '', read_only: true })
const overview = ref<any>({ online: false })
const loading = ref(false)
const tab = ref('explore')

// 左侧库表树宽度可拖动
const {
  style: splitStyle,
  dragging: splitDragging,
  container: splitEl,
  onPointerDown: onSplitDown,
  reset: resetSplitWidth
} = useSplitWidth({ storageKey: 'warden_split_mysql', initial: 280, min: 180, max: 620 })

// 库 / 表
const databases = ref<any[]>([])
const dbLoading = ref(false)
const showSystem = ref(false)
const schema = ref('')
const tables = ref<any[]>([])
const tableLoading = ref(false)

// 表详情
const tableDetailLoading = ref(false)
const tableDetail = ref<any>(null)
const detailTab = ref('structure')
const currentTable = ref('')
const currentTableType = ref('table')

// 左侧库 / 表 树（懒加载：库节点展开时才去拉表）
const dbTreeRef = ref<any>(null)
const treeKey = ref(0)
const treeSig = ref('')
const treeFilter = ref('')
const treeProps = { label: 'label', isLeaf: 'leaf' }
const currentKey = computed(() =>
  currentTable.value ? 'tb:' + schema.value + '.' + currentTable.value : schema.value ? 'db:' + schema.value : ''
)

// 从表列表跳到「导入导出」时预填的表名
const transferTable = ref('')

// 表数据
const rowPage = ref<any>({ columns: [], rows: [], page: 1, size: 50, has_more: false, cost_ms: 0, pk: [] })
const rowLoading = ref(false)
const rowOrder = ref('')
const rowDesc = ref(false)

// 字段修改
const colEditVisible = ref(false)
const colEditSaving = ref(false)
const colEditOrigin = ref<any>(null)
const colPreviewDDL = ref('')
const colPreviewError = ref('')
const colForm = reactive<any>({
  name: '',
  type: '',
  length: '',
  unsigned: false,
  nullable: true,
  auto_increment: false,
  on_update: false,
  default_text: '',
  comment: ''
})
let colPreviewTimer: ReturnType<typeof setTimeout> | null = null
// 预览请求的序号：慢请求回来时若已过期就丢掉，避免旧结果覆盖新结果
let colPreviewSeq = 0

// 行数据修改
const rowEditVisible = ref(false)
const rowEditSaving = ref(false)
const rowEditIndex = ref(-1)
const rowEditFields = ref<any[]>([])

const typeGroups = [
  { label: '整数', options: ['tinyint', 'smallint', 'mediumint', 'int', 'bigint'] },
  { label: '小数', options: ['decimal', 'float', 'double'] },
  { label: '字符串', options: ['char', 'varchar', 'tinytext', 'text', 'mediumtext', 'longtext'] },
  { label: '二进制', options: ['binary', 'varbinary', 'tinyblob', 'blob', 'mediumblob', 'longblob'] },
  { label: '时间', options: ['date', 'datetime', 'timestamp', 'time', 'year'] },
  { label: '其他', options: ['json', 'enum', 'set', 'bool'] }
]

// 只有真正的表能改结构；视图要走 ALTER VIEW，不在这一页处理
const rowEditable = computed(
  () => currentTableType.value === 'table' && (rowPage.value.pk || []).length > 0
)

// 会话
const sessions = ref<any[]>([])
const sessionLoading = ref(false)

// SQL 控制台
const sqlInput = ref('')
const sqlLoading = ref(false)
const history = ref<any[]>([])

// 服务信息
const infoScope = ref('variables')
const infoKeyword = ref('')
const infoLoading = ref(false)
const infoSections = ref<Record<string, any[]>>({})

// 连接设置
const connVisible = ref(false)
const saving = ref(false)
const connForm = reactive<any>({
  host: '',
  port: 3306,
  username: '',
  password: '',
  database: '',
  charset: 'utf8mb4',
  tls: false,
  insecure: false,
  read_only: true,
  clear_password: false
})

const visibleDatabases = computed(() =>
  databases.value.filter((d) => showSystem.value || !d.system)
)

const filteredInfo = computed(() => {
  const list = infoSections.value[infoScope.value] || []
  const k = infoKeyword.value.trim().toLowerCase()
  if (!k) return list
  return list.filter((p: any) => p.key.toLowerCase().includes(k))
})

// ---------- 格式化 ----------

function fmtNum(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v)) return '-'
  return v.toLocaleString('zh-CN')
}

function fmtFloat(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v)) return '-'
  return v >= 100 ? v.toFixed(0) : v.toFixed(2)
}

function fmtSize(n: any) {
  const v = Number(n || 0)
  if (!Number.isFinite(v) || v <= 0) return '0 B'
  if (v < 1024) return v + ' B'
  const units = ['KB', 'MB', 'GB', 'TB']
  let val = v / 1024
  let i = 0
  while (val >= 1024 && i < units.length - 1) {
    val /= 1024
    i++
  }
  return val.toFixed(val >= 100 ? 0 : 1) + ' ' + units[i]
}

function fmtDuration(sec: number) {
  const v = Number(sec || 0)
  if (v < 60) return v + ' 秒'
  if (v < 3600) return Math.floor(v / 60) + ' 分 ' + (v % 60) + ' 秒'
  if (v < 86400) return Math.floor(v / 3600) + ' 小时 ' + Math.floor((v % 3600) / 60) + ' 分'
  return Math.floor(v / 86400) + ' 天 ' + Math.floor((v % 86400) / 3600) + ' 小时'
}

// ---------- 连接 ----------

async function loadConn() {
  try {
    const res = await api<any>('/api/mysql/conn')
    conn.value = res || { configured: false }
  } catch {
    conn.value = { configured: false }
  }
}

function openConn() {
  connForm.host = conn.value.host || '127.0.0.1'
  connForm.port = conn.value.port || 3306
  connForm.username = conn.value.username || ''
  connForm.password = ''
  connForm.database = conn.value.database || ''
  connForm.charset = conn.value.charset || 'utf8mb4'
  connForm.tls = !!conn.value.tls
  connForm.insecure = !!conn.value.insecure
  // 新建时默认只读，避免一上来就能改生产数据
  connForm.read_only = conn.value.configured ? conn.value.read_only !== false : true
  connForm.clear_password = false
  connVisible.value = true
}

async function saveConn() {
  if (!connForm.host) return ElMessage.warning('请填写数据库地址')
  if (!connForm.username) return ElMessage.warning('请填写数据库账号')
  saving.value = true
  try {
    await api('/api/mysql/conn', { method: 'PUT', body: { ...connForm } })
    connVisible.value = false
    ElMessage.success('已保存，正在测试连接')
    await loadConn()
    await reloadAll()
    if (overview.value.online) {
      ElMessage.success(`连接正常${overview.value.version ? '（MySQL ' + overview.value.version + '）' : ''}`)
    } else {
      ElMessage.warning(overview.value.error || '连接失败')
    }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    saving.value = false
  }
}

// ---------- 概览 / 库 ----------

async function loadOverview() {
  try {
    overview.value = await api<any>('/api/mysql/overview', { query: { db: schema.value } })
  } catch (e) {
    overview.value = { online: false, error: errMsg(e) }
  }
}

async function loadDatabases() {
  dbLoading.value = true
  try {
    const res = await api<any>('/api/mysql/databases')
    databases.value = res?.items || []
    // 默认库不在列表里（比如刚被删掉）时退回到第一个业务库
    const names = databases.value.map((d) => d.name)
    if (!schema.value || !names.includes(schema.value)) {
      const pref = databases.value.find((d) => d.name === conn.value.database)
      const first = databases.value.find((d) => !d.system)
      schema.value = pref?.name || first?.name || names[0] || ''
    }
    syncTreeRoot()
  } catch (e) {
    databases.value = []
    syncTreeRoot()
    ElMessage.error(errMsg(e))
  } finally {
    dbLoading.value = false
  }
}

async function loadTables() {
  if (!schema.value) {
    tables.value = []
    return
  }
  tableLoading.value = true
  try {
    const res = await api<any>('/api/mysql/tables', {
      query: { schema: schema.value }
    })
    tables.value = res?.items || []
  } catch (e) {
    tables.value = []
    ElMessage.error(errMsg(e))
  } finally {
    tableLoading.value = false
  }
}

async function reloadAll() {
  if (!conn.value.configured) return
  loading.value = true
  try {
    await loadOverview()
    if (!overview.value.online) return
    await loadDatabases()
    await loadTables()
    if (tab.value === 'sessions') await loadSessions()
    if (tab.value === 'info') await loadVariables()
  } finally {
    loading.value = false
  }
}

function onSchemaChange() {
  tables.value = []
  currentTable.value = ''
  currentTableType.value = 'table'
  tableDetail.value = null
  rowPage.value = { columns: [], rows: [], page: 1, size: 50, has_more: false, cost_ms: 0, pk: [] }
  loadTables()
  loadOverview()
  focusDbNode(schema.value)
  // 会话与变量是全局信息，切库不用重拉
}

function selectSchema(name: string) {
  if (schema.value === name) return
  schema.value = name
  onSchemaChange()
}

// ---------- 库 / 表 树 ----------

// 库列表变了（新增库、切系统库开关）时整棵树重建，否则只保留原有展开状态
function syncTreeRoot(force = false) {
  const sig = visibleDatabases.value.map((d) => d.name).join('|')
  if (!force && sig === treeSig.value) return
  treeSig.value = sig
  treeKey.value++
  // 懒加载树的 default-expanded-keys 不生效，重建后手动展开当前库
  focusDbNode(schema.value)
}

function focusDbNode(name: string) {
  nextTick(() => {
    const ref = dbTreeRef.value
    if (!ref || !name) return
    const node = ref.getNode?.('db:' + name)
    if (!node) return
    node.expand?.()
    ref.setCurrentKey?.('db:' + name)
  })
}

async function loadNode(node: any, resolve: (data: any[]) => void) {
  if (node.level === 0) {
    resolve(
      visibleDatabases.value.map((d) => ({
        key: 'db:' + d.name,
        label: d.name,
        name: d.name,
        kind: 'db',
        tables: d.tables,
        leaf: !d.tables
      }))
    )
    return
  }
  const db = node.data?.name || ''
  try {
    const res = await api<any>('/api/mysql/tables', { query: { schema: db } })
    resolve(
      (res?.items || []).map((t: any) => ({
        key: 'tb:' + db + '.' + t.name,
        label: t.name,
        name: t.name,
        db,
        kind: 'table',
        type: t.type,
        data_size: t.data_size,
        leaf: true
      }))
    )
  } catch {
    resolve([])
  } finally {
    // 过滤状态下懒加载完成的节点要重新参与过滤
    if (treeFilter.value.trim()) nextTick(() => dbTreeRef.value?.filter(treeFilter.value.trim()))
  }
}

function filterTreeNode(value: string, data: any) {
  if (!value) return true
  return String(data?.label || '')
    .toLowerCase()
    .includes(value.toLowerCase())
}

function onNodeClick(data: any, node: any) {
  if (data.kind === 'db') {
    selectSchema(data.name)
    // expand-on-click-node 是关掉的，展开/收起得自己 toggle，
    // 否则点已展开的库收不起来
    if (node?.expanded) node.collapse?.()
    else node?.expand?.()
    return
  }
  if (data.db && data.db !== schema.value) {
    schema.value = data.db
    loadTables()
    loadOverview()
  }
  currentTableType.value = data.type || 'table'
  selectTable(data.name)
}

// 表增删后重新拉该库的表
function reloadDbNode(name: string) {
  const node = dbTreeRef.value?.getNode?.('db:' + name)
  if (!node) return
  node.loaded = false
  node.childNodes = []
  node.expand?.()
}

watch(treeFilter, (v) => {
  const ref = dbTreeRef.value
  if (!ref) return
  const kw = String(v || '').trim()
  if (!kw) {
    ref.filter('')
    return
  }
  // 懒加载树：先展开所有库节点，表拉下来之后才能参与过滤
  const root = ref.root
  if (root?.childNodes) root.childNodes.forEach((n: any) => n.expand?.())
  nextTick(() => ref.filter(kw))
})

// ---------- 表详情 ----------

async function selectTable(name: string, toTab?: string) {
  currentTable.value = name
  detailTab.value = toTab || 'structure'
  tableDetailLoading.value = true
  tableDetail.value = null
  rowOrder.value = ''
  rowDesc.value = false
  rowPage.value = { columns: [], rows: [], page: 1, size: 50, has_more: false, cost_ms: 0, pk: [] }
  try {
    tableDetail.value = await api<any>('/api/mysql/table', {
      query: { schema: schema.value, table: name }
    })
    if (detailTab.value === 'data') await loadRows(true)
  } catch (e) {
    currentTable.value = ''
    ElMessage.error(errMsg(e))
  } finally {
    tableDetailLoading.value = false
  }
}

async function loadRows(reset = false) {
  if (!currentTable.value) return
  if (reset) rowPage.value.page = 1
  rowLoading.value = true
  try {
    const res = await api<any>('/api/mysql/rows', {
      query: {
        schema: schema.value,
        table: currentTable.value,
        page: rowPage.value.page,
        size: rowPage.value.size,
        order: rowOrder.value,
        desc: rowDesc.value ? 1 : 0
      }
    })
    rowPage.value = { ...res, page: res?.page || 1, size: res?.size || 50 }
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    rowLoading.value = false
  }
}

function pageRows(delta: number) {
  const next = rowPage.value.page + delta
  if (next < 1) return
  rowPage.value.page = next
  loadRows()
}

// 表列表点「导出」：切到导入导出页签，并让子组件定位到这张表
function gotoExport(name: string) {
  tab.value = 'transfer'
  transferTable.value = ''
  nextTick(() => {
    transferTable.value = name
  })
}

// 建表/改表执行后刷新库表、树节点与概览
async function onDesignerChanged() {
  await loadDatabases()
  await loadTables()
  await loadOverview()
  reloadDbNode(schema.value)
  // 改表后右侧详情可能已过期
  if (currentTable.value) await selectTable(currentTable.value, detailTab.value)
}

async function copyDDL() {
  const ok = await copyText(tableDetail.value?.ddl || '')
  if (ok) ElMessage.success('已复制建表语句')
  else ElMessage.error('复制失败，请手动选择文本复制')
}

// ---------- 字段修改 ----------

// 把 COLUMN_TYPE 拆成「基础类型 + 参数 + unsigned」，与后端 splitColumnType 保持一致
function parseType(t: string) {
  const s = String(t || '').trim()
  const unsigned = /unsigned\s*$/i.test(s)
  const base = s.replace(/\s*unsigned\s*$/i, '')
  const i = base.indexOf('(')
  if (i >= 0 && base.endsWith(')')) {
    return { type: base.slice(0, i).trim(), length: base.slice(i + 1, -1).trim(), unsigned }
  }
  return { type: base.trim(), length: '', unsigned }
}

const colBaseType = computed(() => String(colForm.type || '').toLowerCase())
const colIsNumeric = computed(() =>
  [
    'tinyint', 'smallint', 'mediumint', 'int', 'integer', 'bigint', 'decimal', 'numeric',
    'float', 'double', 'real', 'bit', 'year'
  ].includes(colBaseType.value)
)
const colIsInteger = computed(() =>
  ['tinyint', 'smallint', 'mediumint', 'int', 'integer', 'bigint'].includes(colBaseType.value)
)
const colIsTime = computed(() => ['timestamp', 'datetime'].includes(colBaseType.value))
const colNoArg = computed(() =>
  [
    'date', 'datetime', 'timestamp', 'time', 'json', 'bool', 'boolean', 'tinytext', 'text',
    'mediumtext', 'longtext', 'tinyblob', 'blob', 'mediumblob', 'longblob'
  ].includes(colBaseType.value)
)
const colArgPlaceholder = computed(() => {
  const t = colBaseType.value
  if (colNoArg.value) return '无需长度'
  if (t === 'enum' || t === 'set') return "'a','b'"
  if (t === 'decimal' || t === 'numeric') return '10,2'
  if (t === 'char' || t === 'varchar' || t === 'binary' || t === 'varbinary') return '255'
  return '默认长度'
})

function canAlterColumn(row: any) {
  if (currentTableType.value !== 'table') return false
  if (/generated/i.test(row.extra || '')) return false
  if (/zerofill/i.test(row.type || '')) return false
  return true
}

function columnEditHint(row: any) {
  if (currentTableType.value !== 'table') return '视图不能在此修改字段定义'
  if (/generated/i.test(row.extra || '')) return '生成列由表达式计算，不能手工修改'
  if (/zerofill/i.test(row.type || '')) return '该字段带 ZEROFILL，请在「设计」向导里修改'
  return ''
}

function openColumnEdit(row: any) {
  const p = parseType(row.type)
  colEditOrigin.value = row
  Object.assign(colForm, {
    name: row.name,
    type: p.type,
    length: p.length,
    unsigned: p.unsigned,
    nullable: !!row.nullable,
    auto_increment: /auto_increment/i.test(row.extra || ''),
    on_update: /on update/i.test(row.extra || ''),
    // 后端用 default_null 区分「没有默认值」与「默认空串」，这两者不能混
    default_text: row.default_null ? '' : row.default === '' ? "''" : row.default,
    comment: row.comment || ''
  })
  colPreviewDDL.value = ''
  colPreviewError.value = ''
  colEditVisible.value = true
  previewColumn()
}

function columnPayload() {
  const d = String(colForm.default_text || '').trim()
  return {
    schema: schema.value,
    table: currentTable.value,
    column: colEditOrigin.value?.name || '',
    spec: {
      name: colForm.name,
      type: colForm.type,
      length: colForm.length,
      unsigned: colForm.unsigned,
      nullable: colForm.nullable,
      auto_increment: colForm.auto_increment,
      default: d === '' ? null : d,
      on_update: colForm.on_update ? 'CURRENT_TIMESTAMP' : '',
      comment: colForm.comment
    }
  }
}

// 预览与执行走同一个接口，dry_run 时只生成不执行
async function previewColumn() {
  if (!colEditVisible.value || !currentTable.value) return
  const seq = ++colPreviewSeq
  try {
    const res = await api<any>('/api/mysql/column/alter', {
      method: 'POST',
      query: { dry_run: 1 },
      body: columnPayload()
    })
    if (seq !== colPreviewSeq) return
    colPreviewDDL.value = res?.ddl || ''
    colPreviewError.value = ''
  } catch (e) {
    if (seq !== colPreviewSeq) return
    colPreviewDDL.value = ''
    colPreviewError.value = errMsg(e)
  }
}

// 表单任一字段变化都重算 DDL：让用户在点「执行」之前就看清后果
watch(
  () => [
    colForm.name, colForm.type, colForm.length, colForm.unsigned, colForm.nullable,
    colForm.auto_increment, colForm.on_update, colForm.default_text, colForm.comment
  ],
  () => {
    if (!colEditVisible.value) return
    if (colPreviewTimer) clearTimeout(colPreviewTimer)
    colPreviewTimer = setTimeout(previewColumn, 350)
  }
)

async function saveColumnEdit() {
  if (!colPreviewDDL.value) return
  colEditSaving.value = true
  try {
    await api('/api/mysql/column/alter', { method: 'POST', body: columnPayload() })
    ElMessage.success('字段已修改')
    colEditVisible.value = false
    const t = currentTable.value
    if (t) await selectTable(t, 'structure')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    colEditSaving.value = false
  }
}

// ---------- 行数据修改 ----------

function openRowEdit(index: number) {
  const cols = tableDetail.value?.columns || []
  const data = rowPage.value.rows[index] || []
  rowEditIndex.value = index
  rowEditFields.value = (rowPage.value.columns || []).map((name: string, ci: number) => {
    const meta = cols.find((c: any) => c.name === name) || {}
    const raw = data[ci]
    return {
      name,
      type: meta.type || '',
      generated: /generated/i.test(meta.extra || ''),
      pk: (rowPage.value.pk || []).includes(name),
      isNull: raw === null,
      text: raw === null ? '' : String(raw),
      raw,
      multiline: /text|blob|json/i.test(meta.type || '')
    }
  })
  rowEditVisible.value = true
}

async function saveRowEdit() {
  if (!currentTable.value) return
  // 主键用「修改前的值」定位行：即使同时改了主键列，WHERE 也必须用旧值
  const key: Record<string, string> = {}
  for (const f of rowEditFields.value) {
    if (!f.pk) continue
    if (f.raw === null || f.raw === undefined) {
      ElMessage.warning(`主键「${f.name}」的值为空，无法定位这一行`)
      return
    }
    key[f.name] = String(f.raw)
  }
  const values: Record<string, string | null> = {}
  for (const f of rowEditFields.value) {
    if (f.generated) continue
    const wasNull = f.raw === null
    const nowNull = !!f.isNull
    if (wasNull && nowNull) continue
    if (!wasNull && !nowNull && String(f.raw) === f.text) continue
    values[f.name] = nowNull ? null : f.text
  }
  if (!Object.keys(values).length) {
    ElMessage.warning('没有修改任何字段')
    return
  }
  rowEditSaving.value = true
  try {
    const res = await api<any>('/api/mysql/row/update', {
      method: 'POST',
      body: { schema: schema.value, table: currentTable.value, key, values }
    })
    ElMessage.success(res?.affected ? '已更新 1 行' : '已提交（新值与旧值相同）')
    rowEditVisible.value = false
    await loadRows()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    rowEditSaving.value = false
  }
}

// 切到「数据」页签时才真正取数据，避免打开结构就扫一张大表。
// 注意要等表结构加载完再判断，selectTable 里已经取过的情况不重复请求。
watch(detailTab, (v) => {
  if (v === 'data' && currentTable.value && tableDetail.value && !rowPage.value.rows.length) {
    loadRows(true)
  }
})

// ---------- 会话 ----------

async function loadSessions() {
  sessionLoading.value = true
  try {
    const res = await api<any>('/api/mysql/processes')
    sessions.value = res?.items || []
  } catch (e) {
    sessions.value = []
    ElMessage.error(errMsg(e))
  } finally {
    sessionLoading.value = false
  }
}

async function killSession(row: any) {
  try {
    await ElMessageBox.confirm(
      `确定断开会话 #${row.id}（${row.user}@${row.host}）吗？该连接上未提交的事务会回滚。`,
      '断开会话',
      { type: 'warning', confirmButtonText: '断开', confirmButtonClass: 'el-button--danger' }
    )
  } catch {
    return
  }
  try {
    await api('/api/mysql/processes/kill', { method: 'POST', body: { id: row.id } })
    ElMessage.success('已断开')
    await loadSessions()
  } catch (e) {
    ElMessage.error(errMsg(e))
  }
}

// ---------- SQL 控制台 ----------

const readonlyPrefixes = ['select', 'show', 'desc', 'describe', 'explain', 'with', 'table', 'values', 'help']

function isReadOnlySQL(sql: string) {
  const s = sql.trim().replace(/^(\/\*[\s\S]*?\*\/|--[^\n]*\n|#[^\n]*\n)+/, '').trim()
  const k = (s.split(/[\s(]+/)[0] || '').toLowerCase().replace(/;$/, '')
  return readonlyPrefixes.includes(k)
}

async function runSQL() {
  const sql = sqlInput.value.trim()
  if (!sql) return
  if (conn.value.read_only && !isReadOnlySQL(sql)) {
    ElMessage.warning('当前连接是只读模式，写语句会被拒绝；如需写入请在「连接设置」中关闭只读')
    return
  }
  // 写语句改数据前再确认一次，SQL 控制台误触的代价太大
  if (!isReadOnlySQL(sql)) {
    try {
      await ElMessageBox.confirm('这是一条写语句，将直接修改数据库。确定执行吗？', '执行写语句', {
        type: 'warning',
        confirmButtonText: '执行',
        confirmButtonClass: 'el-button--danger'
      })
    } catch {
      return
    }
  }
  sqlLoading.value = true
  try {
    const res = await api<any>('/api/mysql/query', {
      method: 'POST',
      query: { db: schema.value },
      body: { sql, limit: 500 }
    })
    history.value.unshift({ ...res, statement: res?.statement || sql })
    if (res?.ok) {
      sqlInput.value = ''
      // 写语句可能改变表统计，顺手刷新一下概览
      if (res.kind === 'exec') {
        await loadOverview()
        await loadTables()
      }
    }
  } catch (e) {
    history.value.unshift({ ok: false, statement: sql, error: errMsg(e) })
  } finally {
    sqlLoading.value = false
  }
}

// ---------- 服务信息 ----------

async function loadVariables() {
  infoLoading.value = true
  try {
    const res = await api<any>('/api/mysql/variables', { query: { db: schema.value } })
    infoSections.value = res?.sections || {}
  } catch (e) {
    infoSections.value = {}
    ElMessage.error(errMsg(e))
  } finally {
    infoLoading.value = false
  }
}

watch(tab, (v) => {
  if (v === 'sessions' && !sessions.value.length) loadSessions()
  if (v === 'info' && !Object.keys(infoSections.value).length) loadVariables()
  if (v === 'metrics') loadOverview()
})

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
  grid-template-columns: 280px 12px minmax(0, 1fr);
  gap: 0;
}
.side-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 12px 14px 8px;
  border-bottom: 1px solid var(--line);
}
/* 左侧库表树 */
.tree-row {
  display: flex;
  align-items: center;
  gap: 5px;
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
.view-tag {
  flex: none;
  transform: scale(0.9);
}
.detail-tabs {
  padding: 4px 14px 10px;
}
:deep(.el-tree-node__content) {
  height: 30px;
}
:deep(.el-tree-node.is-current > .el-tree-node__content) {
  background: #eef3ff;
}
.warn-text {
  color: #b25a00;
}
.cell {
  font-size: 12px;
  word-break: break-all;
}
.cell-null {
  color: var(--ink-3);
  font-style: italic;
}
/* 字段修改弹窗：DDL 预览 */
.alter-preview {
  width: 100%;
  margin: 0;
  padding: 10px 12px;
  background: #f8f9fc;
  border: 1px solid var(--line);
  border-radius: 8px;
  font-size: 12px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 160px;
  overflow: auto;
}
.alter-error {
  width: 100%;
  padding: 8px 12px;
  background: #fff8f7;
  border: 1px solid #f3d7d4;
  border-radius: 8px;
  color: #c2382f;
  font-size: 12.5px;
  line-height: 1.6;
}
/* 行编辑：逐字段一行，长文本用 textarea */
.row-edit-list {
  max-height: calc(100vh - 300px);
  overflow: auto;
  border: 1px solid var(--line);
  border-radius: 8px;
}
.row-edit-item {
  display: grid;
  grid-template-columns: 210px minmax(0, 1fr) 68px;
  gap: 10px;
  align-items: center;
  padding: 7px 12px;
  border-bottom: 1px solid var(--line);
}
.row-edit-item:last-child {
  border-bottom: none;
}
.row-edit-item:nth-child(even) {
  background: #fbfcfe;
}
.row-edit-name {
  display: flex;
  align-items: center;
  gap: 5px;
  min-width: 0;
  overflow: hidden;
  font-size: 12.5px;
}
.row-edit-name .mono {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.row-edit-null {
  display: flex;
  justify-content: flex-end;
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
  max-height: calc(100vh - 380px);
  overflow: auto;
  margin: 0;
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
  flex: none;
}
.cmd-note {
  padding: 6px 12px;
  font-size: 12px;
  color: var(--ink-3);
  border-bottom: 1px solid var(--line);
}
.cmd-err {
  padding: 10px 12px;
  font-size: 12.5px;
  color: #c2382f;
  background: #fff8f7;
  white-space: pre-wrap;
  word-break: break-all;
}
</style>
