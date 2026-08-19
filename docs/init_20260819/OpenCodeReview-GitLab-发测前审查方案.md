# OpenCodeReview + GitLab 发测前变更审查方案

> 目标：基于 Alibaba OpenCodeReview，在代码仓库“发测试”前自动执行变更审查，输出机器可读报告、影响范围和代码审查意见，并将结果发布到 GitLab Merge Request（MR）和 CI/CD 制品中。
>
> 依据：OpenCodeReview 中文说明及其仓库内置 GitLab CI 示例，读取日期：2026-08-12。

## 1. 结论与推荐方案

推荐采用 **GitLab CI/CD + OpenCodeReview CLI + GitLab MR Discussions API + CI Artifact** 的方案：

```text
开发提交代码
    ↓
创建/更新 MR
    ↓
GitLab MR pipeline
    ├─ 获取目标分支与当前提交的完整 Git 历史
    ├─ ocr review --from origin/<目标分支> --to <当前 SHA> --format json
    ├─ 生成变更摘要、影响范围、逐行审查意见
    ├─ 将高风险问题发布为 MR 行级 Discussion
    ├─ 将完整 JSON/Markdown 报告保存为 CI Artifact
    └─ 发测 Job 读取审查结果，按门禁策略决定是否允许发测
```

**推荐的第一阶段落地边界：**

1. 触发点放在 MR 创建/更新，而不是直接在部署脚本中临时扫描。
2. 以 `origin/<target_branch>...CI_COMMIT_SHA` 的 merge-base diff 作为审查范围。
3. OpenCodeReview 输出 JSON；GitLab 发布脚本负责把评论转成行级 Discussion 和汇总 Note。
4. 影响范围由确定性脚本计算，AI 只负责补充业务影响解释；避免把文件统计完全交给模型。
5. 第一阶段仅对 `critical/high` 设置阻断发测，`medium/low` 作为报告和人工确认项。
6. 每次审查保留原始 JSON、stderr、Markdown 报告和统计 dotenv，便于复盘和审计。

该方案优先复用上游仓库的 `examples/gitlab_ci/.gitlab-ci.yml` 和 `post_review.py`，不建议一开始自建 GitLab Webhook 服务。

## 2. OpenCodeReview 能力边界

### 2.1 可直接复用的能力

- **Diff 审查**：`ocr review` 支持工作区、分支范围和单 commit 审查。
- **分支范围审查**：`ocr review --from <ref> --to <ref>` 使用 merge-base 计算变更，适合 MR。
- **结构化输出**：`--format json --audience agent` 适合 CI 消费；输出包含 `comments`、`summary`、`warnings`、`project_summary`、`manifest` 等字段。
- **行级定位**：评论包含文件路径和行号，可转为 GitLab Discussion position。
- **上下文读取**：Agent 可读取完整文件、搜索代码库、检查其他变更文件，因此不局限于逐行 diff。
- **文件过滤与规则匹配**：采用确定性文件筛选、文件打包、规则匹配；支持项目级 `.opencodereview/rule.json` 或 `--rule`。
- **全量扫描**：`ocr scan` 可用于基线审计或没有有效 diff 的目录，但不应替代发测前的 MR diff 审查。
- **并发与恢复**：支持 `--concurrency`、超时、会话恢复，适用于大型 MR。
- **多模型接入**：支持 OpenAI 兼容端点、Anthropic，以及自定义 provider；可使用企业内部模型网关。

### 2.2 需要外接实现的能力

OpenCodeReview 本身主要负责“发现问题”，以下能力需要 GitLab CI/脚本补齐：

- 影响范围的确定性清单和风险分级。
- 发测门禁、审批状态和失败策略。
- 将 JSON 评论发布为 GitLab inline Discussion。
- 汇总 Note 的幂等更新、重复评论去重和降级处理。
- 生成固定格式的 Markdown 报告和制品归档。
- 变更文件到模块、服务、接口、数据库、配置、部署单元的映射。
- 测试建议、回滚建议、验证环境建议。

