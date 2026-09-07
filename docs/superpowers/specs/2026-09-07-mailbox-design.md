# class-mid 邮箱功能设计

状态：已由负责人确认并实施。

## 目标

在现有单进程、JSON、MySQL 事务架构上增加最小邮箱功能。中期版本固定为 4 块田，只种胡萝卜；其他购买、施肥、成熟、收获、出售和任务功能保持不变。不引入 Actor。

## 数据与事务

继续使用已有 `classicfarm` 数据库，不删除或重建数据库。保留 `class_mid_accounts` 和 `class_mid_states`，新增：

```sql
CREATE TABLE IF NOT EXISTS class_mid_mails (
    mail_id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    player_id VARCHAR(32) NOT NULL,
    title VARCHAR(100) NOT NULL,
    content TEXT NOT NULL,
    is_read BOOLEAN NOT NULL DEFAULT FALSE,
    created_at_ms BIGINT NOT NULL,
    PRIMARY KEY (mail_id),
    INDEX idx_class_mid_mails_player (player_id, mail_id),
    CONSTRAINT fk_class_mid_mails_player
        FOREIGN KEY (player_id) REFERENCES class_mid_accounts(player_id)
        ON DELETE CASCADE
) ENGINE=InnoDB;
```

新玩家注册在一个事务中依次写入账号、初始农场状态和欢迎邮件，任一步失败则全部回滚。欢迎邮件标题为“欢迎来到经典农场”，正文为“欢迎来到经典农场！快去种下你的第一颗胡萝卜吧。”。已有账号不自动补发，避免启动时修改历史数据。

内存演示模式也保存邮件，但服务退出后与其他内存数据一起丢失。

## 操作链路

继续使用当前 WebSocket 身份绑定。客户端不提交 `player_id`。

- `GET_MAILBOX`：用户打开邮箱时发送；存储层按已认证玩家查询 MySQL，按 `mail_id DESC` 返回该玩家的所有邮件及正文。
- `READ_MAIL`：点击某封邮件时发送 `mail_id`；SQL 同时匹配 `mail_id` 和当前 `player_id`，标记为已读，然后返回最新邮箱列表。
- 不做附件、领取、删除、过期、分页、后台群发和未读推送。

邮件不放入 `class_mid_states.state_json`，避免一次游戏操作重写全部邮件。游戏写操作仍通过 `SELECT state_json ... FOR UPDATE` 串行化同一玩家请求；邮件标记已读用独立 SQL 更新。

## JSON

`Command.data` 增加字符串形式的 `mail_id`。`Response` 增加 `mails`：

```json
{
  "type": "response",
  "request_id": "mailbox-0001",
  "action": "GET_MAILBOX",
  "code": "OK",
  "server_time_ms": 1800000000000,
  "mails": [
    {
      "mail_id": "1",
      "title": "欢迎来到经典农场",
      "content": "欢迎来到经典农场！快去种下你的第一颗胡萝卜吧。",
      "is_read": false,
      "created_at_ms": 1800000000000
    }
  ]
}
```

`mail_id` 使用十进制字符串，避免 Vue/JavaScript 的 64 位整数精度问题，Qt 可解析为 `qulonglong`。非法编号返回 `INVALID_ARGUMENT`；编号不存在或不属于当前玩家返回 `MAIL_NOT_FOUND`，不泄露其他玩家邮件是否存在。

## 界面

顶部增加“邮箱”按钮和未读数。点击后立即发送 `GET_MAILBOX`，打开侧边或弹层面板并显示标题、时间、已读状态和正文。点击未读邮件发送 `READ_MAIL`；成功后用服务器返回列表更新页面。不在登录响应或玩家快照中夹带邮件。

## 中文注释

在事务边界、玩家权限条件、MySQL 行锁、请求去重和 WebSocket 身份绑定处写简短中文注释。结构体字段和显而易见的赋值不逐行注释，以免降低可读性。

## 验证

- 新注册同时产生账号、初始状态和一封欢迎邮件；欢迎邮件写入失败时注册回滚。
- 两名玩家只能查询、标记自己的邮件。
- 非法或不存在的 mail_id 返回稳定错误。
- 原有四块田和胡萝卜完整流程继续通过。
- Vue 点击邮箱会实时查询，点击邮件更新已读状态；生产构建通过。
- 真实 MySQL 测试只创建并清理独立测试账号，不清空数据库或旧表。
