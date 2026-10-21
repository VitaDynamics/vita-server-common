package test

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/VitaDynamics/vita-server-common/src/utils/pay"
)

const testApiKeyV3 = "0123456789abcdef0123456789abcdef"

func genRSAKey(t *testing.T) (*rsa.PrivateKey, string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key,
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pub}))
}

func newMiniWechatClient(t *testing.T) (*pay.MiniWechatPayClient, *rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	mchKey, mchPriv, _ := genRSAKey(t)
	platformKey, _, platformPub := genRSAKey(t)
	client, err := pay.NewMiniWechatPayClient(pay.GenMiniWechatPayClientParam{
		AppId:         "wx-mini",
		MchId:         "mch-1",
		ApiKeyV3:      testApiKeyV3,
		SerialNo:      "serial-1",
		PrivateKeyPEM: mchPriv,
		PublicKeyPEM:  platformPub,
		NotifyUrl:     "https://example.com/notify",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client, mchKey, platformKey
}

func TestMiniWechatBuildRequestPayment(t *testing.T) {
	client, mchKey, _ := newMiniWechatClient(t)

	params, err := client.BuildRequestPayment("wx-prepay-1")
	if err != nil {
		t.Fatal(err)
	}
	if params.Package != "prepay_id=wx-prepay-1" || params.SignType != "RSA" {
		t.Fatalf("unexpected params: %+v", params)
	}
	message := fmt.Sprintf("wx-mini\n%s\n%s\n%s\n", params.TimeStamp, params.NonceStr, params.Package)
	sig, _ := base64.StdEncoding.DecodeString(params.PaySign)
	hashed := sha256.Sum256([]byte(message))
	if err = rsa.VerifyPKCS1v15(&mchKey.PublicKey, crypto.SHA256, hashed[:], sig); err != nil {
		t.Fatalf("paySign verify failed: %v", err)
	}
}

func buildWechatNotify(t *testing.T, platformKey *rsa.PrivateKey, eventType string, resource any) (http.Header, []byte) {
	t.Helper()
	plaintext, _ := json.Marshal(resource)
	block, _ := aes.NewCipher([]byte(testApiKeyV3))
	gcm, _ := cipher.NewGCM(block)
	nonce, ad := "abcdefghijkl", "transaction"
	ciphertext := gcm.Seal(nil, []byte(nonce), plaintext, []byte(ad))

	body, _ := json.Marshal(map[string]any{
		"id":         "notify-1",
		"event_type": eventType,
		"resource": map[string]string{
			"algorithm":       "AEAD_AES_256_GCM",
			"ciphertext":      base64.StdEncoding.EncodeToString(ciphertext),
			"associated_data": ad,
			"nonce":           nonce,
		},
	})

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	hashed := sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%s\n", ts, "n-1", body)))
	sig, err := rsa.SignPKCS1v15(rand.Reader, platformKey, crypto.SHA256, hashed[:])
	if err != nil {
		t.Fatal(err)
	}
	header := http.Header{}
	header.Set("Wechatpay-Timestamp", ts)
	header.Set("Wechatpay-Nonce", "n-1")
	header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(sig))
	return header, body
}

func TestMiniWechatParseNotify(t *testing.T) {
	client, _, platformKey := newMiniWechatClient(t)

	header, body := buildWechatNotify(t, platformKey, pay.WechatNotifyTransactionSuccess, map[string]any{
		"out_trade_no":   "T1",
		"transaction_id": "W1",
		"trade_state":    "SUCCESS",
		"amount":         map[string]any{"total": 3000},
		"payer":          map[string]string{"openid": "oid-1"},
	})
	notify, err := client.ParseNotify(header, body)
	if err != nil {
		t.Fatal(err)
	}
	if notify.Transaction == nil || notify.Transaction.OutTradeNo != "T1" || notify.Transaction.Amount.Total != 3000 || notify.Transaction.Payer.OpenId != "oid-1" {
		t.Fatalf("unexpected transaction notify: %+v", notify.Transaction)
	}

	header, body = buildWechatNotify(t, platformKey, pay.WechatNotifyRefundSuccess, map[string]any{
		"out_refund_no": "R1",
		"refund_status": "SUCCESS",
		"amount":        map[string]any{"total": 3000, "refund": 1000},
	})
	notify, err = client.ParseNotify(header, body)
	if err != nil {
		t.Fatal(err)
	}
	if notify.Refund == nil || notify.Refund.OutRefundNo != "R1" || notify.Refund.Amount.Refund != 1000 {
		t.Fatalf("unexpected refund notify: %+v", notify.Refund)
	}

	header.Set("Wechatpay-Nonce", "tampered")
	if _, err = client.ParseNotify(header, body); err == nil {
		t.Fatal("expected signature verify failure")
	}
}

func TestYuanCentConvert(t *testing.T) {
	for cent, yuan := range map[int64]string{0: "0.00", 5: "0.05", 3000: "30.00", 12345: "123.45"} {
		if got := pay.CentToYuan(cent); got != yuan {
			t.Errorf("CentToYuan(%d)=%s, want %s", cent, got, yuan)
		}
	}
	for yuan, cent := range map[string]int64{"": 0, "30": 3000, "30.5": 3050, "123.45": 12345, "0.01": 1} {
		got, err := pay.YuanToCent(yuan)
		if err != nil || got != cent {
			t.Errorf("YuanToCent(%q)=%d,%v, want %d", yuan, got, err, cent)
		}
	}
	for _, bad := range []string{"1.234", "abc"} {
		if _, err := pay.YuanToCent(bad); err == nil {
			t.Errorf("YuanToCent(%q) expected error", bad)
		}
	}
}
