# V0.9：网页一键安装（软件更新第二阶段，mac 验证）

设备 `172.20.31.6` 2026-11-19 SSH 超时，本目录只有 mac 上的验证。root 侧的真实安装（systemd timer、`install.sh`、重启、回滚）**没有实机跑过**。

## 做了什么

1. **签名互通**：用临时 ed25519 密钥，`openssl pkeyutl -sign -rawin`（OpenSSL 3.6.3）签出 64 字节签名，Go `crypto/ed25519` 验证通过，篡改一个字节后验证失败。
2. **单元测试**（`internal/update`、`internal/app`，`make test` 全绿）：
   - 主服务侧：续传（假服务器发一半断开，第二次请求带 `Range`）、等备份空闲、坏签名在下载安装包前失败、包与清单不符时删除、不支持 / 无更新 / 无签名 / 重复请求的拒绝、安装结果只报告一次。
   - root 侧（假系统操作 + 临时目录）：成功路径的调用顺序为 `stop → install.sh → daemon-reload → restart`；11 种在动系统之前就拒绝的情形；5 种回滚情形后旧二进制、旧单元、旧数据库恢复且新增文件被删；回滚后仍不健康时保留 `installing` 标记并在下次触发时恢复。
3. **真实安装包**：`make package VERSION=0.9.0-rc.0` 的产物包含两个新单元；用它验证了解包逻辑（mac 上打的包带 AppleDouble `._*` 文件，能正常处理）。包大小 5.25 MB（V0.7.0 为 4.89 MB）。
4. **浏览器流程**（chrome-devtools MCP）：临时演示程序以 0.8.0 身份运行，假 GitHub 提供用测试密钥签名的 0.9.0（6 MB 随机内容，限速下发），在 mac 上强制打开安装支持。
   - 点「立即更新到 v0.9.0」后页面实时显示下载进度：`3.88 KB → 1.06 MB → … → 5.06 MB / 6.00 MB`。
   - 进入「已提交安装请求」后出现全屏遮罩（`ui-install-overlay.png`）。
   - 停掉进程、写入一份成功的结果文件、重新启动：页面自动刷新，遮罩消失，最近事件显示「更新完成　已从 v0.8.0 更新到 v0.9.0」，「软件更新」卡片显示「上次安装：v0.8.0 → v0.9.0 成功」。
   - 演示程序是临时代码，验证后已删除，不在仓库里。
5. `make test`、`make test-integration`、`make lint` 全绿；`GOOS=linux GOARCH=arm64` 编译与 lint 通过。

## 过程中发现并修掉的问题

- `web/src/sse.js` 只监听固定的事件类型列表，`update.*` 不在其中：下载进度不刷新，第一阶段的「有新版本」徽标也不会实时出现。已补登记，并补了此前同样漏掉的 `backup.finished`、`storage.*`、`notify.failed`。

## 没有验证

- 真实 systemd 下的 timer 触发与 `ConditionPathExists`、以 root 执行 `install.sh`、重启两个单元、健康检查、回滚。
- `internal/platform/linux/update_ops_linux.go`（`O_NOFOLLOW` 复制、`systemctl`、执行脚本）只交叉编译与 lint，没有在 Linux 上执行过。
- 用真实私钥签名的发布：要等私钥存入 GitHub Secrets 并打一个 tag。
- 主服务沙箱（`ProtectSystem=strict`）下读取 `/opt/action-backup/update-result.json`。
- 步骤见 `docs/02-verification-checklist.md` 问题 13。
