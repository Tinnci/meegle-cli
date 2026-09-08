# 附件域

## 核心边界

工作项附件有三个独立阶段：**上传文件到对象存储 → 把文件对象写入目标字段 → 回读目标字段验收**。上传成功不等于字段写入成功；只有三步都完成，才可以向用户宣称附件已写入。

附件上传/下载底层分两步：先调 `attachment prepare-upload` / `attachment prepare-download` 申请带签名的对象存储 URL，再与对象存储做一次或多次 HTTP 直连。

`attachment prepare-upload` 的返回结果可能包含 `target_unit_code`；自行对对象存储发起上传时，若该值非空，必须在每次 POST（包括所有上传分片）添加 `x-target-unit`，值原样使用该 `target_unit_code`；缺失或为空时不添加该请求头。`attachment prepare-download` 的返回结果同样可能包含 `target_unit_code`；自行下载时，若该值非空，必须在每次 GET（包括所有下载分片）添加同样的 `x-target-unit`，值原样使用该 `target_unit_code`；缺失或为空时不添加该请求头。

Meegle CLI 内置 `attachment +upload` / `attachment +download` 一键封装，把两步合成一条命令；脚本里需要逐步控制时也可单独调上面的 prepare 命令。
## 写入前门禁

### 1. 校验本地文件

- 对用户点名的每个路径逐一确认：文件存在、是普通文件、可读，且 basename 与用户要求一致。
- 本地文件不存在时立即停止并请用户补充或确认。**禁止自动替换成同名的其他文件，禁止擅自替换文件扩展名**（例如请求 `ad.png` 时改传 `ad.mp4`）。

### 2. 目标字段消歧

每次都必须针对**当前空间 + 当前工作项类型**调用 `workitem meta-fields`，使用 `field_types=["file","multi-file"]`、`page_num=1` 实时查询附件字段；若返回 `pagination.has_more=true`，继续递增页码，直到收集完所有页后再消歧。然后按以下顺序确定目标：

1. 用户明确给出字段 key：只接受精确 key 匹配。
2. 用户明确给出字段名：只接受精确字段名匹配；模糊结果只能用于展示候选。字段名精确匹配后仍有多个候选时，列出每个候选的 key 和类型，让用户按 key 选择。
3. 用户只说“附件”：若仅有一个候选可直接使用；若有多个候选，列出字段名、key、类型并让用户选择。

禁止选择第一个候选，禁止复用历史会话中的字段，禁止把 `attachment` 或 `multi_attachment` 当作固定 key。执行前记录 `target_field_key`、字段名和 `field_type`，后续上传、写入与验收必须始终使用同一个 key。

元数据中存在某字段，只能证明 API 可以识别它，**不能证明该字段已放入用户当前详情页布局或在 UI 可见**。没有布局查询能力时，应明确把结论限定为“API 目标字段写入并回读成功”。

### 3. 区分字段协议

`file` 与 `multi-file` 不是可无条件互换的协议：

- `multi-file` 使用 stringified 文件对象数组，支持按下文协议维护多个附件。
- `file` 可能是旧版或单附件字段；只有当前空间的实际写入与回读已经验证其行为时，才可按其已验证能力操作。不得仅凭字段名或历史经验宣称它支持多附件。
- `workitem get` 回读的文件对象可能比上传结果多出 `uid`、`url` 或其他服务端字段。后续合并时必须原样保留这些未知字段，不能把旧对象降级为只有 `name/type/size/fileToken` 的简化对象。

## 端到端写入协议

上传成功后，把返回值映射为字段对象：

```json
{"name":"a.pdf","type":"application/pdf","size":"12345","fileToken":"<token>"}
```

其中 `type` 来自上传结果的 `mime_type`，`fileToken` 来自 `file_token`，`size` 写成十进制字符串；整个数组还要再次 JSON.stringify 后作为 `field_value`。

### 单附件

1. 通过已消歧的 `target_field_key` 上传文件，获得真实 `file_token`。
2. 创建工作项、目标字段当前为空，或用户明确要求替换时，可调 `workitem create` / `workitem update` 直接写入单元素数组。
3. 向已有 `multi-file` 字段**新增一个附件**时也必须先调 `workitem get`：记录基线数量，原样保留当前完整数组，追加新对象后再调 `workitem update`。不得因为这次只新增一个文件就覆盖旧数组。`file` 字段仍按上面的兼容性规则处理。
4. 立即调 `workitem get` 回读**同一个目标字段**，按文件名和数量做写后验收。追加语义下预期数量为“基线数量 + 1”；不能只检查上传返回值，也不能回读另一个附件字段。

