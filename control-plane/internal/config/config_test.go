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

func TestValidate_ProdHappyPath(t *testing.T) {
	if err := base(true).Validate(); err != nil {
		t.Fatalf("完整生产配置不应失败: %v", err)
	}
}
