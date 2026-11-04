# src/v1.0.2 变更说明

- 生成时间：2026-11-04 03:21:39 UTC
- 对比范围：[src/v1.0.1...src/v1.0.2](https://github.com/VitaDynamics/vita-server-common/compare/src/v1.0.1...src/v1.0.2)

## 功能说明

- 微信小程序登录：用 `wx.login()` 的 code 换取 openid。支付宝小程序登录：用 `my.getAuthCode()` 的 authCode 换取用户标识。
- 微信小程序 JSAPI 支付：下单、生成调起参数、查单、关单、退款、查退款，以及支付和退款回调验签解密。支付宝小程序支付：下单、查单、关单、退款、查退款；资金授权支持冻结、解冻和查询；交易与授权回调验签解析。金额在分与元之间转换。
- 合单支付：微信支持合单下单、关单、查单和回调解析，子单需走合单查单。支付宝支持同事务合并下单和合单回调解析。
- 微信支付 V3 请求带上 context，接口 4xx/5xx 返回带错误码的 `WechatV3APIError`。回调验签后仍可读取请求体。
- 租赁订单资金处理（云平台调用 app-server）：`OrderFundHandle` 按类型退租金或退押金。退押金未指定原交易时，按订单在收回设备后退押金，仍有待补缴则不退。`ReplaceDevice` 按订单和目标设备 SN 更换设备。
- 用户基础信息查询：单条查询增加按 userId、设备 SN 查询。新增批量查询，按手机号、userId 或设备 SN 之一查询，单次最多 200 条；设备 SN 按当前绑定用户查询，未命中的条件不返回。
- 设备服务新增 `UpdateDevice`：按设备 SN 或设备 ID，更新所有者或绑定状态。

## 提交记录

- add replace device method ([`f556a0e`](https://github.com/VitaDynamics/vita-server-common/commit/f556a0e0273df6636039557998ca7ead30f7c6c8))
- add rpc method ([`b359f66`](https://github.com/VitaDynamics/vita-server-common/commit/b359f66dbd65ad9170aff8e9c429f347587d7cc1))
- pay combine ([`a084e4d`](https://github.com/VitaDynamics/vita-server-common/commit/a084e4d45e508b2d2a7d4f34abddb18d18021c92))
- add rpc interface ([`a43ceab`](https://github.com/VitaDynamics/vita-server-common/commit/a43ceab687b7a8e0cb4929e39c084745e712b488))
- add refund proto ([`4fd725a`](https://github.com/VitaDynamics/vita-server-common/commit/4fd725a4ec0aee42a282c1a6f83c9e30402a3d35))
- mini program pay and login ([`5ad5c99`](https://github.com/VitaDynamics/vita-server-common/commit/5ad5c9995441571c52a28a39afca7da934333f61))

## 变更文件

```text
A	doc/src-v1.0.1-test.md
A	doc/src-v1.0.1.md
A	src/svr/appsvr/rental_order_handle.proto
M	src/svr/appsvr/service/basic/user_and_device_query.pb.go
M	src/svr/appsvr/service/basic/user_and_device_query_grpc.pb.go
A	src/svr/appsvr/service/rentalorder/rental_order_handle.pb.go
A	src/svr/appsvr/service/rentalorder/rental_order_handle_grpc.pb.go
M	src/svr/appsvr/user_and_device_query.proto
M	src/svr/cloudsvr/device.proto
M	src/svr/cloudsvr/service/device/device.pb.go
M	src/svr/cloudsvr/service/device/device_grpc.pb.go
A	src/test/mini_combine_test.go
A	src/test/mini_pay_test.go
A	src/utils/login/mini_ali.go
A	src/utils/login/mini_wechat.go
A	src/utils/pay/mini_alipay.go
A	src/utils/pay/mini_alipay_combine.go
A	src/utils/pay/mini_wechat.go
A	src/utils/pay/mini_wechat_combine.go
M	src/utils/pay/wechat.go
```
