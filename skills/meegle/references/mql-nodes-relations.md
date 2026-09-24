# MQL 节点函数与关系函数

目录：1 节点函数 · 2 `get_node_attribute` · 3 控件函数多值语义 · 4 `__BELONGING` 服务端限制 · 5 关系判断函数 · 6 关系参数与跨端字段 · 7 其它关系函数

## 1. 节点函数

| 函数 | 用途 |
|------|------|
| `all_nodes_name()` | 全部节点名数组 |
| `in_progress_nodes_name()` | 进行中节点名数组 |
| `risk_label()` | 节点延期状态标识数组，不支持 `any_match` |
| `get_node_attribute(node, attribute)` | 指定节点属性；`node` 可为节点名 / `__ALL` / `__BELONGING`；「所属节点」必须用 `__BELONGING` |

节点名以 `workflow get-node` 返回为准（`--node-id-list '["_all"]'`，同时传 `--project-key` 与 `--work-item-id`）。

## 2. `get_node_attribute` 属性访问

可用属性：排期、估分、节点时间、节点完成结论、节点完成意见、负责人、当前负责人、状态。指定节点负责人时 `owner` 与 `负责人` 等价。

开始 / 结束时间的第二参数形式：节点排期 `'__排期_开始时间'` / `'__排期_结束时间'`；节点时间 `'__节点时间_开始时间'` / `'__节点时间_结束时间'`。

`__ALL` 简写：对整体属性直接用区间 / 比较，禁止拆 `__排期_开始时间` / `__排期_结束时间`，禁止外套 `any_match`；`排期` 支持 `BETWEEN`，`估分` 不支持 `BETWEEN`（拆 `>= a AND <= b`）。

```sql
WHERE array_contains(get_node_attribute('需求详评','owner'), '<id:userkey>')                    -- 指定节点负责人
WHERE RELATIVE_DATETIME_BETWEEN(get_node_attribute('开始','__排期_开始时间'), 'past','30d')      -- 开始节点排期在过去 30 天
WHERE array_contains(get_node_attribute('__BELONGING','当前负责人'), '<id:userkey>')            -- 所属节点当前负责人
WHERE get_node_attribute('__ALL','排期') between '2026-01-01' and '2026-03-31'                  -- 全部节点排期在区间内
WHERE get_node_attribute('__ALL','估分') >= 30                                                  -- 全部节点估分 ≥ 30
```

## 3. 控件函数多值语义

`all_nodes_name()` / `in_progress_nodes_name()`（不能用 `IN`，只能走 `any_match`）：

| 语义 | 写法 |
|------|------|
| 存在选项属于（OR） | `any_match(<控件>, x -> x IN ('a','b'))` |
| 包含（AND） | `array_contains(<控件>, 'a','b')` |
| 集合完全相等 | `<控件> = '["a","b"]'`（JSON 数组，例外） |
| 全部不属于 | `none_match(<控件>, x -> x IN ('a','b'))` |
| 不同时包含（至少缺一个） | `NOT array_contains(<控件>, 'a','b')` |

`risk_label()`（不支持 `any_match`）：有交集（默认「含延期节点」）`array_intersect(risk_label(), '["延期/前端","延期/后端"]')`；全部包含用多个 `array_contains(risk_label(), 'x')` AND；
父级「延期」/「排期信息不全」用 `array_contains(risk_label(),'延期')`；开始节点已延期 `risk_label() = '["延期/开始"]'`（集合完全相等，JSON 数组例外）。

## 4. `__BELONGING` 服务端限制（权威定义）

`get_node_attribute('__BELONGING','状态')` 一旦被 `array_contains` 包裹（单条件即触发），或任意 `__BELONGING` 属性之间做 AND 组合（如「当前负责人 + 状态」），会触发 nil pointer / panic。
修复：状态类必须用 `=` 顶层比较；负责人等多值属性必须保持单条件；需要组合时改用 `__ALL` 属性，或用状态字段 `work_item_status` + `current_status_operator` 拼接。

```sql
WHERE get_node_attribute('__BELONGING','状态') = '<进行中状态label>'   -- ✅ 顶层 =，返回空 list 无 error
```

## 5. 关系判断函数

对关系对端做条件判断：`any_relation_match(rel, x -> expr)` 存在一个对端满足；`all_relation_match` 每一个都满足；`none_relation_match` 每一个都不满足；`not all_relation_match` 至少一个不满足。
`relation_field_chain` 等取关系后必须外层套关系判断函数。

## 6. 关系参数与跨端字段

关系参数三种形式：1) 关联字段 `` `字段key` ``；2) `relation('关系名')`；3) `relation_field_chain('rel1', 'rel2' [, 'rel3'])`（≤ 3 跳）。子任务父工作项的关系名固定 `'__父工作项'`（双下划线前缀）。

对端字段引用：`` x.`字段名<target:project_key::type_key>` `` 或 `` x.`字段名<target:all>` ``。通用字段必须用 `<target:all>`：标题、创建人、创建时间、业务线、优先级、当前负责人、所属工作项、所属空间、工作项 ID、工作项类型、状态。

```sql
WHERE any_relation_match(`多选关联字段`, x -> x.`priority<target:all>` = 'P0')                                   -- 对端优先级 P0
WHERE any_relation_match(relation_field_chain('__父工作项'), x -> x.`name<target:all>` like '%登录%')             -- 父工作项名称
WHERE all_relation_match(relation_field_chain('__父工作项','需求关联软件'), x -> array_contains(get_node_attribute('开始','负责人'), '<id:userkey>'))
```

## 7. 其它关系函数

- `parent_work_item(relation('关系名'))`：父工作项 ID，仅支持 SELECT，WHERE 报 `parent_work_item() not supported in stage Where`；
  按父工作项过滤改用 `` any_relation_match(relation_field_chain('__父工作项'), x -> x.`work_item_id<target:all>` = '12345') ``。
- `association()`：跨空间关联实例 ID，`WHERE association() = '实例ID'`。
- `linked_work_item()`：子任务来源控件（父工作项 ID）。判空推荐 `IS NOT NULL`；等值右值必须是真实父工作项 ID，否则 `attribute_value not found`。
