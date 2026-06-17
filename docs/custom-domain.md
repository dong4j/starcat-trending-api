# Fly.io 自定义域名 + 自定义证书

> 来源: https://fly.io/docs/networking/custom-domain/  
> 适用: starcat-sharing-api (同理适用于 starcat-trending-api)

## 1. 三种 DNS 接入方式

| 方式 | 适用 | 难度 |
|------|------|------|
| **A/AAAA 记录** | 根域 `starcat.ink` | 推荐 |
| **CNAME 记录** | 子域 `www.starcat.ink` / `app.starcat.ink` | 推荐 |
| **DNS-01 挑战** | 通配符 `*.starcat.ink`, 或想在指向流量前先发证书 | 较复杂 |

Fly 拿到 DNS 记录后, 自动用 Let's Encrypt 签发证书, 无需手动操作。

---

## 2. 最简流程 (用 A/AAAA 接根域为例)

```bash
# 1. 把域名绑到 fly app
fly certs add starcat.ink
# 终端会打印出要配的 A 和 AAAA 记录值, 例如:
#   A     starcat.ink  -> 66.241.124.123
#   AAAA  starcat.ink  -> 2a09:8280:1::1:abcd

# 2. 去你的 DNS 服务商 (Cloudflare / 阿里云 / DNSPod...) 加这两条记录
#    (如果还没 IPv6, 先: fly ips allocate --app starcat-sharing-api)

# 3. 加完 DNS 等 1-5 分钟, 让 fly 自动验证
fly certs check starcat.ink
# 期待输出: "Certificate is valid" + "DNS is correctly configured"

# 4. 完成! 浏览器访问 https://starcat.ink 应该能看到服务
```

---

## 3. CNAME 接子域 (更简单)

```bash
fly certs add www.starcat.ink
# 终端会显示: CNAME www.starcat.ink -> starcat-sharing-api.fly.dev
# 把这条 CNAME 加到 DNS 服务商即可
```

子域永远推荐 CNAME, 以后换 IP 不用改 DNS。

---

## 4. 用自己的证书 (可选)

只在你已经有签好的证书时才需要 (例如 Cloudflare Origin Cert, 或企业 CA):

```bash
fly certs import starcat.ink \
  --fullchain ./fullchain.pem \
  --private-key ./private-key.pem
```

上传后状态会是 `pending_ownership`, 等域名所有权校验完变成 `active`。

- 校验方法 1: 加 AAAA 记录指向 fly 给的 IPv6
- 校验方法 2: 加 `_fly-ownership` TXT 记录 (值在 `fly certs show` 里)

**自己的证书和 Let's Encrypt 证书可以共存**, 自己的优先, 平台签的当 fallback。

---

## 5. Cloudflare 用户必看

如果你用 Cloudflare 橙色云代理, 直接走 A/AAAA 或 CNAME **可能签不出证书**。两种解法:

### 方案 A: 让 fly 走 HTTP-01 验证 (保持橙色云)

1. 在 Cloudflare DNS 加一条 `_fly-ownership` TXT 记录, 值在 `fly certs show` 里
2. Cloudflare SSL/TLS 模式设为 **Full** 或 **Full (Strict)**, 千万**不要**选 Flexible (会 301 循环)

### 方案 B: 改用 Cloudflare Origin Certificate + 灰色云

1. Cloudflare Dashboard -> SSL/TLS -> Origin Server -> Create Certificate
2. 把证书和私钥存成 `fullchain.pem` / `private-key.pem`
3. 跑上面的 `fly certs import` 命令
4. Cloudflare 的代理改成**灰色云** (DNS only), 不代理流量
5. 同样需要 `_fly-ownership` TXT 记录验证所有权

> 注意: 不要再用 Flexible SSL, 那是 2015 年遗留产物, 会跟 HSTS / HTTPS 重定向打架

---

## 6. 常用命令速查

```bash
fly certs list                          # 列出 app 上所有证书
fly certs add starcat.ink               # 添加并启动签发流程
fly certs check starcat.ink             # 触发一次校验, 看现状
fly certs show starcat.ink              # 看证书详情 + 需要的 DNS 记录
fly certs setup starcat.ink             # 显示 DNS 配置指引 (不真改)
fly certs remove starcat.ink            # 移除所有证书
fly certs remove starcat.ink --custom   # 只移自己的证书
fly certs remove starcat.ink --acme     # 只停 ACME 自动签发
fly ips allocate                        # 给 app 分配 IPv4/v6
```

---

## 7. 出问题先看这俩

### 证书签不出来

```bash
fly certs check starcat.ink   # 看具体哪步失败 (DNS / 校验 / CA)
```

最常见原因:
- DNS 记录值抄错
- DNS 还没传播完 (改完等 1-5 分钟)
- Cloudflare 橙色云挡住了 HTTP-01 (见上方案 A)
- 触发 Let's Encrypt 速率限制 (同域名每周 50 张, 重复证书每周 5 张, 失败验证每小时 5 次)

### 卡住 / 想查为什么

- [letsdebug.net](https://letsdebug.net/): 输入域名, 跑 Let's Encrypt 兼容性诊断
- 速率限制只能等窗口过去, fly 也帮不了

---

## 8. 跟 starcat 项目的关系

- **BASE_URL 这个 env** 在 `cmd/server/main.go` 读, 跟证书**没关系**。证书管的是"HTTPS 能正常建立", BASE_URL 管的是"应用里分享链接用什么域名"。两件事独立。
- 域名切换只需要重新配 DNS + 等证书, **不需要重新部署 app**。
- 代码里读 `Host` header 来识别用户从哪个域名来:
  ```go
  host := r.Host   // "starcat.ink" / "www.starcat.ink" / "starcat-sharing-api.fly.dev"
  ```
