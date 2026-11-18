<template>
  <el-dialog
    v-model="visible"
    class="perm-dialog"
    width="480px"
    append-to-body
    :show-close="true"
    :close-on-click-modal="false"
  >
    <template #header>
      <div class="perm-head">
        <div class="perm-title">修改权限</div>
        <div class="perm-desc">输入三位八进制权限，例如 644 或 755。</div>
      </div>
    </template>

    <div class="perm-field">
      <label class="perm-label" for="perm-octal">八进制权限</label>
      <input
        id="perm-octal"
        ref="inputRef"
        v-model="text"
        class="perm-input"
        inputmode="numeric"
        autocomplete="off"
        spellcheck="false"
        maxlength="3"
        @input="onInput"
        @keyup.enter="submit"
      />
    </div>

    <!-- 权限矩阵：行=所有者/属组/其他，列=R/W/X，勾选与上方三位八进制实时联动 -->
    <div class="perm-grid">
      <div class="perm-cell head"></div>
      <div v-for="c in COLS" :key="'h-' + c.key" class="perm-cell head">{{ c.key.toUpperCase() }}</div>

      <template v-for="g in GROUPS" :key="g.key">
        <div class="perm-cell label">{{ g.name }}</div>
        <div v-for="c in COLS" :key="g.key + c.key" class="perm-cell">
          <button
            type="button"
            role="checkbox"
            class="perm-box"
            :class="{ on: bits[g.key][c.key] }"
            :aria-checked="bits[g.key][c.key]"
            :aria-label="g.name + ' ' + c.key.toUpperCase()"
            @click="toggle(g.key, c.key)"
          >
            <el-icon v-if="bits[g.key][c.key]"><Check /></el-icon>
          </button>
        </div>
      </template>
    </div>

    <template #footer>
      <div class="perm-foot">
        <button type="button" class="perm-btn ghost" @click="visible = false">取消</button>
        <button type="button" class="perm-btn solid" :disabled="!valid || busy" @click="submit">
          {{ busy ? '应用中…' : '应用' }}
        </button>
      </div>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { Check } from '@element-plus/icons-vue'

const props = withDefaults(
  defineProps<{ modelValue: boolean; mode?: number | string; busy?: boolean }>(),
  { mode: 0o644, busy: false }
)
const emit = defineEmits<{
  (e: 'update:modelValue', v: boolean): void
  (e: 'apply', v: string): void
}>()

const visible = computed({
  get: () => props.modelValue,
  set: (v: boolean) => emit('update:modelValue', v)
})

const COLS = [
  { key: 'r', bit: 4 },
  { key: 'w', bit: 2 },
  { key: 'x', bit: 1 }
] as const
const GROUPS = [
  { key: 'u', name: '所有者' },
  { key: 'g', name: '属组' },
  { key: 'o', name: '其他' }
] as const

type GroupKey = (typeof GROUPS)[number]['key']
type ColKey = (typeof COLS)[number]['key']

const text = ref('644')
const inputRef = ref<HTMLInputElement>()
const bits = reactive<Record<GroupKey, Record<ColKey, boolean>>>({
  u: { r: true, w: true, x: false },
  g: { r: true, w: false, x: false },
  o: { r: true, w: false, x: false }
})

const valid = computed(() => /^[0-7]{3}$/.test(text.value))

// 后端 mode_bit 是权限位整数；也兼容传进来的八进制字符串（含 4 位时只取后三位）
function toOctal(v: number | string): string {
  if (typeof v === 'number' && Number.isFinite(v)) return (v & 0o777).toString(8).padStart(3, '0')
  const s = String(v ?? '').trim()
  return /^[0-7]{3,4}$/.test(s) ? s.slice(-3) : '644'
}

function applyText() {
  const s = text.value
  if (!/^[0-7]{3}$/.test(s)) return
  GROUPS.forEach((g, gi) => {
    for (const c of COLS) bits[g.key][c.key] = (Number(s[gi]) & c.bit) !== 0
  })
}