**重要边界：** AI 审查不能替代编译、单元测试、集成测试、SAST、依赖漏洞扫描和人工审批。发测门禁应组合这些结果。

## 3. GitLab 发测流程设计

### 3.1 触发方式

#### 推荐：MR Pipeline

```yaml
workflow:
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
```

在 MR 创建、更新、重新打开时触发审查。每次新 push 都重新审查当前 MR 的完整变更范围，保证报告对应当前 `CI_COMMIT_SHA`。

#### 发测门禁

在审查 Job 之后增加 `release-test` 或 `deploy-test` Job：

```text
review → quality-gate → deploy-test
```

`quality-gate` 读取 `.ocr/ocr-stats.env` 与 `.ocr/ocr-result.json`：

- 审查执行失败、结果不可解析、覆盖率不完整：阻断或进入人工豁免。
- 存在 critical/high：默认阻断发测。
- 只有 medium/low：允许发测，但报告必须可见。
- 无问题：允许发测。

### 3.2 Diff 基准

MR pipeline 中建议使用：

```bash
ocr review \
  --from "origin/${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}" \
  --to "${CI_COMMIT_SHA}" \
  --format json \
  --audience agent
```

要求：

- `GIT_DEPTH: 0`，确保 merge-base 和目标分支历史可用。
- 使用 `CI_COMMIT_SHA` 作为 `--to`，兼容同仓库 MR 和 fork MR。
- 不使用分支名替代提交 SHA，避免并发 push 或 fork 场景下审查错版本。
- 在审查结果中记录目标分支、源提交、pipeline ID、MR IID 和 OCR 版本。

### 3.3 LLM 配置

建议在 GitLab Settings → CI/CD → Variables 中配置：

| 变量 | 必需 | 用途 |
|---|---:|---|
| `OCR_LLM_URL` | 是 | OpenAI 兼容或企业模型网关地址 |
| `OCR_LLM_AUTH_TOKEN` | 是 | LLM 鉴权 Token，Masked/Protected |
| `OCR_LLM_MODEL` | 是 | 模型名称 |
| `GITLAB_API_TOKEN` | 是（推荐） | 发布 MR Discussion，建议 Project Access Token，`api` scope |
| `OCR_VERSION` | 推荐 | 固定 OCR 版本，避免 `latest` 漂移 |
| `OCR_LANGUAGE` | 推荐 | 设置为 `中文` |
| `OCR_REVIEW_CONCURRENCY` | 否 | 控制并发 LLM 请求，例如 `5` |
| `OCR_LLM_TIMEOUT` | 否 | 模型请求超时秒数 |
| `OCR_BACKGROUND` | 否 | MR 标题、需求说明或发测说明 |
| `OCR_RULE` | 否 | 自定义规则文件路径 |

Token 只应通过 CI Secret 注入，禁止写入仓库、日志和报告。LLM 请求内容可能包含源代码，需根据企业数据分级决定使用公有模型、私有模型或内网网关。

## 4. 影响范围分析设计

### 4.1 影响范围不是单一列表

报告至少分成四层：

1. **变更范围**：实际修改的文件、模块、语言、增加/删除行数。
2. **静态依赖范围**：被修改类/函数/接口的调用方、实现方、继承方、配置引用方。
3. **运行时范围**：受影响服务、API、消息主题、数据库表/字段、缓存、定时任务、配置项。
4. **验证范围**：推荐执行的单元测试、集成测试、接口回归、数据校验、灰度观测指标。

### 4.2 确定性分析规则

CI 中新增一个 `impact-report` 步骤，至少执行：

```bash
git diff --name-status "$BASE_SHA" "$CI_COMMIT_SHA"
git diff --stat "$BASE_SHA" "$CI_COMMIT_SHA"
```

然后按仓库约定映射：

