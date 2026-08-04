# Binance / OKX 收款配置

这个目录是独立的 BEpusdt 构建，不需要修改原始 BEpusdt 项目。交易所账单轮询在本服务内完成，订单仍然使用 BEpusdt 原有的回调和通知流程。

## 支持方式

- Binance：读取 Binance Pay 交易历史，按收款 UID 分别筛选 USDT 和 USDC。
- OKX：读取资金账户账单，按币种分别筛选 `type=1` 和 `type=72` 的 USDT、USDC 入账。
- 每笔交易以交易所返回的唯一交易 ID 去重。
- 订单匹配同时校验交易类型、收款 UID、对应币种数量和订单时间窗口。
- USDT 与 USDC 使用独立交易类型、金额增量和扫描游标，不会跨币种匹配。

## API 权限

两家交易所都只需要创建**只读 API Key**，不要开启提现、转账或交易权限，并建议设置 API IP 白名单。

需要配置：

```dotenv
BEPUSDT_BINANCE_API_KEY=...
BEPUSDT_BINANCE_SECRET_KEY=...
BEPUSDT_BINANCE_UID=...

BEPUSDT_OKX_API_KEY=...
BEPUSDT_OKX_SECRET_KEY=...
BEPUSDT_OKX_PASSPHRASE=...
BEPUSDT_OKX_UID=...
```

只配置 Binance 或只配置 OKX 都可以；两组都不配置时，交易所轮询会自动关闭。

## Docker 部署

在这个目录执行：

```bash
cp .env.example .env
vi .env
docker compose up -d --build
```

`docker-compose.yml` 使用项目目录下的持久化目录，复制到其他服务器也可以直接使用：

```text
./data
./log
```

将项目放到自定义的 Docker 项目目录即可，数据会保存到该目录下的 `data` 和 `log` 文件夹。

服务监听 `8080` 端口。第一次启动后访问：

```text
http://设备IP:8080
```

查看交易所轮询日志：

```bash
docker logs -f bepusdt-exchange
```

日志中出现 `exchange payment polling enabled` 表示至少有一组交易所凭据已加载；出现 `disabled` 表示没有加载凭据。

## 注意事项

- `.env` 含有 API 密钥，不能提交到代码仓库，也不要放到前端目录。
- 交易所收款不是链上转账，不会产生可在区块浏览器查询的链上交易哈希；系统使用交易所账单中的交易 ID 作为内部交易编号。
- API Key 所属 UID 必须与后台钱包中填写的 Binance/OKX UID 一致。

## 后台配置与接口

登录后台后进入“系统设置 -> 交易所支付”，可以分别启用 Binance Pay 和 OKX Pay，填写收款 UID、API 地址和只读 API 凭据，并使用“测试连接”验证最近 24 小时账单读取权限。

以下接口均要求管理员登录：

```text
POST /api/exchange/config
POST /api/exchange/save
POST /api/exchange/test   {"provider":"binance"|"okx"}
```

- 配置查询接口只返回密钥是否已配置，不返回 API Key、Secret 或 Passphrase 明文。
- 后台保存后轮询任务立即热重载，不需要重启容器。
- 后台数据库配置优先于同名环境变量；未在后台填写的密钥仍可从 `.env` 读取。
- 启用交易所后会自动为 USDT、USDC 创建对应 UID 的收款方式；同一交易所中与当前 API 账户 UID 不一致的钱包不会参与订单分配。
- 收银台会把交易所方式标记为 Binance Pay 或 OKX Pay，并显示收款 UID 与唯一应付金额；链上方式继续显示地址二维码。
