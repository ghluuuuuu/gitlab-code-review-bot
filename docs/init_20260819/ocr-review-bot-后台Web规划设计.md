# OCR Review Bot 后台 Web 规划设计

> 文档状态：规划稿
>
> 适用版本：当前 `ocr-review-bot` 单体服务
>
> 设计目标：基于现有代码审查、增量审查、Code Graph、GitLab 评论发布、OCR Viewer 和 Token 统计能力，重构管理后台的信息架构、接口、数据模型和运维流程，使后台成为真正的“审查任务控制台”，而不是只展示项目列表的统计页面。

---

## 1. 设计结论

后台 Web 的核心对象应从“项目”调整为“审查任务（Review Job）”。项目和 Merge Request 是审查任务的业务归属；任务状态、执行阶段、审查覆盖率、缺陷生命周期和发布结果才是后台需要管理的主要信息。

目标信息链路如下：

```text
GitLab MR 发现
    ↓
审查任务入队
    ↓
规则预检
    ↓
Git 工作区准备
    ↓
Code Graph 构建 / 增量更新
    ↓
OCR 文件审查
    ↓
缺陷汇总与增量对比
    ↓
GitLab Discussion / Note 发布
    ↓
通过、未通过、失败或 stale
```

后台需要完整呈现这条链路，并支持 Operator 在不破坏任务版本一致性的前提下执行重试、取消、调整优先级和重新发现。

### 1.1 首期优先级

第一期只解决最关键的四件事：

1. 以服务端分页和筛选为基础的审查任务中心；
2. 单个任务的详情、执行阶段和覆盖率；
3. 同一 MR 的 Head/Target 版本链和增量复用信息；
4. 失败、重试、stale、规则缺失和覆盖不完整的可解释展示。

不在第一期引入微服务、Redis、消息队列、独立后端服务或复杂实时推送。当前 Vue Query 的 5～10 秒轮询已经足够支撑第一版后台。

---

## 2. 当前实现盘点

### 2.1 当前前端页面

当前路由位于 `web/src/router.ts`：

| 路由 | 页面 | 当前能力 |
|---|---|---|
| `/projects` | 审查队列 | 项目树、MR、状态、文件进度、Token、OCR 报告链接 |
| `/quality` | 分析情况 | 项目/MR 缺陷分类、变更文件、作者、修复趋势、变更分析 |
| `/usage` | Token 用量 | 今日/月度 Token、最近 20 次审查的输入/输出趋势 |

当前页面已经使用 `@tanstack/vue-query`，并分别以 5 秒、10 秒或 15 秒间隔刷新数据。

### 2.2 当前管理接口

当前主要路由位于 `cmd/ocr-review-bot/main.go` 和 `cmd/ocr-review-bot/quality.go`：

```http
GET /api/v1/admin/dashboard
GET /api/v1/admin/queue?limit=
GET /api/v1/admin/history?limit=
GET /api/v1/admin/projects
GET /api/v1/admin/quality/projects
GET /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}/files
GET /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}/trend
GET /api/v1/reviews/{project_id}/{mr_iid}/{head_sha}
```

当前前端主要依赖 `/api/v1/admin/dashboard`、`/api/v1/admin/projects` 和质量接口；`queue`、`history` 及按 Head SHA 查询的审查接口尚未形成完整的后台任务流程。

### 2.3 当前任务数据

`internal/store/store.go` 的 `ReviewJob` 已经包含后台所需的大部分基础字段：

- 项目、MR、源分支、目标分支；
- `head_sha`、`target_sha`、`base_sha`；
- `rule_sha256`；
- `state`、`stage`、`failure_reason`；
- `priority`、`attempt`、租约信息；
- `queued_at`、`started_at`、`finished_at`；
- `artifact_dir`、`repo_dir`；
- LLM Provider、Model、Session ID；
- Input/Output/Total Token；
- Tool Calls、Comments；
- 文件级进度。

任务唯一性已经使用：

```text
(project_id, mr_iid, head_sha, target_sha)
```

因此后台不能只使用 `project_id + mr_iid` 表示一个审查版本。

### 2.4 当前 Worker 执行阶段

`internal/worker/worker.go` 当前执行阶段为：

```text
rule_preflight
    ↓
git_prepare
    ↓
code_graph（启用时）
    ↓
ocr_review
    ↓
publishing
    ↓
finished
```

Worker 会持久化阶段、文件进度、Session ID 和 Token 使用量，并通过 Publisher 向 GitLab 发布实时进度和发现结果。

### 2.5 当前 OCR 结果

`internal/review/ocr.go` 的审查结果包含：

- `status`；
- LLM Provider/Model；
- 审查摘要；
- 输入、输出、总 Token；
- Cache Read/Write Token；
- Tool Calls；
- Comments；
- Warnings；
- Project Summary；
- Change Analysis；
- Session ID；
- Manifest；
- Affected Files。

Manifest 覆盖率包含：

```text
selected
completed
reused
failed
waived
```

### 2.6 当前 Viewer 能力

OCR Viewer 已经能够读取 JSONL Session，展示：

- 会话概要；
- 审查文件；
- 缺陷；
- Token 使用；
- Finding Timeline；
- New / Unfixed / Fixed 状态；
- LLM 任务记录和工具调用。

后台应继续复用 Viewer 作为深度报告入口，但后台自身需要展示任务控制、状态、覆盖率、版本链和运维信息，不能把所有功能都推给 Viewer。

