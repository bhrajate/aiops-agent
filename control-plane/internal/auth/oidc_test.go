package auth

// OIDC/JWKS 校验路径的契约。
//
// 为什么这个文件必须存在:`deploy/helm/aiops/values-prod.yaml` 里
// `authMode: "oidc"` —— **生产跑的就是这条路径**,而它此前零测试覆盖。
// 开发用的 hs256 路径有测试,生产用的这条没有,方向恰好反了。
//
// 这里用内存里的假 IdP(自签 RSA + httptest 提供 JWKS),不需要真实 IdP,
// 所以能进 CI。接入真实 IdP 仍是部署侧动作,但"代码能不能正确校验"
// 是可以在这里证完的。
//
// 每条用例针对的都是**放行了不该放行的 token** —— 那类缺陷不会报错,
// 只会让越权访问看起来完全合法,而审计里记的是一个"通过了认证"的身份。

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// fakeIdP 是内存里的 IdP:持有 RSA 私钥,暴露 JWKS 端点,能签发 token。
type fakeIdP struct {
	key      *rsa.PrivateKey
	kid      string
	srv      *httptest.Server
	fetches  atomic.Int64 // JWKS 被拉取的次数,用于验证缓存与节流
	failNext atomic.Bool  // 下一次 JWKS 请求返回 500
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥: %v", err)
	}
	idp := &fakeIdP{key: key, kid: "kid-1"}
	idp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		idp.fetches.Add(1)
		if idp.failNext.Swap(false) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{idp.jwk()}})
	}))
	t.Cleanup(idp.srv.Close)
	return idp
}

func (i *fakeIdP) jwk() map[string]string {
	pub := i.key.Public().(*rsa.PublicKey)
	return map[string]string{
		"kty": "RSA",
		"kid": i.kid,
		"use": "sig",
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

// tokenOpts 让每条用例只改自己关心的那一项。
type tokenOpts struct {
	issuer   string
	audience string
	subject  string
	roles    []string
	clusters []string
	nss      []string
	expires  time.Time
	kid      string
	method   jwt.SigningMethod
	hsSecret string // 非空则用 HS256 签(测试算法混淆)
}

func (i *fakeIdP) sign(t *testing.T, o tokenOpts) string {
	t.Helper()
	if o.expires.IsZero() {
		o.expires = time.Now().Add(time.Hour)
	}
	if o.kid == "" {
		o.kid = i.kid
	}
	claims := jwt.MapClaims{
		"iss":        o.issuer,
		"aud":        o.audience,
		"sub":        o.subject,
		"exp":        o.expires.Unix(),
		"iat":        time.Now().Add(-time.Minute).Unix(),
		"roles":      o.roles,
		"clusters":   o.clusters,
		"namespaces": o.nss,
	}
	method := o.method
	if method == nil {
		method = jwt.SigningMethodRS256
	}
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = o.kid

	var signed string
	var err error
	if o.hsSecret != "" {
		signed, err = tok.SignedString([]byte(o.hsSecret))
	} else {
		signed, err = tok.SignedString(i.key)
	}
	if err != nil {
		t.Fatalf("签发 token: %v", err)
	}
	return signed
}

const (
	testIssuer = "https://idp.corp.example"
	testAud    = "aiops"
)

func newVerifier(idp *fakeIdP) *JWKSVerifier {
	return NewJWKSVerifier(idp.srv.URL, testIssuer, testAud)
}

func TestOIDCVerifiesValidTokenAndMapsClaims(t *testing.T) {
	idp := newFakeIdP(t)
	v := newVerifier(idp)

	raw := idp.sign(t, tokenOpts{
		issuer: testIssuer, audience: testAud, subject: "alice@corp",
		roles: []string{"sre"}, clusters: []string{"prod-cn-1"}, nss: []string{"payment"},
	})
	p, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("合法 token 应通过: %v", err)
	}
	// claims → Principal 的映射决定了 RBAC/ABAC 的输入。
	// 少映一个字段的后果是"权限凭空变小或变大",而不会报错。
	if p.Subject != "alice@corp" {
		t.Errorf("Subject = %q", p.Subject)
	}
	if len(p.Roles) != 1 || p.Roles[0] != "sre" {
		t.Errorf("Roles = %v,RBAC 依赖它", p.Roles)
	}
	if len(p.Clusters) != 1 || p.Clusters[0] != "prod-cn-1" {
		t.Errorf("Clusters = %v,ABAC 依赖它", p.Clusters)
	}
	if len(p.Namespaces) != 1 || p.Namespaces[0] != "payment" {
		t.Errorf("Namespaces = %v,ABAC 依赖它", p.Namespaces)
	}
	// 映射正确才能让 Can/InScope работать
	if !p.Can(ActionReadIncident) {
		t.Error("sre 应能读 incident —— 说明 roles 没映射进去")
	}
}

func TestOIDCRejectsWrongIssuer(t *testing.T) {
	// issuer 不校验的后果:任何 IdP 签的 token 都能进来 ——
	// 包括攻击者自己搭的那个。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{issuer: "https://evil.example", audience: testAud, subject: "x"})
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("issuer 不匹配必须拒绝")
	}
}

