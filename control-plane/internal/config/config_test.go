package config

import "testing"

func base(prod bool) Config {
	env := "development"
	if prod {
		env = "production"
	}
	return Config{
		Env:           env,
		AuthMode:      "hs256",
		HS256Secret:   "a-strong-random-secret-of-length->=32-chars",
		InternalToken: "internal-token",
		WebhookSecret: "webhook-secret",
	}
}

func TestValidate_DevAllowsWeak(t *testing.T) {
	c := base(false)
	c.HS256Secret = DefaultHS256Secret
	c.InternalToken = ""
	c.WebhookSecret = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("dev 模式不应因弱配置失败: %v", err)
	}
}

func TestValidate_ProdRejectsDefaultSecret(t *testing.T) {
	c := base(true)
	c.HS256Secret = DefaultHS256Secret
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝默认 HS256 密钥")
	}
}

func TestValidate_ProdRejectsShortSecret(t *testing.T) {
	c := base(true)
	c.HS256Secret = "short"
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝过短 HS256 密钥")
	}
}

func TestValidate_ProdRequiresInternalToken(t *testing.T) {
	c := base(true)
	c.InternalToken = ""
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须要求 internal token")
	}
}

func TestValidate_ProdRequiresWebhookSecret(t *testing.T) {
	c := base(true)
	c.WebhookSecret = ""
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须要求 webhook secret")
	}
}

func TestValidate_ProdRejectsDisabledAuth(t *testing.T) {
	c := base(true)
	c.AuthMode = "disabled"
	if err := c.Validate(); err == nil {
		t.Fatal("生产模式必须拒绝 auth disabled")
	}
}

func TestValidate_ProdOIDCRequiresJWKS(t *testing.T) {
	c := base(true)
	c.AuthMode = "oidc"
	// 未配置 issuer/jwks
	if err := c.Validate(); err == nil {
		t.Fatal("oidc 模式必须要求 issuer 与 jwks url")
	}
	c.OIDCIssuer = "https://idp.example"
	c.OIDCJWKSURL = "https://idp.example/jwks"
	if err := c.Validate(); err != nil {
		t.Fatalf("配置齐全后不应失败: %v", err)
	}
}

func TestHasRole(t *testing.T) {
	// 未配置 / all → 全部启用(向后兼容单体部署)
	for _, c := range []Config{{}, {Roles: []string{"all"}}} {
		for _, r := range []string{"api", "internal", "ingest", "trigger", "outbox"} {
			if !c.HasRole(r) {
				t.Errorf("roles=%v 应启用 %s", c.Roles, r)
			}
		}
	}
	// 仅 api:其他角色关闭
	c := Config{Roles: []string{"api"}}
	if !c.HasRole("api") {
		t.Error("api 应启用")
	}
	for _, r := range []string{"internal", "ingest", "trigger", "outbox"} {
		if c.HasRole(r) {
			t.Errorf("roles=[api] 不应启用 %s", r)
		}
	}
	// 组合角色
	c2 := Config{Roles: []string{"ingest", "trigger", "outbox"}}
	if !c2.HasRole("ingest") || !c2.HasRole("outbox") {
		t.Error("组合角色应启用其成员")
	}
	if c2.HasRole("api") {
		t.Error("组合角色不应启用未列出的 api")
	}
}

func TestValidate_ProdHappyPath(t *testing.T) {
	if err := base(true).Validate(); err != nil {
		t.Fatalf("完整生产配置不应失败: %v", err)
	}
}
