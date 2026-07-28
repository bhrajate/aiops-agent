package server

// tls.go 实现 docs/SECURITY.md §3 约定的 mTLS 服务端形态。
//
// Cluster Agent 是 TLS 服务端,Tool Gateway(控制面)是客户端。启用时,agent 要求
// 并校验由配置的客户端 CA 签发的客户端证书(RequireAndVerifyClientCert);
// 未启用时保持明文,便于本地开发。

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// TLSConfig 保存来自 AIOPS_AGENT_TLS_* 的 mTLS 服务端配置。
type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
	ClientCA string
}

// TLSConfigFromEnv 读取 mTLS 服务端配置。
//
//	AIOPS_AGENT_TLS_ENABLED     是否启用 mTLS(默认 false)
//	AIOPS_AGENT_TLS_CERT        服务端证书(PEM)
//	AIOPS_AGENT_TLS_KEY         服务端私钥(PEM)
//	AIOPS_AGENT_TLS_CLIENT_CA   用于校验客户端证书的 CA 包(PEM)
func TLSConfigFromEnv() TLSConfig {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("AIOPS_AGENT_TLS_ENABLED")))
	return TLSConfig{
		Enabled:  enabled,
		CertFile: os.Getenv("AIOPS_AGENT_TLS_CERT"),
		KeyFile:  os.Getenv("AIOPS_AGENT_TLS_KEY"),
		ClientCA: os.Getenv("AIOPS_AGENT_TLS_CLIENT_CA"),
	}
}

// Build 校验配置并返回要求且校验客户端证书的 *tls.Config。TLS 未启用时返回
// nil(error 也为 nil),调用方据此回退到明文 ListenAndServe。
func (c TLSConfig) Build() (*tls.Config, error) {
	if !c.Enabled {
		return nil, nil
	}
	if c.CertFile == "" || c.KeyFile == "" {
		return nil, fmt.Errorf("mTLS enabled but AIOPS_AGENT_TLS_CERT / AIOPS_AGENT_TLS_KEY not set")
	}
	if c.ClientCA == "" {
		return nil, fmt.Errorf("mTLS enabled but AIOPS_AGENT_TLS_CLIENT_CA not set (client cert cannot be verified)")
	}

	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server keypair: %w", err)
	}

	caPEM, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("client CA %q contains no valid certificates", c.ClientCA)
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}, nil
}
