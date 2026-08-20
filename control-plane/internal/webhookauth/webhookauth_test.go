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

// ---- 多密钥轮换 ----
//
// 为什么这组用例存在:密钥只在启动时读环境(无热重载),轮换靠滚动重启。
// 而滚动重启期间一半副本持旧密钥、一半持新密钥 —— Alertmanager 无论用哪个签名
// 都会被另一半以 401 拒绝。Signal Ingress 的 401 意味着**告警丢失**:
// Alertmanager 重试几次就放弃,那段时间的故障在本系统里完全不存在。

func TestVerifyAcceptsEitherSecretDuringRotation(t *testing.T) {
	body := []byte(`{"alerts":[]}`)
	oldSig := Sign("old-secret", body)
	newSig := Sign("new-secret", body)

	// 轮换窗口内配 "新,旧":两种签名都必须收
	const both = "new-secret,old-secret"
	if ok, checked := Verify(both, newSig, body); !ok || !checked {
		t.Error("轮换窗口内新密钥签名应通过")
	}
	if ok, checked := Verify(both, oldSig, body); !ok || !checked {
		t.Error("轮换窗口内旧密钥签名应通过 —— 否则滚动重启期间会丢告警")
	}
}

func TestVerifyRejectsRetiredSecretAfterRotation(t *testing.T) {
	body := []byte(`{"alerts":[]}`)
	oldSig := Sign("old-secret", body)
	// 轮换第三步:只留新密钥,旧的必须失效 ——
	// 否则"轮换"只是加了一个密钥,泄漏的那个仍然可用。
	if ok, _ := Verify("new-secret", oldSig, body); ok {
		t.Error("轮换完成后旧密钥必须失效")
	}
}

func TestParseSecretsTrimsAndDropsEmpty(t *testing.T) {
	got := ParseSecrets(" a , ,b,, ")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ParseSecrets = %v, want [a b]", got)
	}
	if n := len(ParseSecrets("   ")); n != 0 {
		t.Errorf("全空白应解析成 0 项,得到 %d", n)
	}
}

func TestVerifyEmptySecretStillPassesUnchecked(t *testing.T) {
	// 开发环境行为不变:未配密钥则放行但标记未校验。
	// 生产由 config.Validate 拒绝这种配置。
	ok, checked := Verify("", "sha256=whatever", []byte("x"))
	if !ok || checked {
		t.Errorf("空密钥应 (true,false),得到 (%v,%v)", ok, checked)
	}
}

func TestVerifyWrongSignatureRejectedWithMultipleSecrets(t *testing.T) {
	body := []byte(`{"alerts":[]}`)
	if ok, _ := Verify("a,b,c", Sign("d", body), body); ok {
		t.Error("都不匹配时必须拒绝(多密钥不该变成放行)")
	}
}