---

## 3. 当前问题与改造原则

### 3.1 当前问题

#### 问题 A：项目接口承担了任务列表职责

当前 `/api/v1/admin/projects` 会读取所有审查任务，并按项目重新组织。它还会：

- 对项目逐个请求 GitLab 项目详情；
- 解析任务制品；
- 提取覆盖文件；
- 返回所有项目及 MR 数据。

而前端每 5 秒刷新一次。任务数量增长后会产生数据库全量扫描、GitLab N+1 请求和制品重复解析。

#### 问题 B：列表缺少服务端分页、过滤和排序

当前 `ListQueue` 和 `ListHistory` 只接受 `limit`，没有统一的：

- `state` 过滤；
- `stage` 过滤；
- 项目过滤；
- 时间范围；
- 分页游标；
- 排序字段；
- 总数或下一页信息。

#### 问题 C：没有后台任务详情页

用户只能打开 GitLab MR 或 OCR Viewer，无法在管理后台查看完整的执行状态、覆盖率、版本链、失败原因和增量复用信息。

#### 问题 D：任务状态和实际执行语义没有完全对齐

当前 Worker 在评审规则缺失时会记录缺失情况并继续使用 OCR 默认行为执行；后台不应将该场景简单表示为终止性失败。

建议显示：

```text
规则缺失 · 使用默认规则继续
```

如果后续策略改为强制拒绝，则由后端统一状态映射，而不是由前端自行猜测。

#### 问题 E：质量趋势未充分使用 Finding Timeline

当前质量接口通过缺陷内容、文件、行号和类别进行匹配。增量审查已经生成 Finding Timeline，质量分析应优先使用真实的 finding reconciliation 结果。

#### 问题 F：管理接口缺少明确的认证、授权和写操作审计

现有管理路由需要补充：

- OIDC/SSO 或企业统一认证；
- 服务端 RBAC；
- CSRF 防护；
- 写操作原因；
- 审计事件；
- 受控的错误输出。

---

## 4. 目标信息架构

### 4.1 导航结构

```text
总览 Dashboard
审查任务 Reviews
  ├─ 活动任务
  ├─ 历史任务
  └─ 任务详情
质量分析 Quality
Token 用量 Usage
系统状态 System
  ├─ 依赖健康
  ├─ 规则问题
  └─ 审计日志
```

角色对应导航：

| 页面 | Viewer | Operator | Admin | Auditor |
|---|---:|---:|---:|---:|
| 总览 | ✓ | ✓ | ✓ | ✓ |
| 活动任务 | ✓ | ✓ | ✓ | ✓ |
| 历史任务 | ✓ | ✓ | ✓ | ✓ |
| 任务详情 | ✓ | ✓ | ✓ | ✓ |
| 重试/取消/调整优先级 |  | ✓ | ✓ |  |
| 质量分析 | ✓ | ✓ | ✓ | ✓ |
| Token 用量 | ✓ | ✓ | ✓ | ✓ |
| 系统状态 | 部分 | ✓ | ✓ | 只读 |
| 预算和系统配置 |  |  | ✓ |  |
| 审计日志 |  |  | ✓ | ✓ |

### 4.2 总览 Dashboard

路由：

```text
/dashboard
```

#### 顶部指标卡

- 待审任务；
- 审查中；
- 等待重试；
- 发布中；
- 今日通过；
- 今日未通过；
- 基础设施失败；
- stale 任务；
- 今日 Token；
- 本月 Token；
- 平均审查耗时；
- 覆盖完整率。

#### 活动任务区

默认显示最近更新的活动任务：

| 字段 | 说明 |
|---|---|
| 项目/MR | 项目路径、MR IID、标题 |
| 修订 | Head SHA、Target SHA |
| 状态 | 排队、运行、重试等待、发布中 |
| 阶段 | 规则、Git、Graph、OCR、发布 |
| 进度 | 已完成文件/总文件 |
| 缺陷 | Blocking、总数 |
| Token | 当前已累计使用量 |
| 更新时间 | 最后进度更新时间 |

#### 运行健康区

- GitLab API 最近成功请求时间；
- 最近发现轮询开始/完成时间；
- Worker 数量和活跃任务数；
- LLM Preflight 最近结果；
- Code Graph 最近构建结果；
- OCR Viewer 是否可访问；
- 数据库是否可写。

#### 风险区

展示最近出现的：

- 规则缺失/无效项目；
- LLM 配置错误；
- Code Graph 构建失败；
- Git 工作区准备失败；
- 发布失败；
- 覆盖不完整任务；
- 重试次数达到上限的任务。

### 4.3 审查任务列表

路由：

```text
/reviews
```

#### 筛选条件

- 项目/Group；
- MR IID；
- 状态；
- 执行阶段；
- 源分支；
- 目标分支；
- Head SHA；
- 是否有 Blocking Finding；
- 是否覆盖不完整；
- 是否使用增量 Session；
- LLM Provider/Model；
- 排队时间范围；
- 完成时间范围。

#### 状态分组

```text
活动任务
  queued
  retry_wait
  running
  publishing

终态任务
  completed_pass
  completed_fail
  rejected_rule_missing
  rejected_rule_invalid
  failed_infra
  stale
```