| 变更特征 | 影响范围推断 | 推荐验证 |
|---|---|---|
| `src/api/**`、Controller、Router | API/外部调用方 | API 回归、鉴权、参数兼容 |
| Service、Domain、核心业务模块 | 业务流程和下游调用方 | 单测、集成测试、关键链路回归 |
| DAO、Mapper、SQL、数据库迁移 | 数据库表/字段、数据一致性 | SQL 验证、兼容性、回滚演练 |
| 消息生产/消费代码 | Topic、消费者、重试/幂等 | 消息链路、重复消费、积压监控 |
| 配置中心、YAML、环境变量 | 部署环境和运行参数 | 配置校验、启动检查、差异核对 |
| Docker、Helm、K8s、CI 配置 | 部署、发布和流水线 | 构建、部署、回滚验证 |
| 权限、认证、加密、外部依赖 | 安全边界、凭据或第三方服务 | SAST、依赖扫描、安全回归 |
| 静态资源、前端路由 | 页面、浏览器端行为 | 前端构建、核心页面回归 |

### 4.3 依赖追踪建议

按技术栈选用可解释的工具，不把所有调用关系交给 LLM：

- Java：基于编译产物/IDE 索引、`jdeps` 或项目已有依赖分析工具。
- Go：`go list -deps`、`go test`、静态调用关系工具。
- TypeScript/JavaScript：TypeScript project references、模块导入图、构建产物分析。
- Python：导入图和测试收集器；动态调用必须标记为“无法静态确认”。
- 数据库：解析 migration、Mapper、SQL 中的表/字段。
- 配置：解析 Helm/K8s/YAML 中的 service、deployment、secret、configmap 引用。

对无法确定的关系，报告使用“疑似影响”而不是“确定影响”，并给出证据文件或规则。

### 4.4 影响范围报告字段

建议保存 `.ocr/impact.json`：

```json
{
  "base": "origin/main",
  "head": "<commit-sha>",
  "changed_files": [],
  "modules": [],
  "services": [],
  "apis": [],
  "events": [],
  "database_objects": [],
  "configurations": [],
  "deployment_units": [],
  "test_scope": [],
  "rollback_points": [],
  "confidence": "high|medium|low",
  "evidence": []
}
```

## 5. 审查报告设计

### 5.1 产物

每次 MR 审查在 `.ocr/` 下生成：

```text
.ocr/
├── ocr-result.json       # OpenCodeReview 原始结构化输出
├── impact.json           # 确定性影响范围分析
├── review-report.md      # 人可读综合报告
├── ocr-stderr.log        # OCR 运行日志/警告
├── ocr-stats.env         # 门禁和下游 Job 使用的统计变量
└── metadata.json         # MR、SHA、版本、模型、时间、基准信息
```

所有文件设置 `artifacts: when: always`，保留时间按企业审计要求配置，建议至少 1~4 周。

### 5.2 Markdown 报告模板

```markdown
# 发测前变更审查报告

## 结论
- 状态：通过 / 有条件通过 / 阻断 / 审查失败
- MR：!123
- 变更提交：<sha>
- 比对基线：origin/main
- 审查时间：<time>
- 审查工具/模型：OpenCodeReview <version> / <model>

## 变更概览
- 文件数：
- 新增/删除行：
- 变更模块：
- 变更类型：功能 / 缺陷 / 配置 / 数据库 / 部署 / 依赖 / 重构

## 影响范围
### 确定影响
- 服务：
- API：
- 数据库对象：
- 消息/任务：
- 配置/部署单元：

### 疑似影响
- 项目：
- 证据：
- 需要人工确认：

## 代码审查
| 等级 | 类别 | 文件:行 | 问题 | 修复建议 | 是否阻断 |
|---|---|---|---|---|---|
| high | security | src/...:42 | ... | ... | 是 |

## 验证建议
- [ ] 编译/构建
- [ ] 单元测试
- [ ] 集成测试
- [ ] 关键 API 回归
- [ ] 数据库兼容性/回滚验证
- [ ] 部署与健康检查
- [ ] 监控、日志、告警核对

## 风险与门禁
- critical：0
- high：0
- medium：0
- low：0
- 审查覆盖：complete / partial / failed
- 门禁原因：

## 原始制品
- ocr-result.json
- impact.json
- ocr-stderr.log
```

