package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWKSVerifier 通过 IdP 的 JWKS 端点验证 RS256 JWT(SECURITY §1,生产模式)。
// 缓存公钥并按 TTL 刷新;实现 OIDCVerifier 接口。
type JWKSVerifier struct {
	jwksURL  string
	issuer   string
	audience string
	http     *http.Client

	mu          sync.RWMutex
	keys        map[string]*rsa.PublicKey
	fetched     time.Time
	ttl         time.Duration
	lastRefresh time.Time     // 最近一次(含失败)刷新时间,用于节流
	minInterval time.Duration // 未知 kid 触发刷新的最小间隔(防 DoS 放大)
	refreshing  bool          // singleflight:同一时刻只允许一个刷新
}

// NewJWKSVerifier 创建验证器。issuer/audience 为空表示不校验对应 claim(不推荐)。
func NewJWKSVerifier(jwksURL, issuer, audience string) *JWKSVerifier {
	return &JWKSVerifier{
		jwksURL:     jwksURL,
		issuer:      issuer,
		audience:    audience,
		http:        &http.Client{Timeout: 5 * time.Second},
		keys:        map[string]*rsa.PublicKey{},
		ttl:         10 * time.Minute,
		minInterval: 30 * time.Second, // 未知 kid 最多每 30s 拉一次 IdP
	}
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Use string `json:"use"`
}

func (v *JWKSVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch status %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	// 限制 JWKS 响应大小,防止恶意/异常 IdP 返回超大 body 打爆内存
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return err
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pk, err := jwkToRSA(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pk
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = time.Now()
	v.mu.Unlock()
	return nil
}

func jwkToRSA(k jwk) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}

func (v *JWKSVerifier) keyForKid(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	pk, ok := v.keys[kid]
	stale := time.Since(v.fetched) > v.ttl
	throttled := time.Since(v.lastRefresh) < v.minInterval
	refreshing := v.refreshing
	v.mu.RUnlock()
	if ok && !stale {
		return pk, nil
	}
	// 未命中或过期:节流刷新,防止随机 kid token 每请求打一次 IdP(DoS 放大)。
	// 距上次刷新不足 minInterval,或已有刷新在途 → 直接用当前缓存判定,不再拉取。
	if throttled || refreshing {
		if !ok {
			return nil, fmt.Errorf("unknown kid %q (jwks refresh throttled)", kid)
		}
		return pk, nil
	}

	v.mu.Lock()
	// 双检:可能在等锁期间已有别的 goroutine 刷新
	if v.refreshing || time.Since(v.lastRefresh) < v.minInterval {
		v.mu.Unlock()
		v.mu.RLock()
		pk, ok = v.keys[kid]
		v.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown kid %q (jwks refresh in-flight)", kid)
		}
		return pk, nil
	}
	v.refreshing = true
	v.lastRefresh = time.Now()
	v.mu.Unlock()

	err := v.refresh(ctx)

	v.mu.Lock()
	v.refreshing = false
	v.mu.Unlock()

	if err != nil && !ok {
		return nil, err
	}
	v.mu.RLock()
	pk, ok = v.keys[kid]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown kid %q", kid)
	}
	return pk, nil
}

// Verify 实现 OIDCVerifier:校验 RS256 签名 + issuer + audience + 过期。
func (v *JWKSVerifier) Verify(ctx context.Context, raw string) (Principal, error) {
	claims := &customClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		kid, _ := t.Header["kid"].(string)
		return v.keyForKid(ctx, kid)
	}, jwt.WithIssuer(v.issuer), jwt.WithAudience(v.audience),
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithLeeway(60*time.Second))
	if err != nil {
		return Principal{}, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return Principal{
		Subject:    claims.Subject,
		Email:      claims.Email,
		Roles:      claims.Roles,
		Clusters:   claims.Clusters,
		Namespaces: claims.Namespaces,
	}, nil
}