`rejected_rule_missing` 的含义必须由后端统一定义。如果当前策略是缺失规则时继续执行，则不应在正常任务中使用该终态；可以将规则缺失作为 `rule_status=missing_default_used` 单独返回。

#### 列表列

| 列 | 内容 |
|---|---|
| 项目/MR | 项目路径、IID、标题、GitLab 链接 |
| 分支 | Source → Target |
| 修订 | Head SHA、Target SHA |
| 状态 | 状态标签和失败类型 |
| 阶段 | 当前阶段 |
| 文件进度 | completed / total |
| 覆盖率 | completed、reused、failed |
| 缺陷 | blocking、total |
| Token | total、重试标识 |
| Attempt | 当前尝试次数 |
| 时间 | 排队、开始、结束或最后更新 |
| 操作 | 查看、重试、取消、调整优先级 |

### 4.4 审查详情

路由：

```text
/reviews/:review_id
```

#### 详情头部

显示：

- 项目路径；
- MR IID、标题；
- GitLab MR 链接；
- 当前任务状态；
- 当前阶段；
- Head SHA；
- Target SHA；
- Base SHA；
- Source → Target 分支；
- 排队、开始、完成时间；
- Attempt；
- 报告链接。

#### 概览页签

- 任务摘要；
- 审查结果；
- Blocking Finding 数量；
- 规则状态；
- 是否为增量恢复；
- 是否达到 Token Budget；
- LLM Provider/Model；
- Token 和 Tool Calls；
- 代码审查版本；
- Code Graph 影响范围。

#### 执行时间线页签

```text
queued
  → rule_preflight
  → git_prepare
  → code_graph
  → ocr_review
  → publishing
  → finished
```

每个阶段显示：

- 开始时间；
- 结束时间；
- 耗时；
- 阶段状态；
- 文件进度；
- 阶段级安全错误摘要。

不保存和展示完整的 LLM Prompt、源码全文或敏感 Header。

#### 覆盖率页签

显示：

| 分类 | 含义 |
|---|---|
| Selected | 本次计划审查的文件 |
| Completed | 本次完成审查的文件 |
| Reused | 从历史 Session 复用的文件 |
| Failed | 审查失败的文件 |
| Waived | 被规则或策略豁免的文件 |
| Affected | Code Graph 判定的影响文件 |

必须显式展示：

```text
覆盖状态：完整 / 不完整
新增审查文件：N
复用文件：N
失败文件：N
```

#### 缺陷页签

筛选：

- Severity：Critical、High、Medium、Low；
- Category：Security、Correctness/Bug、Reliability、Performance、Maintainability、Test、Style、Documentation、Other；
- Finding 状态：New、Unfixed、Fixed、Current；
- 文件路径。

每条缺陷显示：

- 文件；
- 起止行；
- 严重度；
- 类别；
- 内容；
- Existing Code；
- Suggestion Code；
- 当前 Finding 状态；
- GitLab Discussion/Note 链接。

#### 版本链页签

同一 `project_id + mr_iid` 下按时间倒序显示所有版本：

| 字段 | 说明 |
|---|---|
| Head SHA | MR 源代码版本 |
| Target SHA | 目标分支版本 |
| Base SHA | 实际比较基线 |
| 状态 | 当前结果 |
| Session | 新建或复用 |
| Reused | 复用文件数 |
| Rerun | 重新审查文件数 |
| New | 新增缺陷数 |
| Unfixed | 未修复缺陷数 |
| Fixed | 已修复缺陷数 |
| Stale 原因 | 被哪个新版本替代 |

#### 运维页签

仅 Operator/Admin 显示：

- Attempt；
- Retry Wait 时间；
- 失败分类；
- 安全错误摘要；
- Worker/租约状态；
- Retry；
- Cancel；
- 修改优先级；
- 查看审计记录。

不展示 `repo_dir`、`artifact_dir`、`lease_owner` 等内部路径和运行时标识。

### 4.5 质量分析

路由：

```text
/quality
```

保留当前项目树和 MR 维度，但调整展示重点。

#### 项目级指标

- 审查 MR 数；
- 通过率；
- 未通过率；
- Blocking Finding 数；
- New Finding 数；
- Unfixed Finding 数；
- Fixed Finding 数；
- 平均每 MR 缺陷数；
- 平均变更文件数；
- 近 7/30 天趋势。

#### MR 级指标

- 变更文件数；
- 增加/删除行数；
- 缺陷总数；
- Severity 分布；
- Category 分布；
- New/Unfixed/Fixed 分布；
- 变更影响分析；
- 提交人和文件作者；
- Code Graph 影响文件。

#### 文件级视图

显示：

- 文件路径；
- Old Path；
- 增加行数；
- 删除行数；
- 缺陷数；
- Severity 最高级别；
- 参与提交人；
- 是否属于受影响文件。

#### 修复趋势

修复趋势优先读取 OCR Session 中的 `FindingTimeline`：

- 当前缺陷数；
- 新增缺陷数；
- 未修复缺陷数；
- 累计已修复缺陷数；
- 覆盖不完整次数；
- 每个版本的 Head SHA。

当前基于文本、行号和类别的匹配可以保留为历史数据降级策略，但不能优先于真实 reconciliation 结果。

### 4.6 Token 用量

路由：

```text
/usage
```

#### 汇总维度

- 今日；
- 最近 7 天；
- 最近 30 天；
- 当月；
- 自定义时间范围。

#### 统计维度

