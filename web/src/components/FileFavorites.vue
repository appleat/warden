<template>
  <div class="fav">
    <div class="fav-head">
      <span class="fav-title">收藏夹</span>
      <el-button link type="primary" size="small" :loading="busy" @click="emit('toggle-current')">
        {{ currentFaved ? '取消收藏当前目录' : '收藏当前目录' }}
      </el-button>
    </div>

    <div v-if="!items.length" class="fav-empty">
      还没有收藏。<br />在常用目录点「收藏当前目录」，之后一键跳转。
    </div>
    <div v-else class="fav-list">
      <div v-for="f in items" :key="f.id" class="fav-item" :title="f.path" @click="emit('open', f)">
        <el-icon class="fav-icon"><Folder /></el-icon>
        <div class="fav-text">
          <div class="fav-name">{{ f.name }}</div>
          <div class="fav-path">{{ f.path }}</div>
        </div>
        <el-button link :icon="Close" class="fav-del" title="取消收藏" @click.stop="emit('remove', f)" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { Close, Folder } from '@element-plus/icons-vue'

defineProps<{ items: any[]; currentFaved?: boolean; busy?: boolean }>()
const emit = defineEmits<{
  (e: 'toggle-current'): void
  (e: 'open', item: any): void
  (e: 'remove', item: any): void
}>()
</script>

<style scoped>
.fav {
  font-size: 12.5px;
}
.fav-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding-bottom: 6px;
  border-bottom: 1px solid var(--line);
  margin-bottom: 6px;
}
.fav-title {
  font-weight: 600;
  color: var(--ink);
}
.fav-empty {
  padding: 10px 2px 6px;
  color: var(--ink-3);
  font-size: 12px;
  line-height: 1.7;
}
.fav-list {
  max-height: 300px;
  overflow: hidden auto;
}
.fav-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px;
  border-radius: 6px;
  cursor: pointer;
  min-width: 0;
}
.fav-item:hover {
  background: var(--bg);
}
.fav-icon {
  flex: 0 0 auto;
  color: #e6a23c;
}
.fav-text {
  flex: 1;
  min-width: 0;
}
.fav-name {
  font-weight: 500;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fav-path {
  color: var(--ink-3);
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.fav-del {
  flex: 0 0 auto;
  opacity: 0.5;
}
.fav-item:hover .fav-del {
  opacity: 1;
}
</style>
