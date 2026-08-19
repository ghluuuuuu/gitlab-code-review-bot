<script setup lang="ts">
import { computed, onMounted, onUnmounted } from 'vue'
import { RouterLink, RouterView, useRoute } from 'vue-router'
import { useQueryClient } from '@tanstack/vue-query'
import { subscribeAdminEvents } from './eventStream'

const route = useRoute()
const queryClient = useQueryClient()
let stopEvents = () => { }
onMounted(() => {
  stopEvents = subscribeAdminEvents(event => {
    const common = new Set(['admin-dashboard', 'admin-reviews', 'admin-active-reviews', 'system-status'])
    if (event.event_type === 'finding_updated' || event.event_type === 'progress_updated' || event.event_type === 'job_finished') {
      common.add('quality-projects')
      common.add('quality-branches')
    }
    if (event.event_type === 'usage_updated' || event.event_type === 'job_finished') {
      common.add('usage-summary')
      common.add('usage-trend')
    }
    void queryClient.invalidateQueries({ predicate: query => common.has(String(query.queryKey[0])) })
  })
})
onUnmounted(() => stopEvents())
const active = computed(() => {
  if (route.name === 'quality') return 'quality'
  if (route.name === 'usage') return 'usage'
  if (route.name === 'system') return 'system'
  if (route.name === 'reviews' || route.name === 'review-detail') return 'reviews'
  return 'dashboard'
})
</script>

<template>
  <el-container class="layout">
    <el-aside width="238px" class="aside">
      <div class="brand">
        <div class="brand-mark">O</div>
        <div><strong>OCR Bot</strong><small>代码智能审查控制台</small></div>
      </div>
      <el-menu :default-active="active" class="menu">
        <el-menu-item index="dashboard">
          <RouterLink to="/dashboard"><span class="menu-icon">◈</span>运行总览</RouterLink>
        </el-menu-item>
        <el-menu-item index="reviews">
          <RouterLink to="/reviews"><span class="menu-icon">▦</span>审查任务</RouterLink>
        </el-menu-item>
        <el-menu-item index="quality">
          <RouterLink to="/quality"><span class="menu-icon">⌁</span>质量分析</RouterLink>
        </el-menu-item>
        <el-menu-item index="usage">
          <RouterLink to="/usage"><span class="menu-icon">◒</span>Token 用量</RouterLink>
        </el-menu-item>
        <el-menu-item index="system">
          <RouterLink to="/system"><span class="menu-icon">⚙</span>系统状态</RouterLink>
        </el-menu-item>
      </el-menu>
      <div class="aside-footer"><span class="service-state"><span class="online-dot" />服务运行中</span><span
          class="sse-state"><i />SSE 实时</span></div>
    </el-aside>
    <el-main class="main">
      <RouterView />
    </el-main>
  </el-container>
</template>

<style scoped>
:global(*) {
  box-sizing: border-box;
}

:global(body) {
  margin: 0;
  font-family: Inter, "PingFang SC", "Microsoft YaHei", sans-serif;
  color: #20253a;
  background: #f6f7fb;
}

:global(a) {
  color: inherit;
  text-decoration: none;
}

:global(a:hover),
:global(a:focus),
:global(a:active),
:global(a:visited) {
  text-decoration: none;
}

.layout {
  height: 100vh;
  min-height: 100vh;
  overflow: hidden;
  background: #f6f7fb;
}

.aside {
  position: relative;
  flex: 0 0 238px;
  height: 100vh;
  overflow: hidden;
  background: #fff;
  border-right: 1px solid #e9ebf2;
}

.brand {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 24px 22px 28px;
  font-size: 17px;
  letter-spacing: .2px;
}

.brand small {
  display: block;
  margin-top: 3px;
  color: #a0a5b8;
  font-size: 11px;
  font-weight: 400;
}

.brand-mark {
  width: 34px;
  height: 34px;
  border-radius: 10px;
  display: grid;
  place-items: center;
  color: #fff;
  font-size: 21px;
  font-weight: 800;
  background: linear-gradient(135deg, #7267e8, #4e8de8);
  box-shadow: 0 7px 15px #7167e83d;
}

.menu {
  border-right: 0;
  padding: 0 12px;
}

.menu :deep(.el-menu-item) {
  height: 46px;
  margin: 4px 0;
  padding: 0 !important;
  border-radius: 9px;
  color: #737991;
}

.menu :deep(.el-menu-item.is-active) {
  color: #6258d8;
  background: #f0efff;
  font-weight: 600;
}

.menu :deep(.el-menu-item a) {
  display: flex;
  align-items: center;
  width: 100%;
  height: 100%;
  padding: 0 20px;
  color: inherit;
  text-decoration: none;
}

.menu-icon {
  width: 25px;
  margin-right: 9px;
  font-size: 21px;
  text-align: center;
}

.aside-footer {
  position: absolute;
  bottom: 28px;
  left: 24px;
  color: #a0a5b8;
  font-size: 12px;
}

.online-dot {
  width: 7px;
  height: 7px;
  display: inline-block;
  margin-right: 7px;
  border-radius: 50%;
  background: #32c48d;
  box-shadow: 0 0 0 4px #32c48d20;
}

.main {
  width: 100%;
  height: 100vh;
  min-width: 0;
  padding: 34px 42px 48px;
  margin: 0 auto;
  overflow-x: hidden;
  overflow-y: auto;
}

.aside-footer {
  right: 18px;
  left: 18px;
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.service-state,
.sse-state {
  display: inline-flex;
  align-items: center;
  white-space: nowrap;
}

.sse-state {
  gap: 5px;
  padding: 4px 7px;
  border-radius: 7px;
  color: #2f9c72;
  background: #eaf8f2;
  font-size: 9px;
  font-weight: 600;
}

.sse-state i {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: #31b87f;
  box-shadow: 0 0 0 3px #31b87f20;
}

@media (max-width: 1050px) {
  .main {
    padding: 24px;
  }
}

@media (max-width: 760px) {
  .aside {
    width: 64px !important;
    flex-basis: 64px;
  }

  .brand {
    padding: 18px 15px;
  }

  .brand>div:not(.brand-mark),
  .menu :deep(.el-menu-item:not(.is-active)) {
    font-size: 0;
  }

  .brand small,
  .aside-footer {
    display: none;
  }

  .menu {
    padding: 0 8px;
  }

  .menu :deep(.el-menu-item a) {
    padding: 0 18px;
  }

  .menu-icon {
    margin: 0;
  }

  .main {
    padding: 20px 14px;
  }
}
</style>