- GitLab Group；
- 项目；
- MR；
- Provider；
- Model；
- 结果状态；
- 是否重试；
- 是否增量复用；
- 是否 Code Graph 启用。

#### 指标

- Input Token；
- Output Token；
- Cache Read Token；
- Cache Write Token；
- Total Token；
- Tool Calls；
- 平均 Token；
- P50/P95 Token；
- P50/P95 审查耗时；
- 失败、partial、stale、retry 造成的消耗；
- Token Budget 达到次数。

Token 用量指计费使用量，后台任何页面都不能展示 LLM 鉴权 Token。

### 4.7 系统状态

路由：

```text
/system
```

#### 依赖健康

- GitLab API；
- 当前 Bot 用户；
- LLM Endpoint；
- Code Graph 命令和版本；
- OCR Viewer；
- SQLite；
- Artifact 目录；
- Git Cache。

#### 规则问题

展示：

- 规则文件缺失项目；
- 规则校验失败项目；
- 最近规则 SHA；
- 最近一次规则读取时间；
- 当前任务受影响情况。

#### 审计日志

按以下条件筛选：

- actor；
- action；
- review_id；
- 时间范围；
- IP；
- request_id。

---

## 5. 状态模型和展示规范

### 5.1 状态机

```text
queued ───────────────┐
                      ↓
retry_wait ───────→ running
                      │
                      ├─ rule_preflight
                      ├─ git_prepare
                      ├─ code_graph
                      └─ ocr_review
                              ↓
                         publishing
                              ↓
                 ┌────────────┴────────────┐
                 ↓                         ↓
          completed_pass            completed_fail

running / retry_wait / publishing ──→ stale
running ──→ failed_infra
running ──→ retry_wait
```

规则拒绝状态作为独立的终态或规则状态处理，具体取决于当前 Worker 策略。前后端必须使用统一的状态字典，不允许每个页面自行维护一套映射。

### 5.2 状态显示

| 内部状态 | 中文展示 | 颜色 | 说明 |
|---|---|---|---|
| queued | 排队中 | 默认 | 等待 Worker |
| retry_wait | 等待重试 | 警告 | 保留 Session，等待自动恢复 |
| running | 审查中 | 主色 | 当前正在执行 |
| publishing | 发布中 | 主色 | 正在发布 GitLab 结果 |
| completed_pass | 通过 | 成功 | 无阻断缺陷且覆盖完整 |
| completed_fail | 未通过 | 危险 | 存在阻断缺陷或结果不通过 |
| failed_infra | 基础设施失败 | 危险 | Git、LLM、Graph、Viewer 等故障 |
| stale | 已过期 | 信息 | 被新 Head/Target 版本替代 |
| rejected_rule_invalid | 规则无效 | 警告 | 规则无法解析或验证 |
| rejected_rule_missing | 规则缺失 | 警告 | 仅在强制规则策略下作为终态 |

### 5.3 完成判定

后台应同时展示三个维度：

```text
任务状态：completed_fail
审查覆盖：incomplete
阻断缺陷：2
```

不能仅根据 `completed_fail` 推断失败原因。

任务失败原因至少区分：

- `blocking_findings`；
- `coverage_incomplete`；
- `rule_invalid`；
- `rule_missing`；
- `llm_configuration`；
- `llm_timeout`；
- `git_prepare`；
- `code_graph`；
- `publish`；
- `worker_interrupted`；
- `target_revision_changed`；
- `unknown_infra`。

---

## 6. 后端 API 设计

### 6.1 通用约定

所有管理 API：

- 使用 `/api/v1/admin` 前缀；
- 返回 JSON；
- 支持 request ID；
- 统一错误格式；
- 不直接返回数据库模型；
- 不返回密钥、本地路径和完整源代码；
- 列表接口使用服务端分页；
- 时间使用 RFC3339 UTC；
- Token 使用整数；
- 零 Token 作为合法值处理。

统一错误格式：

```json
{
  "error": {
    "code": "review_state_conflict",
    "message": "review is no longer in the expected state",
    "request_id": "req-123"
  }
}
```

### 6.2 当前用户和权限

```http
GET /api/v1/admin/me
```

响应：

```json
{
  "id": "user-100",
  "name": "Alice",
  "username": "alice",
  "roles": ["operator"],
  "permissions": [
    "review.read",
    "review.retry",
    "review.cancel",
    "review.priority"
  ]
}
```

### 6.3 总览

```http
GET /api/v1/admin/dashboard?range=24h
```

响应：

```json
{
  "generated_at": "2026-08-18T08:00:00Z",
  "range": "24h",
  "counts": {
    "queued": 3,
    "running": 2,
    "retry_wait": 1,
    "publishing": 0,
    "passed": 42,
    "failed": 8,
    "failed_infra": 2,
    "stale": 12
  },
  "usage": {
    "today_tokens": 120000,
    "month_tokens": 3200000
  },
  "quality": {
    "blocking_findings": 6,
    "coverage_incomplete": 2,
    "coverage_complete_rate": 0.94
  },
  "runtime": {
    "worker_count": 2,
    "active_workers": 1,
    "last_discovery_at": "2026-08-18T07:59:30Z"
  }
}
```

### 6.4 审查任务列表

```http
GET /api/v1/admin/reviews
```

查询参数：

