package test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/VitaDynamics/vita-server-common/src/utils/pay"
)

func TestMiniWechatParseCombineNotify(t *testing.T) {
	client, _, platformKey := newMiniWechatClient(t)
	paidAt := time.Now().UTC().Truncate(time.Second)
	header, body := buildWechatNotify(t, platformKey, pay.WechatNotifyTransactionSuccess, map[string]any{
		"combine_appid":        "wx-mini",
		"combine_mchid":        "mch-1",
		"combine_out_trade_no": "CMB10001",
		"sub_orders": []map[string]any{
			{"mchid": "mch-1", "out_trade_no": "D1", "transaction_id": "WX-D", "trade_state": "SUCCESS", "success_time": paidAt.Format(time.RFC3339), "amount": map[string]any{"total_amount": 50000}},
			{"mchid": "mch-1", "out_trade_no": "R1", "transaction_id": "WX-R", "trade_state": "SUCCESS", "success_time": paidAt.Format(time.RFC3339), "amount": map[string]any{"total_amount": 3000}},
		},
	})
	notify, err := client.ParseCombineNotify(header, body)
	if err != nil {
		t.Fatal(err)
	}
	if notify.CombineOutTradeNo != "CMB10001" || notify.AppId != "wx-mini" || len(notify.SubOrders) != 2 ||
		notify.SubOrders[0].TransactionId != "WX-D" || notify.SubOrders[0].AmountCent != 50000 || !notify.SubOrders[0].SuccessTime.Equal(paidAt) ||
		notify.SubOrders[1].OutTradeNo != "R1" {
		t.Fatalf("unexpected combine notify: %+v", notify)
	}

	header, body = buildWechatNotify(t, platformKey, pay.WechatNotifyRefundSuccess, map[string]any{})
	notify, err = client.ParseCombineNotify(header, body)
	if err != nil || notify.EventType != pay.WechatNotifyRefundSuccess || len(notify.SubOrders) != 0 {
		t.Fatalf("non-success event = %+v, err=%v", notify, err)
	}

	header.Set("Wechatpay-Nonce", "tampered")
	if _, err = client.ParseCombineNotify(header, body); err == nil {
		t.Fatal("expected combine signature verify failure")
	}
}

func TestMiniAlipayMergeValidation(t *testing.T) {
	client := newMiniAlipayClient(t)
	if _, err := client.CreateMergeTrade(t.Context(), pay.MiniAlipayMergeCreateParam{
		BuyerOpenId: "buyer-1",
		NotifyUrl:   "https://example.com/m",
		SubOrders: []pay.MiniAlipayMergeSubOrder{
			{OutTradeNo: "D1", Subject: "押金", AmountCent: 1},
			{OutTradeNo: "R1", Subject: "租金", AmountCent: 1},
		},
	}); err == nil {
		t.Fatal("expected empty out merge no to fail")
	}
	valid := []pay.MiniAlipayMergeSubOrder{
		{OutTradeNo: "D1", Subject: "押金", AmountCent: 1},
		{OutTradeNo: "R1", Subject: "租金", AmountCent: 1},
	}
	cases := map[string]pay.MiniAlipayMergeCreateParam{
		"single sub":    {OutMergeNo: "M1", BuyerOpenId: "buyer-1", NotifyUrl: "https://example.com/m", SubOrders: valid[:1]},
		"no notify url": {OutMergeNo: "M1", BuyerOpenId: "buyer-1", SubOrders: valid},
		"duplicate sub": {OutMergeNo: "M1", BuyerOpenId: "buyer-1", NotifyUrl: "https://example.com/m", SubOrders: []pay.MiniAlipayMergeSubOrder{valid[0], valid[0]}},
	}
	for name, param := range cases {
		if _, err := client.CreateMergeTrade(t.Context(), param); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func newMiniAlipayClient(t *testing.T) *pay.MiniAlipayClient {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	client, err := pay.NewMiniAlipayClient(pay.GenMiniAlipayClientParam{
		AppId:      "ali-mini",
		PrivateKey: string(priv),
		PublicKey:  string(pub),
		NotifyUrl:  "https://example.com/ali/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
