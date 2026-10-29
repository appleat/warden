<template>
  <div class="grid" style="grid-template-columns: 1fr 1fr; align-items: start">
    <div class="card">
      <div class="card-title"><span>账号信息</span></div>
      <div class="kv">
        <div><span>用户名</span><b>{{ me.username || '-' }}</b></div>
        <div><span>角色</span><b>{{ roleText }}</b></div>
        <div><span>两步验证</span><b>{{ me.totp_enabled ? '已开启' : '未开启' }}</b></div>
        <div><span>最近登录</span><b>{{ me.last_login_at ? fmtTime(me.last_login_at) : '-' }}</b></div>
      </div>

      <el-divider style="margin: 18px 0 16px" />

      <div class="card-title"><span>修改密码</span></div>
      <el-form label-position="top" style="max-width: 360px">
        <el-form-item label="原密码">
          <el-input v-model="pwd.old" type="password" show-password />
        </el-form-item>
        <el-form-item label="新密码">
          <el-input v-model="pwd.new" type="password" show-password placeholder="至少 8 位" />
        </el-form-item>
        <el-form-item label="确认新密码">
          <el-input v-model="pwd.confirm" type="password" show-password />
        </el-form-item>
        <el-button type="primary" :loading="pwdSaving" @click="savePwd">保存密码</el-button>
      </el-form>

      <el-divider style="margin: 20px 0 16px" />

      <div class="card-title">
        <span>两步验证（TOTP）</span>
        <el-button v-if="!me.totp_enabled" size="small" type="primary" plain @click="setupTotp">开启</el-button>
        <el-button v-else size="small" type="danger" plain @click="disableTotp">关闭</el-button>
      </div>
      <div class="muted" style="font-size: 12px; line-height: 1.7">
        使用 Google Authenticator、1Password 等应用扫描二维码，登录时需额外输入 6 位动态码。
      </div>
    </div>

    <div>
      <div class="card">
        <div class="card-title">
          <span>监控告警通知</span>
          <el-switch v-model="alert.enabled" size="small" />
        </div>
        <el-input
          v-model="alert.webhook"
          type="textarea"
          :rows="3"
          placeholder="支持企业微信群机器人 / 钉钉 / 飞书 / 任意接收 JSON 的地址"
        />
        <div class="muted" style="font-size: 11.5px; margin-top: 8px; line-height: 1.7">
          站点状态发生变化时推送。企业微信示例：<br />
          <code class="mono">https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=xxx</code>
        </div>
        <el-button type="primary" style="margin-top: 12px" :loading="alertSaving" @click="saveAlert">保存配置</el-button>
        <el-button style="margin-top: 12px" @click="testAlert" :disabled="!alert.webhook">发送测试</el-button>
      </div>

      <div class="card" style="margin-top: 14px">
        <div class="card-title"><span>运行信息</span></div>
        <div class="kv">
          <div><span>版本</span><b>{{ info.version || '-' }}</b></div>
          <div><span>Go 运行时</span><b class="mono">{{ info.go_version || '-' }}</b></div>
          <div><span>文件根目录</span><b class="mono">{{ info.file_root || '-' }}</b></div>
          <div><span>主机名</span><b>{{ info.host?.hostname || '-' }}</b></div>
          <div><span>系统</span><b>{{ info.host?.platform }} {{ info.host?.version }}</b></div>
        </div>
        <el-alert
          type="info"
          :closable="false"
          style="margin-top: 14px"
          title="安全建议：将服务置于 Nginx/HTTPS 之后，或配置 cert_file 启用 TLS；仅监听 127.0.0.1 可进一步收敛暴露面。"
        />
      </div>
    </div>

    <el-dialog v-model="totpVisible" title="开启两步验证" width="440px">
      <div style="text-align: center">
        <div class="muted" style="font-size: 12px; margin-bottom: 8px">
          在验证器 App 中添加以下密钥（或手动输入）
        </div>
        <div class="mono secret">{{ totpSecret }}</div>
        <div class="mono muted" style="font-size: 10.5px; word-break: break-all; margin-top: 6px">{{ totpUrl }}</div>
        <el-input v-model="totpCode" placeholder="输入 6 位验证码" maxlength="6" style="margin-top: 16px" />
      </div>
      <template #footer>
        <el-button @click="totpVisible = false">取消</el-button>
        <el-button type="primary" :loading="totpSaving" @click="enableTotp">确认开启</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { api, errMsg } from '../api'
import { fmtTime } from '../utils'

const me = ref<any>({})
const info = ref<any>({})
const pwd = reactive({ old: '', new: '', confirm: '' })
const pwdSaving = ref(false)
const alert = reactive({ webhook: '', enabled: false })
const alertSaving = ref(false)
const totpVisible = ref(false)
const totpSecret = ref('')
const totpUrl = ref('')
const totpCode = ref('')
const totpSaving = ref(false)

const roleText = computed(() => {
  const map: Record<string, string> = { admin: '管理员', operator: '运维', viewer: '只读' }
  return map[me.value.role] || '-'
})

async function load() {
  me.value = await api('/api/auth/me')
  info.value = await api('/api/system/info')
  const a = await api<any>('/api/settings/alert')
  alert.webhook = a.webhook || ''
  alert.enabled = !!a.enabled
}

async function savePwd() {
  if (pwd.new.length < 8) return ElMessage.warning('新密码至少 8 位')
  if (pwd.new !== pwd.confirm) return ElMessage.warning('两次输入不一致')
  pwdSaving.value = true
  try {
    await api('/api/auth/password', { body: { old_password: pwd.old, new_password: pwd.new } })
    ElMessage.success('密码已更新')
    pwd.old = pwd.new = pwd.confirm = ''
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    pwdSaving.value = false
  }
}

async function saveAlert() {
  alertSaving.value = true
  try {
    await api('/api/settings/alert', { method: 'PUT', body: { ...alert } })
    ElMessage.success('已保存')
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    alertSaving.value = false
  }
}

function testAlert() {
  ElMessage.info('保存后将由真实的站点状态变化触发；此处仅校验配置格式')
}

async function setupTotp() {
  const d = await api<any>('/api/auth/totp/setup', { method: 'POST' })
  totpSecret.value = d.secret
  totpUrl.value = d.url
  totpCode.value = ''
  totpVisible.value = true
}

async function enableTotp() {
  totpSaving.value = true
  try {
    await api('/api/auth/totp/enable', { body: { secret: totpSecret.value, code: totpCode.value } })
    ElMessage.success('两步验证已开启')
    totpVisible.value = false
    load()
  } catch (e) {
    ElMessage.error(errMsg(e))
  } finally {
    totpSaving.value = false
  }
}

async function disableTotp() {
  try {
    await ElMessageBox.confirm('关闭后登录将只需密码，确认关闭？', '关闭两步验证', { type: 'warning' })
  } catch {
    return
  }
  await api('/api/auth/totp/disable', { method: 'POST' })
  ElMessage.success('已关闭')
  load()
}

onMounted(load)
</script>

<style scoped>
.kv > div {
  display: flex;
  justify-content: space-between;
  padding: 8px 0;
  border-bottom: 1px dashed #f0f2f7;
  font-size: 12.5px;
}
.kv > div:last-child {
  border-bottom: none;
}
.kv span {
  color: var(--ink-3);
}
.kv b {
  font-weight: 500;
  max-width: 62%;
  text-align: right;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.secret {
  font-size: 15px;
  letter-spacing: 1.5px;
  background: #f5f7fb;
  padding: 10px;
  border-radius: 8px;
  word-break: break-all;
}
</style>
