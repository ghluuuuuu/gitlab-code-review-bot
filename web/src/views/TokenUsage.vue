<script setup lang="ts">
import { computed, ref } from 'vue'
import { useQuery } from '@tanstack/vue-query'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart, BarChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, LegendComponent } from 'echarts/components'
import { getJSON, formatTokens, type UsageSummary, type UsageTrendPoint } from '../adminApi'

use([CanvasRenderer, LineChart, BarChart, GridComponent, TooltipComponent, LegendComponent])
const range = ref('30d')
const rangeQuery = computed(() => range.value === '7d' ? '7' : range.value === '90d' ? '90' : '30')
const from = computed(() => { const date = new Date(); date.setDate(date.getDate() - Number(rangeQuery.value)); return date.toISOString() })
const querySuffix = computed(() => `from=${encodeURIComponent(from.value)}&to=${encodeURIComponent(new Date().toISOString())}`)
const summary = useQuery({ queryKey: ['usage-summary', querySuffix], queryFn: () => getJSON<UsageSummary>(`/api/v1/admin/usage/summary?${querySuffix.value}`) })
const trend = useQuery({ queryKey: ['usage-trend', querySuffix], queryFn: () => getJSON<UsageTrendPoint[]>(`/api/v1/admin/usage/trend?${querySuffix.value}`) })
const chart = computed(() => ({ tooltip: { trigger: 'axis', formatter: (params: Array<{ seriesName: string; value: number }>) => params.map(item => `${item.seriesName}: ${formatTokens(item.value)}`).join('<br/>') }, legend: { data: ['输入 Token', '输出 Token'] }, grid: { left: 55, right: 25, top: 42, bottom: 40 }, xAxis: { type: 'category', data: (trend.data.value ?? []).map(item => item.date) }, yAxis: { type: 'value', axisLabel: { formatter: (value: number) => formatTokens(value) } }, series: [{ name: '输入 Token', type: 'line', smooth: true, data: (trend.data.value ?? []).map(item => item.input_tokens), itemStyle: { color: '#4a89dc' } }, { name: '输出 Token', type: 'line', smooth: true, data: (trend.data.value ?? []).map(item => item.output_tokens), itemStyle: { color: '#7469df' } }] }))
const resultChart = computed(() => ({ tooltip: { trigger: 'item' }, series: [{ type: 'pie', radius: ['45%', '72%'], label: { formatter: '{b}: {c}' }, data: [{ name: '失败', value: summary.data.value?.failed_reviews ?? 0, itemStyle: { color: '#e76b75' } }, { name: '重试', value: summary.data.value?.retried_reviews ?? 0, itemStyle: { color: '#e7a14b' } }, { name: 'stale', value: summary.data.value?.stale_reviews ?? 0, itemStyle: { color: '#9aa3b8' } }] }] }))
</script>

<template>
  <div class="page-heading">
    <div>
      <h1>Token 用量</h1>
      <p>按时间范围查看实际 LLM 使用量、审查次数和重试成本</p>
    </div><el-radio-group v-model="range" size="small"><el-radio-button label="7d">7 天</el-radio-button><el-radio-button
        label="30d">30 天</el-radio-button><el-radio-button label="90d">90 天</el-radio-button></el-radio-group>
  </div>
  <div class="summary-grid">
    <div class="summary-card"><span>总 Token</span><strong class="purple">{{
      formatTokens(summary.data.value?.total_tokens ?? 0) }}</strong><small>{{ summary.data.value?.review_count ?? 0
        }} 次审查</small></div>
    <div class="summary-card"><span>输入 Token</span><strong class="blue">{{ formatTokens(summary.data.value?.input_tokens
      ?? 0) }}</strong><small>模型上下文消耗</small></div>
    <div class="summary-card"><span>输出 Token</span><strong class="teal">{{
      formatTokens(summary.data.value?.output_tokens ?? 0) }}</strong><small>模型生成消耗</small></div>
    <div class="summary-card"><span>Tool Calls</span><strong>{{ formatTokens(summary.data.value?.tool_calls ?? 0)
        }}</strong><small>工具调用总数</small></div>
  </div>
  <div class="chart-grid"><el-card shadow="never" class="chart-card"><template #header><strong>Token
          趋势</strong></template><v-chart class="large-chart" :option="chart" autoresize /></el-card><el-card
      shadow="never" class="chart-card"><template #header><strong>异常与重试</strong></template><v-chart class="small-chart"
        :option="resultChart" autoresize /></el-card>
  </div>
  <el-card shadow="never" class="detail-card"><template #header><strong>成本构成</strong></template>
    <div class="cost-grid">
      <div><span>失败任务</span><b class="red">{{ summary.data.value?.failed_reviews ?? 0 }}</b></div>
      <div><span>发生重试</span><b class="orange">{{ summary.data.value?.retried_reviews ?? 0 }}</b></div>
      <div><span>stale 任务</span><b class="gray">{{ summary.data.value?.stale_reviews ?? 0 }}</b></div>
      <div><span>评论数量</span><b>{{ summary.data.value?.comments ?? 0 }}</b></div>
    </div>
  </el-card>
</template>

<style scoped>
.page-heading {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 22px;
}

.page-heading h1 {
  margin: 0 0 7px;
  font-size: 25px;
}

.page-heading p {
  margin: 0;
  color: #9499ad;
  font-size: 13px;
}

.summary-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
  margin-bottom: 18px;
}

.summary-card {
  min-height: 112px;
  padding: 18px 20px;
  border: 1px solid #ebedf4;
  border-radius: 12px;
  background: #fff;
}

.summary-card span,
.summary-card small {
  display: block;
  color: #8c91a5;
  font-size: 12px;
}

.summary-card strong {
  display: block;
  margin: 8px 0 3px;
  color: #30364e;
  font-size: 25px;
}

.summary-card small {
  color: #b1b5c4;
  font-size: 11px;
}

.purple {
  color: #7469df !important;
}

.blue {
  color: #3984e8 !important;
}

.teal {
  color: #2ca89b !important;
}

.chart-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.6fr) minmax(280px, 1fr);
  gap: 18px;
  margin-bottom: 18px;
}

.chart-card,
.detail-card {
  border: 1px solid #ebedf4;
  border-radius: 12px;
}

.large-chart {
  height: 360px;
}

.small-chart {
  height: 360px;
}

.cost-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 14px;
}

.cost-grid div {
  padding: 15px;
  border-radius: 8px;
  background: #f7f8fc;
}

.cost-grid span,
.cost-grid b {
  display: block;
}

.cost-grid span {
  color: #9298aa;
  font-size: 11px;
}

.cost-grid b {
  margin-top: 6px;
  font-size: 20px;
}

.red {
  color: #e56b75;
}

.orange {
  color: #e29a44;
}

.gray {
  color: #8c95a8;
}

@media (max-width:850px) {
  .summary-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .chart-grid {
    grid-template-columns: 1fr;
  }

  .page-heading {
    gap: 10px;
  }
}

@media (max-width:600px) {

  .summary-grid,
  .cost-grid {
    grid-template-columns: 1fr 1fr;
  }

  .page-heading {
    flex-direction: column;
  }
}
</style>