func TestOIDCRejectsWrongAudience(t *testing.T) {
	// audience 不校验的后果:给**别的**服务签的 token 能用来访问本系统。
	// 企业里同一个 IdP 会给几十个系统签 token,这条是唯一区分。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{issuer: testIssuer, audience: "some-other-app", subject: "x"})
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("audience 不匹配必须拒绝")
	}
}

func TestOIDCRejectsExpiredToken(t *testing.T) {
	// 注意实现里有 60s leeway(容忍时钟偏差),所以过期时间要拉得比它远。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{
		issuer: testIssuer, audience: testAud, subject: "x",
		expires: time.Now().Add(-10 * time.Minute),
	})
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("已过期 token 必须拒绝")
	}
}

func TestOIDCAllowsClockSkewWithinLeeway(t *testing.T) {
	// 反面:leeway 之内不能拒。IdP 与本机有几十秒偏差是常态,
	// 严格判定会让登录随机失败,而那种故障极难定位("有时候能登有时候不能")。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{
		issuer: testIssuer, audience: testAud, subject: "x",
		expires: time.Now().Add(-20 * time.Second), // 在 60s leeway 内
	})
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("leeway(60s)内的时钟偏差不该拒绝: %v", err)
	}
}

func TestOIDCRejectsHS256AlgorithmConfusion(t *testing.T) {
	// **算法混淆**:攻击者把 alg 改成 HS256,用 RSA **公钥的 PEM 字节**当 HMAC
	// 密钥签名。公钥是公开的(JWKS 就在那儿),所以攻击者拿得到 ——
	// 这是 JWT 最经典的一个洞,成功的话能伪造任意身份。
	//
	// 用真实攻击形态而不是"随便一个 HMAC 密钥":后者会因为**签名不对**被拒,
	// 于是用例在防护被摘掉后照样通过 —— 那种绿色什么都不证明。
	// (我第一版就是那样写的,摘掉 RS256 锁定后用例仍然通过。)
	idp := newFakeIdP(t)
	v := newVerifier(idp)

	der, err := x509.MarshalPKIXPublicKey(idp.key.Public())
	if err != nil {
		t.Fatalf("编码公钥: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": testIssuer, "aud": testAud, "sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = idp.kid
	raw, err := tok.SignedString(pubPEM)
	if err != nil {
		t.Fatalf("构造攻击 token: %v", err)
	}

	p, err := v.Verify(context.Background(), raw)
	if err == nil {
		t.Fatalf("算法混淆攻击成功 —— 伪造身份 %q 通过了认证", p.Subject)
	}

	// 断言**是哪一层挡住的**。这里有两层独立防护:
	//   1. 本实现显式锁定 RS256(WithValidMethods + Method 类型断言)
	//      → 报 "signing method HS256 is invalid"
	//   2. keyfunc 返回 *rsa.PublicKey,而 HMAC 校验要 []byte(jwt/v5 的结构性防护)
	//      → 报 "HMAC verify expects []byte"
	//
	// 只断言"被拒"的话,第 1 层被摘掉后第 2 层会接住,用例照常通过 ——
	// 而那时我们的显式防护已经没了,只剩下依赖库实现细节的偶然保护。
	// 实测:摘掉锁定后错误信息确实从第 1 层变成第 2 层。
	if !strings.Contains(err.Error(), "signing method") {
		t.Errorf("被拒了但不是因为算法锁定,而是 %q —— "+
			"说明 RS256 显式锁定已失效,当前只靠 jwt/v5 的 keyfunc 类型不匹配兜底", err)
	}
}

func TestOIDCRejectsNoneAlgorithm(t *testing.T) {
	// alg=none:不签名的 token。库应当拒,但值得钉住 ——
	// 放行它等于完全没有认证。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"iss": testIssuer, "aud": testAud, "sub": "attacker",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = idp.kid
	raw, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("构造 none token: %v", err)
	}
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("alg=none 必须拒绝")
	}
}