function bitsToOctal() {
  return GROUPS.map((g) => COLS.reduce((n, c) => (bits[g.key][c.key] ? n | c.bit : n), 0)).join('')
}

function toggle(g: GroupKey, c: ColKey) {
  bits[g][c] = !bits[g][c]
  text.value = bitsToOctal()
}

// 只允许 0-7，且最多三位；输入满三位即同步勾选状态
function onInput() {
  const cleaned = text.value.replace(/[^0-7]/g, '').slice(0, 3)
  if (cleaned !== text.value) text.value = cleaned
  applyText()
}

function submit() {
  if (!valid.value || props.busy) return
  emit('apply', text.value)
}

watch(
  () => props.modelValue,
  async (open) => {
    if (!open) return
    text.value = toOctal(props.mode)
    applyText()
    await nextTick()
    inputRef.value?.focus()
    inputRef.value?.select()
  }
)
</script>

<style scoped>
.perm-head {
  padding-right: 24px;
}
.perm-title {
  font-size: 17px;
  font-weight: 600;
  color: var(--ink);
  line-height: 1.4;
}
.perm-desc {
  margin-top: 8px;
  font-size: 13px;
  color: var(--ink-3);
}

.perm-field {
  margin-top: 24px;
}
.perm-label {
  display: block;
  font-size: 13px;
  color: var(--ink);
  margin-bottom: 8px;
}
.perm-input {
  width: 100%;
  height: 42px;
  padding: 0 12px;
  font-size: 14px;
  font-family: inherit;
  color: var(--ink);
  background: #f6f7f9;
  border: 1px solid #dcdfe6;
  border-radius: 8px;
  outline: none;
  transition: border-color 0.15s, background 0.15s;
}
.perm-input:focus {
  background: #fff;
  border-color: #a9b0bb;
}

.perm-grid {
  margin-top: 20px;
  display: grid;
  grid-template-columns: 116px repeat(3, 52px);
  row-gap: 10px;
  align-items: center;
  justify-content: start;
}
.perm-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 24px;
}
.perm-cell.head {
  font-size: 12.5px;
  color: var(--ink-3);
}
.perm-cell.label {
  justify-content: flex-start;
  font-size: 13px;
  color: var(--ink);
}

.perm-box {
  width: 22px;
  height: 22px;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 13px;
  color: #fff;
  background: #f3f4f6;
  border: 1px solid #e8eaee;
  border-radius: 6px;
  cursor: pointer;
  transition: background 0.15s, border-color 0.15s;
}
.perm-box:hover {
  border-color: #c6ccd6;
}
.perm-box.on {
  background: #2b2f36;
  border-color: #2b2f36;
}
.perm-box.on:hover {
  background: #1b1f24;
  border-color: #1b1f24;
}

.perm-foot {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
.perm-btn {
  height: 38px;
  padding: 0 20px;
  font-size: 13px;
  font-family: inherit;
  border: none;
  border-radius: 8px;
  cursor: pointer;
  transition: background 0.15s, opacity 0.15s;
}
.perm-btn.ghost {
  background: #f2f3f5;
  color: #2b2f36;
}
.perm-btn.ghost:hover {
  background: #e8eaee;
}
.perm-btn.solid {
  background: #2b2f36;
  color: #fff;
}
.perm-btn.solid:hover:not(:disabled) {
  background: #1b1f24;
}
.perm-btn.solid:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
</style>

<!-- el-dialog 被 teleport 到 body，外壳节点拿不到 scoped 标记，这里用 .perm-dialog 前缀单独约束 -->
<style>
.perm-dialog {
  padding: 0;
  border-radius: 12px;
  overflow: hidden;
}
.perm-dialog .el-dialog__header {
  padding: 22px 24px 0;
  margin: 0;
}
.perm-dialog .el-dialog__headerbtn {
  top: 18px;
  right: 16px;
  font-size: 16px;
}
.perm-dialog .el-dialog__body {
  padding: 0 24px 22px;
}
.perm-dialog .el-dialog__footer {
  padding: 0 24px 20px;
}
</style>
