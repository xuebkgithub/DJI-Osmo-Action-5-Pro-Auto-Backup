# action-backup（macOS 包）

mac 上的用途是开发、联调与**接真相机的 dry-run**（真实 USB / diskutil 发现，只扫描和规划，零写入）。
正式长期运行仍在 OEC-Turbo（ARM64 Linux，systemd），见仓库 `action-backup/README.md`。

包内：

| 文件 | 说明 |
| --- | --- |
| `action-backup` | production 构建，不含 faultinject |
| `config.mac-dryrun.yaml` | mac 接真相机的 dry-run 模板，认证关闭，监听 `127.0.0.1:8080` |
| `config.example.yaml` | OEC-Turbo 上的 production 模板，仅作完整选项参考（cifs 挂载、`/etc`、`/var/lib` 均为 Linux 路径） |
| `SHA256SUMS` | 逐文件校验和 |

## 1. 校验与解除隔离

从 GitHub 下载的二进制未签名未公证，Gatekeeper 会拦。先核对校验和再解除隔离属性：

```bash
shasum -a 256 -c SHA256SUMS
xattr -dr com.apple.quarantine ./action-backup
./action-backup --version
```

## 2. dry-run 跑一轮

```bash
mkdir -p dev                                      # 配置里数据库写在 ./dev/
./action-backup --config config.mac-dryrun.yaml --check
./action-backup --config config.mac-dryrun.yaml
```

相机用 USB 接上 mac；NAS 在 Finder 里挂到 `/Volumes/VideoBackup`（未挂载时计划仍会生成，`target_checked` 为 `false`）。
浏览器打开 <http://127.0.0.1:8080>，`config.mac-dryrun.yaml` 里 `auth.enabled: false`，不需要登录。
`Ctrl-C` 停止。

按自己的设备改 `storage.mount_point`、`storage.share`、`destination_root` 即可。

## 3. 需要登录认证时

`config.mac-dryrun.yaml` 默认关闭认证。要打开，先改这一段：

```yaml
server:
  listen: "127.0.0.1:8080"
  auth:
    enabled: true
    username: "admin"
```

再生成口令哈希（交互式输入两遍，bcrypt cost 12，直接写回该文件的 `password_hash`）：

```bash
./action-backup passwd --config config.mac-dryrun.yaml
./action-backup --config config.mac-dryrun.yaml --check
```

`passwd` 会重写整个 YAML，文件内的空行与行内注释不保留，自定义的配置值不受影响。