```text
scope=active|history|all
state=queued,running,completed_fail
stage=ocr_review
project_id=105
mr_iid=28
has_blocking=true|false
coverage=incomplete|complete
from=2026-08-01T00:00:00Z
to=2026-08-18T00:00:00Z
page=1
page_size=50
sort=updated_at.desc
```

响应：

```json
{
  "items": [
    {
      "id": 1024,
      "project": {
        "id": 105,
        "path_with_namespace": "group/service",
        "web_url": "https://gitlab.example/group/service"
      },
      "merge_request": {
        "iid": 28,
        "title": "Fix command execution",
        "web_url": "https://gitlab.example/group/service/-/merge_requests/28"
      },
      "revision": {
        "head_sha": "abc123",
        "target_sha": "def456",
        "base_sha": "789abc"
      },
      "status": {
        "state": "running",
        "stage": "ocr_review",
        "attempt": 1,
        "failure_class": ""
      },
      "progress": {
        "completed": 8,
        "total": 12,
        "percent": 67
      },
      "coverage": {
        "selected": 12,
        "completed": 8,
        "reused": 3,
        "failed": 0,
        "waived": 1,
        "complete": true
      },
      "findings": {
        "total": 3,
        "blocking": 1,
        "new": 1,
        "unfixed": 2,
        "fixed": 0
      },
      "usage": {
        "input_tokens": 12000,
        "output_tokens": 1800,
        "total_tokens": 13800,
        "tool_calls": 24
      },
      "queued_at": "2026-08-18T07:40:00Z",
      "started_at": "2026-08-18T07:41:00Z",
      "finished_at": null,
      "updated_at": "2026-08-18T07:48:00Z",
      "report_url": "/r/group-service/session-1"
    }
  ],
  "page": 1,
  "page_size": 50,
  "total": 1,
  "has_next": false
}
```

### 6.5 审查详情

```http
GET /api/v1/admin/reviews/{review_id}
```

详情响应在列表字段基础上增加：

- `rule`；
- `session`；
- `llm`；
- `timing`；
- `coverage.items`；
- `affected_files`；
- `failure`；
- `publication`；
- `revision_chain`。

示例：

```json
{
  "id": 1024,
  "rule": {
    "path": ".opencodereview/rule.json",
    "status": "valid",
    "sha256": "rule-sha"
  },
  "session": {
    "id": "session-1",
    "resumed": true,
    "resumed_from": "session-previous",
    "reused_files": 3,
    "rerun_files": 9
  },
  "llm": {
    "provider": "ocr-bot",
    "model": "model-name"
  },
  "publication": {
    "state": "published",
    "comments": 3,
    "blocking_comments": 1,
    "report_url": "/r/group-service/session-1"
  }
}
```

### 6.6 事件和时间线

```http
GET /api/v1/admin/reviews/{review_id}/events?cursor=&limit=100
```

事件类型：

```text
job_queued
stage_started
stage_finished
progress_updated
usage_updated
finding_published
retry_scheduled
job_staled
job_finished
job_failed
```

事件不保存完整源码、完整 LLM Prompt 或敏感凭证，只保存安全摘要和统计字段。

### 6.7 覆盖率

```http
GET /api/v1/admin/reviews/{review_id}/coverage
```

响应：

```json
{
  "complete": false,
  "selected": [
    {"path": "internal/service.go", "fingerprint": "..."}
  ],
  "completed": [],
  "reused": [],
  "failed": [
    {"path": "internal/client.go", "reason": "file timeout"}
  ],
  "waived": [],
  "affected_files": ["internal/service.go"]
}
```

### 6.8 缺陷

```http
GET /api/v1/admin/reviews/{review_id}/findings
```

查询参数：

```text
severity=critical,high
category=security
status=new,unfixed
path=internal/service.go
page=1
page_size=50
```

### 6.9 版本链

```http
GET /api/v1/admin/reviews/{review_id}/revisions
```

建议以 `project_id + mr_iid` 查询同一 MR 的版本链，返回每个版本的：

- Head SHA；
- Target SHA；
- Base SHA；
- 任务 ID；
- Session ID；
- 状态；
- stale 原因；
- 覆盖统计；
- New/Unfixed/Fixed 统计。

### 6.10 管理操作

```http
POST /api/v1/admin/reviews/{review_id}/retry
POST /api/v1/admin/reviews/{review_id}/cancel
POST /api/v1/admin/reviews/{review_id}/priority
```

请求必须包含原因和预期状态：

```json
{
  "reason": "LLM 服务已恢复，重新执行审查",
  "expected_state": "failed_infra"
}
```

优先级接口：

```json
{
  "priority": 100,
  "reason": "高优先级发布分支"
}
```

所有操作必须：

1. 服务端检查角色；
2. 服务端检查任务状态；
3. 使用条件更新避免竞态；
4. 写入 `audit_event`；
5. 返回新的任务状态。

### 6.11 质量接口

```http
GET /api/v1/admin/quality/projects
GET /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}
GET /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}/files
GET /api/v1/admin/quality/projects/{project_id}/mrs/{mr_iid}/timeline
```

质量接口应返回统一的 `severity_counts`、`category_counts`、`finding_status_counts`，前端不再自行定义多套类别映射。

### 6.12 用量接口

```http
GET /api/v1/admin/usage/summary?from=&to=&group_by=
GET /api/v1/admin/usage/trend?from=&to=&interval=day
GET /api/v1/admin/usage/projects?from=&to=&page=&page_size=
GET /api/v1/admin/usage/models?from=&to=
```