### 5.3 GitLab 展示方式

- **行级 Discussion**：只发布有准确 diff 行号且达到发布阈值的评论，优先展示 critical/high/medium。
- **Summary Note**：发布结论、问题数量、影响范围摘要、验证建议、未能定位到行的评论和警告。
- **Job Artifact**：保存完整 JSON、Markdown、影响范围和运行元数据。
- **Pipeline 状态**：由质量门禁 Job 显示“通过/阻断”，不要仅依赖 MR Note 的文字结论。

上游 GitLab 示例已经实现了：行级 Discussion、可更新的 sticky summary、失败降级到汇总 Note、评论去重、位置无法解析时的回退、限流重试和 `dotenv` 统计输出。建议直接复制并按本方案扩展，而不是重写发布逻辑。

## 6. 代码审查规则建议

在仓库提交 `.opencodereview/rule.json`，把企业发测关注点固化：

```json
{
  "exclude": [
    "**/generated/**",
    "**/vendor/**",
    "**/node_modules/**",
    "**/dist/**"
  ],
  "rules": [
    {
      "path": "**/controller/**/*.java",
      "rule": "重点检查鉴权、参数校验、越权、敏感信息泄露、异常处理、幂等性和接口兼容性。"
    },
    {
      "path": "**/*mapper*.xml",
      "rule": "重点检查 SQL 注入、参数绑定、分页、空值、索引使用、事务边界和数据库兼容性。"
    },
    {
      "path": "**/migration/**",
      "rule": "重点检查升级顺序、重复执行、向后兼容、数据量、锁表风险和回滚方案。"
    },
    {
      "path": "**/*.{yml,yaml,properties,json}",
      "rule": "重点检查配置覆盖关系、默认值、环境差异、敏感信息、连接超时和上线后生效范围。"
    },
    {
      "path": "**/Dockerfile",
      "rule": "重点检查基础镜像、权限、敏感信息、构建可复现性和运行时健康检查。"
    }
  ]
}
```

规则文件应按仓库真实目录调整，并通过 `ocr rules check <path>` 验证某文件最终命中的规则。

## 7. 门禁策略

### 7.1 推荐初始策略

| 条件 | 处理 |
|---|---|
| OCR 运行失败或 JSON 无法解析 | 阻断发测，允许人工豁免 |
| manifest 为 partial/failed，且关键文件未覆盖 | 阻断发测 |
| critical ≥ 1 | 阻断发测 |
| high ≥ 1 | 阻断发测或要求负责人确认 |
| medium/low | 不阻断，进入报告和 MR Note |
| 无问题且覆盖完整 | 允许发测 |

门禁不要只检查 `comments.length`；必须按 `severity`、`manifest`、`warnings` 和覆盖状态综合判断。

### 7.2 伪代码

```python
if review_status in {"failed", "partial"} and critical_files_uncovered:
    fail("审查覆盖不完整")
elif severity_count["critical"] > 0:
    fail("存在 critical 级问题")
elif severity_count["high"] > 0:
    fail("存在 high 级问题")
else:
    pass_pipeline()
```

AI 结果不应直接自动修改代码；门禁只负责提醒、阻断或放行，修复由开发者提交新 commit 后重新触发审查。

## 8. GitLab CI 实施骨架

以下是适合放入现有 `.gitlab-ci.yml` 的骨架。上游示例中的完整发布脚本应一并复制；这里仅展示关键职责：

