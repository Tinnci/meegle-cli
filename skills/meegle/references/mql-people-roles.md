# MQL 人员函数、名称消歧与角色列

目录：1 人员与团队函数 · 2 名称消歧 `<id:xxxx>` · 3 角色列

## 1. 人员与团队函数

| 函数 | 返回 |
|------|------|
| `current_login_user()` | 当前登录用户 userkey |
| `team(include_manager, '团队名')` | 团队成员 userkey 数组；首参 `true` 含管理者 |
| `all_participate_persons()` | 全部参与人 userkey 数组 |
| `participate_persons()` | 当前参与人 userkey 数组 |
| `participate_roles()` | 参与角色的 `role_name` label 数组 |

约束：

- 团队名必须先用 `team list`（传 `--project-key`）查询，否则报 `attribute_value not found`。
- `participate_roles()` 值位必须用 `role_name` label（`'后端开发'`），禁传 `role_id`（`'fe_rd'`）。
- 人员字段值位必须用 `<id:userkey>`（见 §2）。`current_login_user()` 是 MQL 内置函数字面量，`user search` 不会解析它。

```sql
array_contains(`current_status_operator`, current_login_user())            -- 当前负责人是我
any_match(`current_status_operator`, x -> x IN (team(true, '真实团队名')))   -- 指派给某团队（含管理者）
array_contains(participate_roles(), '后端开发', '前端开发')                   -- 有指定角色参与
```

## 2. 名称消歧 `<id:xxxx>`

### 2.1 人员字段值位

人员字段、user、multi-user、自定义人员控件、角色列的值位必须优先用 `<id:userkey>`：

- 裸 userkey（`= 'example_userkey'`）在部分场景被当姓名解析，报 `user label '...' does not exist`。
- 裸中文姓名（`= '张三'`）常报 `user label '...' is not unique`（即便 `user search` 唯一，服务端仍全局校验）。

流程：`user search` 拿 userkey → 值位写 `<id:userkey>`。

```sql
WHERE `owner` = '<id:userkey>'                                   -- 单人字段
WHERE array_contains(`current_status_operator`, '<id:userkey>')  -- 多人字段：= 或 array_contains 均可
WHERE `current_status_operator` IN ('<id:k1>', '<id:k2>')        -- 多人 OR 首选 IN
WHERE `current_status_operator` = current_login_user()
```

### 2.2 团队 / 枚举消歧

同名重复时用 `<id:xxxx>`：

```sql
WHERE any_match(`current_status_operator`, x -> x IN (team(true, '开放平台团队<id:3455>')))
WHERE `priority` = '<id:option_2>'
```

## 3. 角色列

角色不是字段，在 MQL 中作为列引用。

| 写法 | 使用场景 | 示例 |
|------|---------|------|
| 主写法 `` `__<role_name>` `` | 默认。`role_name` 严格取自 `workitem meta-roles`，含空格原样保留 | `` `__后端开发` ``、`` `__经办人` `` |
| fallback `` `__role_<project_key>_<work_item_type>_<role_id>` `` | 仅当 `role_name` 与其它字段 / 角色中文名冲突时 | `` `__role_<project_key>_story_<role_id>` `` |

- 硬性前置：涉及角色的 MQL 必须先 `workitem meta-roles`（同时传 `--project-key` 与 `--work-item-type`）取真实 `role_name` / `role_id`；禁止按用户口语、缩写、错别字拼列名。
- 系统默认角色：`role_name` 经办人 / 报告人；`role_id` `operator` / `reporter`（后者只用于 `role_operate` 参数，禁出现在 MQL 列名或函数里）。
- 无效形式（Code 3010 `attr label not found`）：`` `__<role_id>` ``（`__fe_rd`、`__operator`）、`` `<role_id>` ``（无 `__`）、函数包装 `role(fe_rd)` / `get_role_owners(fe_rd)`。

```sql
WHERE array_contains(`__后端开发`, '<id:userkey>')                          -- ✅ 主写法
WHERE array_contains(`__role_<project_key>_story_<role_id>`, '<id:userkey>')  -- ✅ fallback
WHERE `经办人` = '<id:userkey>'                                             -- ❌ 缺 __ / 中文原文
WHERE array_contains(`__operator`, '<id:userkey>')                          -- ❌ 用了 role_id
```
