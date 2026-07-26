package webhookauth

import "testing"

func TestVerify(t *testing.T) {
	body := []byte(`{"alerts":[]}`)
	sig := Sign("topsecret", body)

	if ok, checked := Verify("topsecret", sig, body); !ok || !checked {
		t.Error("正确签名应通过且标记 checked")
	}
	if ok, _ := Verify("topsecret", "sha256=deadbeef", body); ok {
		t.Error("错误签名应拒绝")
	}
	if ok, checked := Verify("topsecret", sig, []byte("tampered")); ok || !checked {
		t.Error("篡改 body 应拒绝")
	}
	if ok, checked := Verify("", "whatever", body); !ok || checked {
		t.Error("空 secret 应放行但 checked=false")
	}
}