```yaml
stages:
  - review
  - quality-gate
  - test
  - deploy-test

ocr-review:
  stage: review
  image: node:20
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  variables:
    GIT_DEPTH: "0"
  script:
    - npm install -g "@alibaba-group/open-code-review@${OCR_VERSION:-latest}"
    - ocr config set llm.url "$OCR_LLM_URL"
    - ocr config set llm.auth_token "$OCR_LLM_AUTH_TOKEN"
    - ocr config set llm.model "$OCR_LLM_MODEL"
    - ocr config set language "${OCR_LANGUAGE:-中文}"
    - mkdir -p .ocr
    - ocr review --from "origin/${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}" --to "$CI_COMMIT_SHA" --format json --audience agent > .ocr/ocr-result.json 2> .ocr/ocr-stderr.log
    - python3 ci/impact_report.py > .ocr/impact.json
    - python3 ci/build_review_report.py .ocr/ocr-result.json .ocr/impact.json > .ocr/review-report.md
    - python3 post_review.py .ocr/ocr-result.json
  artifacts:
    when: always
    expire_in: 4 weeks
    paths:
      - .ocr/
    reports:
      dotenv: .ocr/ocr-stats.env

quality-gate:
  stage: quality-gate
  needs:
    - job: ocr-review
      artifacts: true
  script:
    - python3 ci/review_gate.py .ocr/ocr-result.json .ocr/impact.json

release-test:
  stage: deploy-test
  needs: [quality-gate]
  script:
    - ./ci/deploy-test.sh
```

### 骨架使用注意事项

1. 生产配置中不要使用未定义的 `${OCR_VERSION:-latest}` 作为长期策略，建议固定版本变量。
2. 必须捕获 OCR 退出码，同时仍上传失败时的 stderr 和部分结果；上游示例已经实现了此行为。
3. `post_review.py` 失败不能掩盖 OCR 结果，发布脚本和门禁脚本应分别记录错误。
4. `quality-gate` 应使用 `needs` 获取审查制品，且发测 Job 必须依赖 `quality-gate`。
5. 现有项目若已有 `.gitlab-ci.yml`，建议用 `include:local` 引入模板，避免复制后长期漂移。

## 9. 部署步骤

### P0：验证可行性

1. 选择一个非关键仓库或测试分支。
2. 配置 LLM URL、Token、Model 和 GitLab API Token。
3. 复制上游 `examples/gitlab_ci/.gitlab-ci.yml`、`post_review.py`。
4. 固定 `OCR_VERSION`，设置 `OCR_LANGUAGE=中文`。
5. 创建包含一个明显缺陷和一个正常变更的 MR。
6. 验证 MR 行级评论、汇总 Note、JSON/Markdown Artifact 和失败日志。

### P1：接入发测门禁

1. 增加 `.opencodereview/rule.json`。
2. 增加确定性 `impact_report.py`。
3. 增加 `build_review_report.py`。
4. 增加 `review_gate.py`。
5. 将 `quality-gate` 接入测试部署 Job 的 `needs`。
6. 先以“只报告不阻断”运行 1~2 周，统计误报、漏报、耗时和模型成本。

### P2：正式启用

1. 启用 critical/high 阻断。
2. 配置人工豁免流程和审批人。
3. 设定报告保留期限和数据脱敏策略。
4. 监控 OCR Job 耗时、失败率、LLM token、评论数量和 API 限流。
5. 定期回顾规则、门禁阈值和影响范围映射。

## 10. 风险与控制

| 风险 | 控制措施 |
|---|---|
| AI 误报 | 先报告后阻断；critical/high 要求人工确认；保留原始证据 |
| AI 漏报 | 与编译、测试、SAST、依赖扫描并行，不把 OCR 当唯一质量门禁 |
| MR push 后审查错版本 | 使用 `CI_COMMIT_SHA`，结果写入 metadata |
| 大 MR 超时/成本高 | 文件过滤、并发限制、规则聚焦、token 预算、超时和恢复 |
| 行号定位失败 | 使用上游回退逻辑，不能定位的评论放到 Summary Note |
| 重复评论 | sticky summary、评论 ID、增量审查和幂等重试 |
| GitLab API 限流 | 指数退避、Retry-After、请求节流和重试上限 |
| 源码泄露 | 私有模型/企业网关、Masked Secret、最小权限、日志脱敏 |
| 自定义规则失效 | `ocr rules check`、CI 预览、规则变更评审 |
| 影响范围不完整 | 确定性扫描 + 证据链 + “疑似影响”标记 + 人工确认 |

