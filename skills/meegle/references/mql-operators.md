# MQL 运算符兼容性、数组函数与 Lambda

目录：1 运算符 × 字段类型兼容表 · 2 数组字段语义与树状匹配 · 3 数组与集合函数 · 4 多选字段决策顺序 · 5 Lambda 限制

## 1. 运算符 × 字段类型兼容表（唯一权威表）

违反本表报 `field not supported OPERATOR` / `operator not supported`。

| 字段类型 | 支持 | 不支持 |
|---------|------|--------|
| text / multi-pure-text | `=` `!=` `IN` `NOT IN` `LIKE` `NOT LIKE` `IS NULL` `IS NOT NULL` | 比较符、`BETWEEN`、`array_contains` |
| multi-text | `LIKE` `NOT LIKE` `IS NULL` `IS NOT NULL` | 其它 |
| select / radio | `=` `!=` `IN` `NOT IN` `IS NULL` `IS NOT NULL` | `LIKE`、比较符、`BETWEEN`、`array_contains`、`any_match` |
| workitem_related_select / workitem_related_multi_select | `=` `!=` `IN` `NOT IN` `IS NULL` `IS NOT NULL`，右值必须是对端**精确全名**或 `'<id:work_item_id>'` | `LIKE`、比较符、`array_contains`、`any_match`；用户给的是简称或模糊名时**不要用等值**，改 `any_relation_match`，见 [mql-nodes-relations.md §5](mql-nodes-relations.md) |
| tree-select（单选） | `=` `!=` `IN` `NOT IN` `IS NULL` `IS NOT NULL`（仅叶子精确匹配） | `LIKE`、比较符、`BETWEEN`、`array_contains`、`any_match`、父级级联 |
| user | `=` `!=` `IN` `NOT IN` `array_contains` `IS NULL` `IS NOT NULL` | `LIKE`、比较符、`BETWEEN` |
| link | `=` `!=` `LIKE` `NOT LIKE` `IS NULL` `IS NOT NULL` | `IN`、比较符、`BETWEEN`、`array_contains` |
| signal | `=` `!=` `IN` `NOT IN`（值位见下） | `IS NULL` / `IS NOT NULL`（报 `operator not supported`）、其它 |
| number（含 `work_item_id`） | `=` `!=` `IN` `NOT IN` `>` `>=` `<` `<=` `IS NULL` `IS NOT NULL` | `LIKE`、`BETWEEN`、`array_contains` |
| array(varchar) | `=` `!=` `IN` `NOT IN` `array_contains` `NOT array_contains` `any_match` `none_match` `IS NULL` `IS NOT NULL` | `LIKE`、比较符、`BETWEEN` |
| date | `=` `!=` `>` `>=` `<` `<=` `BETWEEN` `RELATIVE_DATETIME_*` `IS NULL` `IS NOT NULL` | `LIKE`、`IN`、`array_contains` |
| datetime（`schedule` / `precise_date`） | `>` `>=` `<` `<=` `BETWEEN` `RELATIVE_DATETIME_*` `IS NULL` `IS NOT NULL`，须先拆为 `` `__字段key_开始时间` `` / `` `__字段key_结束时间` `` | `=` `!=`、`LIKE`、`IN`、`array_contains` |
| bool | `=` `!=` `IS NULL` `IS NOT NULL` | `IN`、`LIKE`、`BETWEEN`、`array_contains`、比较符 |
| array(struct)（`compound_field`） | 不可直接在 WHERE 比较 | 需通过子字段或 `workitem get` 读取 |
| `multi-file` | `SELECT` / `IS NULL` / `IS NOT NULL` | `LIKE`、比较符、`BETWEEN`、`array_contains`、`IN` |

值位补充：

- signal 查询值位仅接受 `option_name` label（`'已通过'` / `'未通过'` / `'处理中'` / `'暂无信息'`）；禁 `option_id`（`'passed'`）、`<id:option_id>`、`'true'/'false'/'null'`。
  写入接口的值位规则相反（写 `option_id`），查询侧不要照搬。