### 6.13 运维和审计接口

```http
GET  /api/v1/admin/health/dependencies
GET  /api/v1/admin/rules/problems
GET  /api/v1/admin/audit-events
POST /api/v1/admin/reconcile
```

---

## 7. 数据模型调整

### 7.1 Review Job 继续作为任务主表

保留现有 `review_job`，增加或派生以下字段：

```text
failure_class
last_progress_at
duration_ms
rule_status
coverage_selected
coverage_completed
coverage_reused
coverage_failed
coverage_waived
coverage_complete
blocking_findings
new_findings
unfixed_findings
fixed_findings
cache_read_tokens
cache_write_tokens
publication_state
publication_comments
```

其中列表必须优先读取汇总字段，不能对每次列表请求都重新解析 `ocr-result.json`。

### 7.2 Review Event

新增事件表：

```sql
CREATE TABLE review_event (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  review_job_id INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  stage TEXT NOT NULL DEFAULT '',
  safe_message TEXT NOT NULL DEFAULT '',
  completed INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  FOREIGN KEY(review_job_id) REFERENCES review_job(id)
);
```

索引：

```sql
CREATE INDEX idx_review_event_job_time
ON review_event(review_job_id, created_at, id);
```

进度事件需要限流，避免每个 LLM 回调都产生数据库写入。Token 数据仍然必须单调持久化。

### 7.3 审计事件

复用已有 `audit_event`，建议将 `detail` 规范化为 JSON：

```json
{
  "reason": "LLM 服务已恢复",
  "expected_state": "failed_infra",
  "old_state": "failed_infra",
  "new_state": "queued",
  "request_id": "req-123",
  "source_ip": "10.0.0.1"
}
```

### 7.4 Token 日汇总

当历史任务增大后新增按日聚合表：

```text
usage_daily
  date
  project_id
  provider
  model
  state
  input_tokens
  output_tokens
  cache_read_tokens
  cache_write_tokens
  total_tokens
  tool_calls
  review_count
```

首期可以从 `review_job` 聚合；当数据量达到性能阈值后再启用日汇总表。

---

## 8. 前端设计

### 8.1 建议目录

```text
web/src/
├── api/
│   ├── client.ts
│   ├── dashboard.ts
│   ├── reviews.ts
│   ├── quality.ts
│   ├── usage.ts
│   └── system.ts
├── types/
│   ├── dashboard.ts
│   ├── review.ts
│   ├── quality.ts
│   └── usage.ts
├── components/
│   ├── review/
│   │   ├── ReviewStateTag.vue
│   │   ├── ReviewStageTimeline.vue
│   │   ├── CoveragePanel.vue
│   │   ├── FindingTable.vue
│   │   └── RevisionChain.vue
│   ├── usage/
│   └── system/
├── views/
│   ├── Dashboard.vue
│   ├── ReviewList.vue
│   ├── ReviewDetail.vue
│   ├── QualityTrend.vue
│   ├── TokenUsage.vue
│   └── SystemStatus.vue
└── router.ts
```

### 8.2 API Client

统一处理：

- JSON 请求；
- 认证过期；
- 错误码；
- request ID；
- 取消请求；
- 查询参数；
- 401/403/409/429/500 状态。

前端不直接拼接多个页面中的 URL，也不在页面中重复声明任务类型。

### 8.3 查询策略

| 数据类型 | 刷新策略 |
|---|---|
| Dashboard | 10 秒 |
| 活动任务 | 5 秒 |
| 历史任务 | 手动刷新或 30 秒 |
| 任务详情 | 活动期间 5 秒，终态停止 |
| 质量分析 | 30 秒 |
| Token 统计 | 60 秒 |
| 系统健康 | 15 秒 |
| 事件时间线 | 首期分页轮询，后续可 SSE |

使用 Vue Query 的 `staleTime`、`enabled` 和 query key 管理刷新，避免同一接口在多个组件中重复请求。

### 8.4 加载和错误体验

所有页面必须有：

- 首次加载骨架；
- 空状态；
- 局部请求失败提示；
- 重试按钮；
- 最后更新时间；
- 权限不足提示；
- 409 状态冲突提示；
- 终态数据和实时数据的明确区分。

---

## 9. 安全设计

### 9.1 身份认证

所有 `/api/v1/admin/**` 接入企业 OIDC/SSO 或统一 Session/JWT 认证。前端路由守卫只用于用户体验，不能替代后端认证。

### 9.2 RBAC

| 角色 | 权限 |
|---|---|
| Viewer | 查看队列、详情、报告、质量和用量 |
| Operator | Viewer + Retry、Cancel、Priority |
| Admin | Operator + 预算、系统配置、人工豁免、Reconcile |
| Auditor | 只读审计、用量和历史导出 |

### 9.3 CSRF 和写操作

Retry、Cancel、Priority、Reconcile 等写操作必须：

- 使用 CSRF Token 或 SameSite Session；
- 检查请求来源；
- 校验 `expected_state`；
- 要求填写原因；
- 写入审计事件。

### 9.4 敏感数据

禁止通过后台 API 返回：

- GitLab Token；
- LLM Token；
- Authorization Header；
- 本地 `repo_dir`；
- 本地 `artifact_dir`；
- 完整 LLM Prompt；
- 未受控的源码全文。