## 11. 验收标准

### 功能验收

- MR 创建或更新后自动触发审查。
- 审查基于正确的目标分支 merge-base 到当前 SHA。
- MR 中出现行级 Discussion 和一条汇总 Note。
- `.ocr/ocr-result.json`、`.ocr/impact.json`、`.ocr/review-report.md`、`.ocr/ocr-stderr.log` 均可下载。
- 发测 Job 严格依赖质量门禁。
- 新 commit 重新审查后不产生重复机器人评论。

### 质量验收

- 报告明确区分确定影响和疑似影响。
- 每个影响结论有文件、规则或命令证据。
- critical/high/medium/low 统计互斥且可复核。
- OCR partial/failed 状态不会被误判为“无问题”。
- 行号无法解析时评论仍可在 Summary Note 中找到。

### 运维验收

- LLM Token 和 GitLab Token 不出现在日志和制品中。
- LLM、OCR、GitLab API 异常有明确失败原因。
- 支持固定 OCR 版本、限流重试和人工重跑。
- 报告可按 MR、SHA、Pipeline ID 追溯。

## 12. 参考资料

- OpenCodeReview 中文 README：<https://github.com/alibaba/open-code-review/blob/main/README.zh-CN.md>
- OpenCodeReview GitLab CI 示例：<https://github.com/alibaba/open-code-review/tree/main/examples/gitlab_ci>
- GitLab CI 配置示例：<https://raw.githubusercontent.com/alibaba/open-code-review/main/examples/gitlab_ci/.gitlab-ci.yml>
- GitLab 发布脚本：<https://raw.githubusercontent.com/alibaba/open-code-review/main/examples/gitlab_ci/post_review.py>
- OpenCodeReview 中文 CLI 参考：<https://github.com/alibaba/open-code-review/blob/main/pages/src/content/docs/zh/cli-reference.md>
- OpenCodeReview 中文评审规则：<https://github.com/alibaba/open-code-review/blob/main/pages/src/content/docs/zh/review-rules.md>
- GitLab Merge Request Discussions API：<https://docs.gitlab.com/api/discussions/>
- GitLab CI/CD Variables：<https://docs.gitlab.com/ci/variables/>

## 13. 如何确认当前分支的来源和变更

### 13.1 先确认当前分支及远程跟踪关系

在本地仓库执行：

```bash
git branch --show-current
git status --short --branch
git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}'
git remote -v
```

结果含义：

- `git branch --show-current`：当前分支名称。
- `git status --short --branch`：当前分支、是否落后/领先远程分支。
- `@{upstream}`：当前分支跟踪的远程分支，例如 `origin/main` 或 `origin/feature/login`。
- `git remote -v`：仓库远程地址；可以确认当前 clone 指向哪个 GitLab 项目。

如果没有设置 upstream，`git rev-parse ... '@{upstream}'` 会失败，这不影响后续使用明确的基线分支。

### 13.2 确认“从哪个基线分支分叉”

Git 通常不能保存“这个分支最初由哪个分支创建”的元数据。工程上应使用两个提交之间的共同祖先（merge-base）作为可复现的分叉点：

```bash
# 先同步远程引用，不修改工作区文件
git fetch origin --prune

# 查看远程默认分支，例如 origin/main 或 origin/master
git symbolic-ref --short refs/remotes/origin/HEAD

# 推荐：以 main 为基线，计算当前分支与 main 的共同祖先
git merge-base origin/main HEAD

# 如果分支曾经从 main 分叉后被 rebase，可尝试识别 reflog 中的原始分叉点
git merge-base --fork-point origin/main HEAD
```

`git merge-base origin/main HEAD` 输出的是基线提交 SHA。它表示“当前分支相对于 `origin/main` 的共同祖先”，不是绝对证明分支当初一定从 `main` 创建。若项目使用 `develop`、`release/*` 等发测基线，应把 `origin/main` 替换为实际目标分支。