- `workitem_related_select` 值位：`= '<id:work_item_id>'` 或对端**精确全名**；裸 `work_item_id` 拒回 `attrValueLabel not found`。
  用户只给了简称或名称片段时，等值必报 `attrValueLabel not found`，不要重猜名字，一律改写成关系判断：

  ```sql
  WHERE any_relation_match(`<关联字段key>`, x -> x.`name<target:all>` LIKE '%<名称片段>%')
  ```

  首参传 `workitem meta-fields` 返回的关联字段 key；对端字段引用形式（`<target:all>` 与 `<target:project_key::type_key>` 各自的适用范围）见 [mql-nodes-relations.md §5–6](mql-nodes-relations.md)。
- `tree-multi-select` / `workitem_related_multi_select` 右值仅接受 label 或 `<id:option_id>` / `<id:work_item_id>` 包裹形式；裸 id 拒回 `metadata error`。

关联工作项字段优先遵循上表的专用限制，不套用下文通用数组函数规则。

## 2. 数组字段语义与树状匹配

- `=` / `!=` 按整组精确匹配；`IN` 按元素级 OR；`array_contains` 多参数为 AND（同时包含）；`any_match` 逐元素匹配。
- 树状 / 级联字段父级匹配（含所有下级）：`tree-multi-select`（数组型）用 `any_match(field, x -> x = '<父级 label>')` 或 `` `field` IN ('<父级 label>') ``；
  `array_contains` 仅精确 label 匹配、不含子级级联。`tree-select`（单选）不支持父级级联，仅按叶子 label / option_id 精确匹配。禁止写完整路径 `'<父级>/<叶子>'`。
- 业务线等树状字段先 `workitem meta-fields --field-keys '["business"]'`（或 `--field-query 业务线`）确认叶子 option_id 与 label。

## 3. 数组与集合函数

| 函数 | 说明 |
|------|------|
| `array_contains(field, e1 [,e2,...])` | 数组包含元素；多参数为 AND。右值形式按 §1 值位补充 |
| `any_match(field, x -> pred)` | 任一元素满足；仅当值列表含函数（`team()` / `current_login_user()`）或多个 `<id:userkey>` 时使用 |
| `none_match(field, x -> pred)` | 全部元素都不满足 |
| `array_intersect(field, '[...]')` | 有交集；仅用于控件函数返回值（`risk_label()` 等），普通多选字段报 `array_intersect: invalid right_array`；第二参数必须是 JSON 数组字符串 |

## 4. 多选 / 数组字段决策顺序

1. 单元素或多元素 OR → `IN + 元组`
2. 多元素 AND（同时包含）→ `array_contains(field, 'a', 'b')`
3. 集合完全相等 → `` `field` = ('v1','v2') ``（元组，禁 JSON 数组字符串）
4. 全不属于 → `NOT IN` 或 `none_match`；`NOT array_contains` 是「不同时包含」（至少缺一个），不是「全不属于」
5. 树状父级匹配 → 见 §2
6. 值列表含 `team()` / `current_login_user()`，或要在集合内判断多个 `<id:userkey>` → `any_match(x -> x IN (...))`

```sql
array_contains(`current_status_operator`, '<id:userkey>')        -- 单人命中（多人字段）
`current_status_operator` IN ('<id:k1>', '<id:k2>')              -- 多人 OR
array_contains(`tag`, '标签A', '标签B')                            -- 多标签 AND
`tag` IS NULL                                                     -- 空判断
any_match(`watchers`, x -> x IN (team(true, '真实团队名')))         -- 值列表含团队函数
```

## 5. Lambda 限制

数组匹配函数 `any_match` / `none_match` 的 `x -> ...` 内部服务端只受理下列极简条件，其余拒回 `lambda predicate operator not supported: <OP>` / `unsupported lambda predicate`。

| ✅ 支持 | ❌ 不支持 |
|--------|----------|
| `x = 'value'` | `x != 'a'`、`x NOT IN (...)` |
| `x IN ('a','b',...)`（值可含 `team(...)` / `current_login_user()` / `<id:userkey>`） | `> >= < <=`、`LIKE`、`BETWEEN`、`IS NULL` |
| 同变量 OR：`x = 'a' OR x = 'b'`（推荐改 `IN`） | `RELATIVE_DATETIME_*(x,...)`、`AND` 复合、嵌套 `any_match` / `none_match` |

决策：多选 / 数组字段优先顶层 `IN` / `NOT IN` / `array_contains` / `none_match`；`any_match` 第二参数必须是 `x -> ...` lambda，不接受数组字面量。
