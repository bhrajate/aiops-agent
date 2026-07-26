// Package httpx 提供 HTTP 响应辅助与统一错误体。
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func Error(w http.ResponseWriter, status int, code, message string) {
	var b errBody
	b.Error.Code = code
	b.Error.Message = message
	JSON(w, status, b)
}

// Decode 解析请求 JSON body,失败写 400。
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
	if err := dec.Decode(v); err != nil {
		Error(w, http.StatusBadRequest, "bad_request", "invalid json body: "+err.Error())
		return false
	}
	return true
}

// CORS 开发期允许前端跨域(生产由网关处理)。
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Idempotency-Key,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Logging 简单访问日志中间件。
func Logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		log.Debug("http", "method", r.Method, "path", r.URL.Path)
	})
}
