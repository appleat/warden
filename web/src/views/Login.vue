<template>
  <div class="login-page">
    <div class="login-side">
      <div class="row" style="gap: 10px">
        <img class="brand-mark" :src="logoMark" alt="Warden" />
        <span style="font-size: 15px; font-weight: 600; letter-spacing: 0.3px">Warden</span>
      </div>

      <div class="feature-list">
        <h2 style="font-size: 26px; line-height: 1.45; margin: 0 0 10px; font-weight: 600">
          Linux 服务器管理面板
        </h2>
        <p style="color: #8b98b3; font-size: 13px; line-height: 1.8; margin: 0 0 34px; max-width: 360px">
          容器、文件、防火墙、终端与站点监控，单文件部署，开箱即用。
        </p>

        <div class="feature">
          <div class="feature-ico"><el-icon><Box /></el-icon></div>
          <div>
            <h4>容器全生命周期</h4>
            <p>启停重启、实时日志、资源监控，直接进入容器内部 shell</p>
          </div>
        </div>
        <div class="feature">
          <div class="feature-ico"><el-icon><FolderOpened /></el-icon></div>
          <div>
            <h4>文件与终端</h4>
            <p>浏览器内上传下载、在线编辑，SSH 会话随时打开</p>
          </div>
        </div>
        <div class="feature">
          <div class="feature-ico"><el-icon><Lock /></el-icon></div>
          <div>
            <h4>防火墙不锁死自己</h4>
            <p>危险改动 30 秒未确认自动回滚，给你留一条退路</p>
          </div>
        </div>
      </div>

      <div style="position: relative; z-index: 1; font-size: 11px; color: #5d6a84">
        Warden · 自托管运维面板
      </div>
    </div>

    <div class="login-main">
      <div class="login-card">
        <template v-if="!initialized">
          <h1>初始化管理员</h1>
          <p class="sub">首次使用，请设置管理员账号，该账号拥有全部权限</p>
        </template>
        <template v-else>
          <h1>登录控制台</h1>
          <p class="sub">请输入账号密码继续</p>
        </template>

        <el-form :model="form" label-position="top" @submit.prevent="submit">
          <el-form-item label="用户名">
            <el-input v-model="form.username" size="large" placeholder="admin" autofocus />
          </el-form-item>
          <el-form-item label="密码">
            <el-input
              v-model="form.password"
              type="password"
              size="large"
              show-password
              placeholder="至少 8 位"
              @keyup.enter="submit"
            />
          </el-form-item>
          <el-form-item v-if="!initialized" label="确认密码">
            <el-input v-model="form.confirm" type="password" size="large" show-password />
          </el-form-item>
          <el-form-item v-if="needTOTP" label="两步验证码">
            <el-input v-model="form.totp" size="large" maxlength="6" placeholder="6 位数字" />
          </el-form-item>

          <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon style="margin-bottom: 12px" />

          <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="submit">
            {{ initialized ? '登 录' : '创建并进入' }}
          </el-button>
        </el-form>

        <div style="margin-top: 16px; font-size: 11.5px; color: var(--ink-3); line-height: 1.7">
          <template v-if="initialized">
            忘记密码？在服务器上停止服务，删除
            <code class="mono">data/warden.db</code> 可重置全部数据。
          </template>
          <template v-else> 密码将使用 bcrypt 加密存储，请妥善保管 </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { Box, FolderOpened, Lock } from '@element-plus/icons-vue'
import { api, errMsg, setToken } from '../api'
import logoMark from '../assets/logo/warden-mark.svg'

const router = useRouter()
const initialized = ref(true)
const loading = ref(false)
const error = ref('')
const needTOTP = ref(false)

const form = reactive({ username: '', password: '', confirm: '', totp: '' })

onMounted(async () => {
  try {
    const st = await api<any>('/api/auth/status')
    initialized.value = !!st.initialized
    if (st.authed) router.push('/dashboard')
  } catch (e) {
    error.value = errMsg(e)
  }
})

async function submit() {
  error.value = ''
  if (!form.username) return (error.value = '请输入用户名')
  if (!form.password) return (error.value = '请输入密码')

  if (!initialized.value) {
    if (form.password.length < 8) return (error.value = '密码至少 8 位')
    if (form.password !== form.confirm) return (error.value = '两次输入的密码不一致')
  }

  loading.value = true
  try {
    const path = initialized.value ? '/api/auth/login' : '/api/auth/bootstrap'
    const body: any = { username: form.username, password: form.password }
    if (form.totp) body.totp = form.totp
    const res = await api<any>(path, { body })
    setToken(res.token)
    ElMessage.success(initialized.value ? '登录成功' : '初始化完成')
    router.push('/dashboard')
  } catch (e: any) {
    const msg = errMsg(e)
    error.value = msg
    if (msg.includes('两步验证')) needTOTP.value = true
  } finally {
    loading.value = false
  }
}
</script>