可以对候选基线逐一比较：

```bash
git merge-base origin/main HEAD
git merge-base origin/develop HEAD
git merge-base origin/release/2026.08 HEAD
```

选择 GitLab MR 的 **Target branch** 作为最终基线，而不是根据提交时间猜测。MR 审查应固定记录：

```text
base_ref   = origin/<target-branch>
base_sha   = git merge-base base_ref HEAD
head_sha   = HEAD
```

### 13.3 查看当前分支新增的提交

```bash
# 查看基线之后当前分支独有的提交
git log --oneline --decorate --graph origin/main..HEAD

# 查看每个提交的作者、时间和说明
git log --format='%h %an %ad %s' --date=short origin/main..HEAD
```

`origin/main..HEAD` 的含义是“在 HEAD 中、但不在 origin/main 中的提交”。不要写成 `HEAD..origin/main`，后者查看的是基线独有提交。

### 13.4 查看变更了哪些文件和具体内容

```bash
# 文件级变更：A 新增、M 修改、D 删除、R 重命名
git diff --name-status origin/main...HEAD

# 统计每个文件新增/删除行数
git diff --numstat origin/main...HEAD

# 汇总统计
git diff --stat origin/main...HEAD

# 查看完整代码差异
git diff --find-renames origin/main...HEAD

# 仅查看某个文件
git diff origin/main...HEAD -- src/path/to/file.java
```

在 MR 场景中，`origin/main...HEAD` 是推荐写法：三点 diff 以共同祖先为起点，展示当前分支相对目标分支引入的变更。两点 `origin/main..HEAD` 主要用于提交集合查询；不要用 `git diff origin/main..HEAD` 代替 MR diff，尤其是基线包含合并提交时。

查看变更文件中的关键符号和上下文，可配合：

```bash
git diff --unified=80 origin/main...HEAD
git log -p origin/main..HEAD -- src/path/to/file.java
```

### 13.5 一条命令输出可归档的基线与变更清单

PowerShell：

```powershell
git fetch origin --prune
$baseRef = "origin/main"
$baseSha = git merge-base $baseRef HEAD
$headSha = git rev-parse HEAD
"base_ref=$baseRef"
"base_sha=$baseSha"
"head_sha=$headSha"
"=== commits ==="
git log --oneline "$baseSha..$headSha"
"=== files ==="
git diff --name-status "$baseSha" "$headSha"
"=== stat ==="
git diff --stat "$baseSha" "$headSha"
"=== diff ==="
git diff --find-renames "$baseSha" "$headSha"
```

### 13.6 GitLab CI 中的确定性写法

MR Pipeline 不应依赖 runner 当前 checkout 的分支名称。GitLab CI 中直接使用 MR 目标分支和当前提交 SHA：

```bash
git fetch origin --prune
BASE_REF="origin/${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}"
BASE_SHA="$(git merge-base "$BASE_REF" "$CI_COMMIT_SHA")"
HEAD_SHA="$CI_COMMIT_SHA"

git diff --name-status "$BASE_SHA" "$HEAD_SHA" > .ocr/changed-files.txt
git diff --stat "$BASE_SHA" "$HEAD_SHA" > .ocr/change-stat.txt
git diff --find-renames "$BASE_SHA" "$HEAD_SHA" > .ocr/change.diff

ocr review \
  --from "$BASE_REF" \
  --to "$HEAD_SHA" \
  --format json \
  --audience agent > .ocr/ocr-result.json
```

这样报告中的“来源”应写成：

```text
MR Target Branch: <CI_MERGE_REQUEST_TARGET_BRANCH_NAME>
Base SHA:         <BASE_SHA>
Review Head SHA:  <CI_COMMIT_SHA>
```

若要确认某次发测报告是否对应当前代码，比较报告中的 `head_sha`、GitLab Pipeline 的 `CI_COMMIT_SHA` 和 MR 当前 source commit；三者必须一致。
***
