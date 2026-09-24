# MQL 时间函数、日期区间字段与状态函数

目录：1 `RELATIVE_DATETIME_*` · 2 日期区间字段（date_range） · 3 `status_time`

## 1. 时间函数 `RELATIVE_DATETIME_*`

签名：`RELATIVE_DATETIME_{EQ|GT|GE|LT|LE|BETWEEN}(col_name, 'date_para', ['days'])`

date_para：`today` / `tomorrow` / `yesterday` / `current_week` / `next_week` / `last_week` / `current_month` / `next_month` / `last_month` / `future` / `past`。

### 1.1 函数 × date_para × days 兼容矩阵

不在允许列的组合服务端拒回 `unexpected operator for <para>` / `invalid argument`。

| 函数 | 推荐 date_para | days（`'Nd'` / `'-Nd'`） | 备注 |
|------|----------------|--------------------------|------|
| `_EQ` | `today` / `tomorrow` / `yesterday` | ❌ 不接受 | `_EQ + future/past` 无 offset 报 `invalid argument: relative datetime <para> expr must have 2 params`；带 offset 语义模糊，禁止生成，改用 `_BETWEEN` |
| `_GT` / `_GE` / `_LT` / `_LE` | 仅 `today` | ✅ 可正可负 | — |
| `_BETWEEN` | `current_week` / `next_week` / `last_week` / `current_month` / `next_month` / `last_month` / `future` / `past` | 仅 `future` / `past` 接受且必须传 `'Nd'`；其它不接受 | 拒回 `today` / `tomorrow` / `yesterday`（`unexpected operator for today`），改用 `_EQ` |

### 1.2 示例

```sql
RELATIVE_DATETIME_EQ(`start_time`, 'today')                  -- 今天创建
RELATIVE_DATETIME_BETWEEN(`start_time`, 'last_week')         -- 上周创建
RELATIVE_DATETIME_BETWEEN(`field_xxxxxx`, 'future', '3d')    -- 未来 3 天到期
RELATIVE_DATETIME_GT(`start_time`, 'today', '-3d')           -- 今天往前 3 天之后
RELATIVE_DATETIME_LT(`start_time`, 'today', '3d')            -- 今天往后 3 天之前
```

字段 key 必须先经 `workitem meta-fields` 确认（同时传 `--project-key` 与 `--work-item-type`），禁复用示例 key。

## 2. 日期区间字段（date_range）

工作项级日期区间字段（自定义「计划周期」等，字段类型 `schedule` / `precise_date`）不能直接以字段 key 查询，必须拆成派生访问形式：`` `__<字段key>_开始时间` `` / `` `__<字段key>_结束时间` ``。这不是复合字段子字段概念。

```sql
✅ WHERE `__field_xxxxxx_开始时间` > '2025-01-01'
✅ WHERE RELATIVE_DATETIME_BETWEEN(`__field_xxxxxx_结束时间`, 'past', '30d')
❌ WHERE RELATIVE_DATETIME_BETWEEN(`field_xxxxxx`, 'past', '30d')
```

节点排期与此不同：用 `get_node_attribute(节点, '__排期_开始时间')` 访问，见 `mql-nodes-relations.md`。

## 3. 状态函数 `status_time`

对状态流（如 `issue`）和节点流（如 `story`）工作项均有效。状态名不存在报 `metadata error`；
状态名以 `workflow list-state-transitions` 或 `workitem meta-fields --field-keys '["work_item_status"]'` 返回的真实 option 为准。

| 用法 | 允许位置 | 参数形式 |
|------|---------|---------|
| `status_time('状态名')` | WHERE / SELECT | 纯状态名，不带 `__` 前缀或 `_开始时间` / `_结束时间` 后缀 |
| `status_time('__状态名_开始时间')` / `_结束时间` | 仅 SELECT 或时间差表达式 | WHERE 中使用会报错 |

```sql
-- ✅ WHERE 用纯状态名过滤（日期区间）
WHERE status_time('<状态名>') between '2025-01-01' and '2025-12-31'
-- ✅ SELECT 计算状态累计时长
SELECT `work_item_id`, status_time('__<状态名>_结束时间') - status_time('__<状态名>_开始时间') FROM `project_key`.`issue`
-- ❌ WHERE 里做算术过滤
WHERE status_time('__<状态名>_结束时间') - status_time('__<状态名>_开始时间') > 86400
```