func TestOIDCRejectsUnknownKid(t *testing.T) {
	// 未知 kid 会触发一次 JWKS 刷新;刷新后仍找不到就必须拒,
	// 而不是回退到"用手上任意一个 key 试试"。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{
		issuer: testIssuer, audience: testAud, subject: "x", kid: "kid-does-not-exist",
	})
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("未知 kid 必须拒绝")
	}
}

func TestOIDCCachesKeysAcrossVerifications(t *testing.T) {
	// 每次校验都拉一次 JWKS 会把 IdP 打挂(每个请求一次外部 HTTP),
	// 且给本系统引入一个新的必经故障点。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	raw := idp.sign(t, tokenOpts{issuer: testIssuer, audience: testAud, subject: "x"})

	for i := 0; i < 5; i++ {
		if _, err := v.Verify(context.Background(), raw); err != nil {
			t.Fatalf("第 %d 次校验失败: %v", i+1, err)
		}
	}
	if n := idp.fetches.Load(); n != 1 {
		t.Errorf("JWKS 被拉取 %d 次,期望 1 次(公钥应被缓存)", n)
	}
}

func TestOIDCThrottlesRefreshOnUnknownKid(t *testing.T) {
	// 未知 kid 触发刷新,但必须有节流 —— 否则攻击者用随机 kid 刷请求,
	// 就能让本系统对 IdP 发起放大攻击(每个伪造请求 → 一次 JWKS 拉取)。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	// 先做一次成功校验,把缓存填上(1 次拉取)
	good := idp.sign(t, tokenOpts{issuer: testIssuer, audience: testAud, subject: "x"})
	if _, err := v.Verify(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	before := idp.fetches.Load()

	for i := 0; i < 10; i++ {
		bad := idp.sign(t, tokenOpts{
			issuer: testIssuer, audience: testAud, subject: "x",
			kid: fmt.Sprintf("random-kid-%d", i),
		})
		_, _ = v.Verify(context.Background(), bad)
	}
	added := idp.fetches.Load() - before
	// minInterval 是 30s,所以 10 次未知 kid 最多再拉 1 次。
	if added > 1 {
		t.Errorf("10 次未知 kid 触发了 %d 次 JWKS 拉取 —— 节流失效,可被用于放大攻击", added)
	}
}

func TestOIDCRejectsWhenJWKSUnavailable(t *testing.T) {
	// IdP 不可用时必须**拒绝**,不能 fail-open。
	// fail-open 的后果是 IdP 一抖动,系统就对所有 token 放行。
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	idp.srv.Close() // IdP 彻底不可达

	raw := idp.sign(t, tokenOpts{issuer: testIssuer, audience: testAud, subject: "x"})
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("JWKS 不可用时必须拒绝(不能 fail-open)")
	}
}

func TestOIDCGarbageTokenRejected(t *testing.T) {
	idp := newFakeIdP(t)
	v := newVerifier(idp)
	for _, raw := range []string{"", "garbage", "a.b.c", strings.Repeat("x", 4096)} {
		if _, err := v.Verify(context.Background(), raw); err == nil {
			t.Errorf("垃圾 token %q 必须拒绝", raw[:min(len(raw), 12)])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