错误信息使用稳定错误码和安全摘要，原始错误只写入脱敏日志。

### 9.5 报告和源码访问

后台只返回受控的 `report_url`。OCR Viewer 继续负责 Session 报告展示，并保留其 Host Header 防护。

如果后台部署在多用户环境，应增加：

- 登录用户到 GitLab 用户的映射；
- 项目访问权限检查；
- 报告链接的短期授权或反向代理权限校验。

---

## 10. 性能和可观测性

### 10.1 必须避免的访问模式

后台不能在一个列表请求中：

- 扫描全部历史任务后再分页；
- 每个项目逐个调用 GitLab；
- 每个任务重新解析大型 OCR 结果；
- 将完整制品内容塞进列表响应。

### 10.2 推荐索引

```sql
CREATE INDEX idx_review_job_active
ON review_job(state, priority DESC, queued_at);

CREATE INDEX idx_review_job_project_time
ON review_job(target_project_id, queued_at DESC);

CREATE INDEX idx_review_job_mr_revision
ON review_job(project_id, mr_iid, head_sha, target_sha);

CREATE INDEX idx_review_job_finished_project
ON review_job(finished_at, project_id);
```

### 10.3 项目元数据缓存

GitLab 项目基础信息建议短期缓存：

- 名称；
- 路径；
- 描述；
- Web URL。

项目名称变化不应导致每次后台刷新都访问 GitLab。

### 10.4 日志和指标

使用现有 `slog` JSON 日志体系，至少增加：

- API 请求数；
- API 响应耗时；
- API 4xx/5xx 数量；
- 活动任务数；
- 任务状态转移数；
- Retry/Cancel 数量；
- GitLab 请求耗时和失败数；
- LLM Preflight 失败数；
- Code Graph 构建耗时和失败数；
- 报告解析失败数。

---

## 11. 实施阶段

### Phase 0：接口和状态契约

目标：先统一后端语义，避免前端继续依赖数据库字段。

任务：

1. 定义统一状态、阶段、失败分类和规则状态；
2. 定义 Admin DTO；
3. 增加分页、过滤、排序参数模型；
4. 将 `ReviewJob` 与 API DTO 解耦；
5. 统一错误格式和 request ID；
6. 增加状态映射测试。

产出：

- `internal/admin/dto.go`；
- API 错误规范；
- 状态字典；
- OpenAPI 或接口示例文档。

### Phase 1：审查任务中心

目标：替代当前以 `/projects` 为中心的全量列表。

任务：

1. 实现 `/api/v1/admin/reviews`；
2. 增加服务端分页和过滤；
3. 增加 Dashboard 活动任务；
4. 实现 `/reviews` 页面；
5. 实现 `/reviews/:id` 概览和时间线；
6. 修复项目元数据 N+1；
7. 增加审查详情 API 测试。

验收：

- 大列表不再全量加载；
- 活动任务能在 5 秒内更新；
- 当前任务状态和 Worker 阶段一致；
- 页面不暴露本地路径和密钥。

### Phase 2：覆盖率、缺陷和版本链

目标：体现增量审查和代码质量能力。

任务：

1. 增加 Review Event；
2. 增加 Coverage API；
3. 增加 Findings API；
4. 接入 Finding Timeline；
5. 增加 Revision Chain；
6. 增加 Code Graph Affected Files；
7. 详情页增加覆盖率、缺陷、版本链页签。

验收：

- New/Unfixed/Fixed 与 Viewer 一致；
- Session 复用和重新审查文件数正确；
- stale 版本不会被显示为当前版本；
- 覆盖不完整有明确警告。

### Phase 3：质量和 Token 分析

目标：从单次结果统计升级为长期趋势分析。

任务：

1. 统一 Severity/Category 字典；
2. 增加项目、MR、文件三个质量维度；
3. 增加 7/30 天趋势；
4. 增加按项目、模型、状态的 Token 聚合；
5. 增加失败、重试、stale Token 分析；
6. 增加 Cache Token 和 Tool Calls；
7. 必要时增加按日汇总表。

验收：

- Token 使用量与任务实际持久化数据一致；
- 支持项目和模型排行；
- 质量趋势使用真实 finding 状态；
- 零 Token 不被误判为缺失。

### Phase 4：权限、运维和审计

目标：让后台具备生产环境运维能力。

任务：

1. 接入 OIDC/SSO；
2. 实现 Viewer/Operator/Admin/Auditor；
3. 实现 Retry、Cancel、Priority；
4. 实现 Reconcile；
5. 接入 CSRF；
6. 完善 audit_event；
7. 增加依赖健康和规则问题页面；
8. 增加预算和告警能力。

验收：

- 无权限用户无法执行写操作；
- 所有写操作都可追溯；
- 并发状态变化不会被旧页面覆盖；
- 敏感配置和源码不被接口泄漏。

### Phase 5：实时能力和规模优化

目标：在确实需要时引入实时推送和聚合优化。

任务：

1. 根据轮询压力评估 SSE；
2. 增加事件游标和断线恢复；
3. 使用 Token 日汇总表；
4. 增加历史导出；
5. 增加制品保留和清理策略；
6. 增加数据库备份和恢复检查。

不在该阶段前引入 Redis、Kafka 或微服务拆分。

---

## 12. 推荐代码组织

