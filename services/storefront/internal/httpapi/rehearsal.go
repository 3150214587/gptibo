package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"strings"
)

// Rehearsals expose the imported copy only to an existing staging administrator.
// No login emails, customer/partner sessions, callbacks or business writes run.
func rehearsalGuard(opts Options) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Migration-Rehearsal", "read-only")
		path := c.Request.URL.Path
		if path == "/healthz" || path == "/livez" {
			c.Next()
			return
		}
		if c.Request.Method != http.MethodGet || !strings.HasPrefix(path, "/api/admin/") {
			c.AbortWithStatusJSON(403, gin.H{"error": "迁移验收站仅支持管理员只读查看，交易、回调和通知均已关闭。"})
			return
		}
		if path != "/api/admin/session" {
			name := "sf_admin"
			if strings.HasPrefix(opts.Origin, "https://") {
				name = "__Host-sf_admin"
			}
			token, _ := c.Cookie(name)
			if _, err := opts.Auth.Require(c.Request.Context(), opts.Auth.Store.DB, "admin", token); err != nil {
				fail(c, err)
				return
			}
		}
		original := c.Writer
		captured := &rehearsalResponse{ResponseWriter: original}
		c.Writer = captured
		c.Next()
		c.Writer = original
		data := captured.body.Bytes()
		if strings.Contains(original.Header().Get("Content-Type"), "application/json") {
			var value any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if decoder.Decode(&value) == nil {
				hidePaymentLinks(value)
				if encoded, err := json.Marshal(value); err == nil {
					data = encoded
				}
			}
		}
		original.Header().Del("Content-Length")
		_, _ = original.Write(data)
	}
}

type rehearsalResponse struct {
	gin.ResponseWriter
	body bytes.Buffer
}

func (w *rehearsalResponse) Write(b []byte) (int, error)       { return w.body.Write(b) }
func (w *rehearsalResponse) WriteString(s string) (int, error) { return w.body.WriteString(s) }
func hidePaymentLinks(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			switch k {
			case "qr", "paymentUrl", "paymentQr", "payment_url", "payment_qr":
				x[k] = nil
			case "canSubmit", "checkoutEnabled", "testingEnabled":
				x[k] = false
			default:
				hidePaymentLinks(value)
			}
		}
	case []any:
		for _, value := range x {
			hidePaymentLinks(value)
		}
	}
}