### 同一 `multi-file` 字段写入多个附件

可靠路径是**单附件落库后逐个追加**，不要把“创建时一次传入多个简化对象”作为默认协议：

1. 创建场景先上传并随 `workitem create` 写入第一个附件，拿到工作项 ID；剩余附件改用该 ID 逐个处理。已有工作项直接从下一步开始。
2. 每追加一个文件前，先调 `workitem get` 读取 `target_field_key` 的当前完整数组。
3. 上传一个新文件；将当前数组中的旧对象**原样保留**（包括 `uid`、`url` 及未知字段），只在数组末尾加入这一个新文件对象。
4. stringify 整个新数组并调 `workitem update` 覆盖目标字段；随后再次回读确认这一轮新增文件已出现。
5. 对剩余文件重复 2–4。最终比较用户请求的文件名多重集和数量：创建语义下等于请求总数，追加语义下等于“基线数量 + 成功新增数”；token 可能被服务端重签，不能把 token 完全相等作为唯一验收条件。

如果一次写入多个简化对象触发 `unique` 校验，直接切换到上述逐个追加协议；不得删除已成功落库的附件后盲目重试。更新是覆盖语义，不先读取并保留旧数组会导致已有附件丢失。

## 写后验收清单

- 回读的是已消歧并实际写入的 `target_field_key`。
- 单附件：目标文件名存在，目标字段数量符合预期。
- 多附件：请求的每个文件名都存在，重复文件按用户意图计数，总数符合预期。
- API 验收通过但无法验证页面布局时，如实说明 UI 可见性未验证，不把它包装成 UI 成功。

## attachment prepare-upload
申请上传签名。`work_item_id` 与 `work_item_type` **二选一必填**：已有工作项传 `work_item_id`；"创建工作项时同步上传附件" 场景传 `work_item_type`，两者同传时 `work_item_id` 优先。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| --project-key | string | 是 | 空间 key |
| --resource-type | number | 是 | 附件场景：13=评论附件 / 14=评论图片 / 15=工作项附件字段 / 16=富文本字段图片 |
| --file-name | string | 是 | 附件名称 |
| --mime-type | string | 是 | MIME 类型 |
| --size | number | 是 | 文件总大小（字节）；后端据此判断走单次上传还是分片 |
| --work-item-id | string | 二选一 | 已有工作项 ID |
| --work-item-type | string | 二选一 | 工作项类型（仅 "创建工作项同步上传附件" 场景） |
| --field-key | string | 条件 | `resource_type=15/16` 必填，13/14 不填 |

## attachment prepare-download
申请下载签名。`file_url` 是其它命令（如 `workitem get` 的附件字段值、`comment list` 评论里的附件链接、富文本中的附件引用）回传的不透明引用，**不要**手工拼接。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| --project-key | string | 是 | 空间 key |
| --work-item-id | string | 是 | 工作项 ID |
| --file-url | string | 是 | 附件 URL（来自附件字段、评论或富文本） |

## attachment +upload
端到端上传：CLI 在本地把 `attachment prepare-upload` 与对象存储的签名 HTTP POST 串起来，返回 `file_token` 与文件元数据，可直接喂给 `workitem create` / `workitem update` / `comment add` 的附件字段。**Meegle CLI 专用**。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `<source-path>`（位置参数） | string | 是 | 本地文件路径 |
| --resource-type | string | 是 | 13/14/15/16，含义同 `attachment prepare-upload` |
| --project-key | string | 是 | 空间 key |
| --work-item-id | string | 二选一 | 已有工作项 ID |
| --work-item-type | string | 二选一 | 创建场景的工作项类型 |
| --field-key | string | 条件 | resource_type=15/16 时必填 |
| --filename | string | 否 | 覆盖发送给后端的文件名（默认取本地 basename） |
| --content-type | string | 否 | 覆盖 MIME 类型（默认按扩展名探测，未识别走 `application/octet-stream`） |

## attachment +download
端到端下载：CLI 在本地把 `attachment prepare-download` 与对象存储的签名 HTTP GET 串起来，并用 `.partial` 临时文件 + 原子改名落盘，失败时不会留下半残文件。**Meegle CLI 专用**。

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `<file-url>`（位置参数） | string | 是 | 附件 URL（来自附件字段、评论或富文本） |
| --project-key | string | 是 | 空间 key |
| --work-item-id | string | 是 | 工作项 ID |
| --output | string | 是 | 本地落地路径 |
| overwrite | bool | 否 | 目标已存在时是否覆盖（默认 false） |