```text
internal/admin/
├── auth.go
├── dashboard.go
├── dto.go
├── errors.go
├── handler.go
├── quality.go
├── reviews.go
├── system.go
├── usage.go
└── audit.go

internal/store/
├── review_queries.go
├── review_events.go
├── usage_queries.go
└── audit.go

web/src/
├── api/
│   ├── client.ts
│   ├── dashboard.ts
│   ├── reviews.ts
│   ├── quality.ts
│   ├── usage.ts
│   └── system.ts
├── types/
│   ├── dashboard.ts
│   ├── review.ts
│   ├── quality.ts
│   └── usage.ts
├── components/
│   ├── review/
│   │   ├── ReviewStateTag.vue
│   │   ├── ReviewStageTimeline.vue
│   │   ├── CoveragePanel.vue
│   │   ├── FindingTable.vue
│   │   └── RevisionChain.vue
│   └── common/
├── views/
│   ├── Dashboard.vue
│   ├── ReviewList.vue
│   ├── ReviewDetail.vue
│   ├── QualityTrend.vue
│   ├── TokenUsage.vue
│   └── SystemStatus.vue
└── router.ts
```

建议逐步拆分 `cmd/ocr-review-bot/main.go` 中的管理路由，不要继续把所有 Admin API 添加到 `main.go`。

---

## 13. 测试和验收标准

### 13.1 后端接口测试

必须覆盖：

- 默认分页；
- 最大分页；
- 状态过滤；
- 项目过滤；
- 时间范围；
- 排序；
- 空列表；
- 任务不存在；
- Head SHA 不匹配；
- Target SHA 变化；
- 状态冲突；
- 任务重试；
- 任务取消；
- 优先级修改；
- 权限拒绝；
- 审计事件写入；
- 错误信息脱敏。

### 13.2 增量审查测试

场景：

1. 同一 MR 第一次审查；
2. Source Head 变化；
3. Target Branch 变化；
4. 规则 SHA 不变；
5. 规则 SHA 变化；
6. 部分文件指纹未变；
7. 部分文件审查失败；
8. 新版本替代旧版本。

预期：

- 旧任务标记 stale；
- 新任务继承正确 Session；
- 只有必要文件重新审查；
- 版本链顺序正确；
- Finding Timeline 状态正确；
- Target revision 变化不会使用错误的结果。

### 13.3 Token 测试

验证：

- 使用实际上游 usage 字段；
- Token 累加单调；
- 零 Token 不被当成异常；
- 重试 Token 单独计入；
- Cache Read/Write 正确展示；
- 项目、模型和状态聚合一致。

### 13.4 前端验收

验证：

- 首次加载、空状态、错误状态；
- 5 秒活动任务刷新；
- 终态任务停止频繁刷新；
- 详情页面各页签；
- 筛选和分页；
- 409 状态冲突；
- 权限不足；
- 移动端窄屏；
- GitLab 和 OCR 报告链接；
- 不显示内部目录和敏感配置。

### 13.5 性能验收

建议使用至少以下数据规模验证：

- 100 个项目；
- 1000 个 MR 版本；
- 单个 MR 20 次增量审查；
- 单个任务 500 个文件；
- 单个 OCR 结果包含大量 Finding。

验收要求：

- 任务列表使用数据库分页；
- 单次列表请求不解析全部历史制品；
- 项目详情不产生无界 GitLab N+1 请求；
- 任务详情按需加载制品细节；
- 页面刷新不会产生重复请求风暴。

---

## 14. 非目标和边界

第一版明确不做：

1. 自建用户名密码体系；
2. 浏览器直接访问 SQLite；
3. 浏览器直接访问 GitLab Token；
4. 浏览器直接访问本地制品目录；
5. 使用 GitLab Commit Status 作为后台状态来源；
6. 将完整 LLM Prompt 展示给所有后台用户；
7. 在后台执行仓库脚本、测试或包管理器命令；
8. 引入 PostgreSQL、Redis、Kafka、Elasticsearch；
9. 将后台拆成独立微服务；
10. 第一阶段引入复杂 SSE 或 WebSocket。

审查结果继续以 GitLab MR Note/Discussion 发布为主；后台只负责展示发布结果、链接和发布状态。

---

## 15. 最终推荐

实施顺序应为：

```text
统一状态和 DTO
    ↓
服务端分页的审查任务中心
    ↓
审查详情和执行时间线
    ↓
覆盖率、缺陷和版本链
    ↓
质量和 Token 趋势
    ↓
认证、RBAC、写操作和审计
    ↓
SSE、汇总表和规模优化
```

第一版上线标准不是“页面数量增加”，而是管理员能够回答以下问题：

1. 当前有哪些任务正在审查？
2. 每个任务卡在哪个阶段？
3. 审查覆盖是否完整？
4. 哪些文件被复用，哪些文件重新审查或失败？
5. 当前未通过的原因是缺陷、规则、LLM、Code Graph 还是发布失败？
6. 这个 MR 的上一版本发生了什么？
7. 哪些缺陷是新增、未修复或已修复？
8. 本次审查消耗了多少实际 Token？
9. 管理员执行的重试、取消和优先级修改是否可追溯？
10. 后台是否在不泄露源码和密钥的情况下提供了足够的运维信息？

满足以上问题后，后台 Web 才真正覆盖当前 OCR Review Bot 的审查任务、增量审查、Code Graph、缺陷演进、GitLab 发布和运行成本能力。
