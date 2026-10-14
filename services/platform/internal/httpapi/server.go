package httpapi

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/store"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Server struct {
	Store              *store.Store
	Commerce           *commerce.Service
	Origin             string
	Secure             bool
	TrustedProxies     []string
	WebDir             string
	Preflight          func(context.Context, string, string, commerce.Credential) (any, error)
	Inventory          func(context.Context, string) (any, error)
	ResolveCard        func(context.Context, string, commerce.CardResolution) error
	ManualCheckout     func(context.Context, string, string) (string, error)
	RegenerateCheckout func(context.Context, string, string, bool) (string, error)
	ValidatePair       func(context.Context, commerce.CardRule) (map[int64]string, error)
	GPTPlans           func(context.Context) ([]commerce.UpstreamPlan, error)
}
type Principal struct{ ID, Role, Name, Email string }

// Unknown addresses still perform a bcrypt comparison to avoid exposing account existence by timing.
var unknownPasswordHash, _ = bcrypt.GenerateFromPassword([]byte(security.Token()), bcrypt.DefaultCost)

func (s *Server) Router() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.ContextWithFallback = true
	if err := r.SetTrustedProxies(s.TrustedProxies); err != nil {
		panic("invalid trusted proxy configuration")
	}
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) {
		c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "internal", "message": "请求未确认，请查询原记录"}})
	}))
	r.Use(func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 75*time.Second)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cache-Control", "no-store")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'")
		c.Next()
	})
	r.GET("/healthz", func(c *gin.Context) {
		if e := s.Store.DB.Ping(c); e != nil {
			bad(c, commerce.Fail("db_unavailable", "数据库不可用", 503))
			return
		}
		var fresh bool
		if e := s.Store.DB.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM worker_heartbeats WHERE updated_at>now()-interval '60 seconds')").Scan(&fresh); e != nil {
			bad(c, commerce.Fail("db_unavailable", "数据库结构或心跳查询不可用", 503))
			return
		}
		ok(c, gin.H{"ready": true, "worker_ready": fresh})
	})
	r.GET("/api/session", s.session)
	r.POST("/api/login", s.origin, s.login)
	r.POST("/api/invitations/accept", s.origin, s.accept)
	r.POST("/api/logout", s.origin, s.auth(false), s.logout)
	a := r.Group("/api", s.auth(false))
	a.GET("/overview", s.overview)
	a.GET("/products", s.products)
	a.GET("/orders", s.orders)
	a.GET("/orders/by-merchant/:number", s.orderByMerchant)
	a.GET("/orders/:id", s.order)
	a.POST("/orders", s.origin, s.channel, s.createOrder)
	a.POST("/preflight", s.origin, s.channel, s.preflight)
	a.GET("/ledger", s.ledger)
	a.GET("/keys", s.channel, s.keys)
	a.POST("/keys", s.origin, s.channel, s.createKey)
	a.POST("/keys/:id/revoke", s.origin, s.channel, s.revokeKey)
	a.GET("/webhook", s.channel, s.webhook)
	a.POST("/webhook", s.origin, s.channel, s.saveWebhook)
	a.GET("/notifications", s.notifications)
	admin := a.Group("/admin", s.admin)
	admin.GET("/channels", s.channels)
	admin.POST("/channels", s.origin, s.invite)
	admin.POST("/channels/:id/invite", s.origin, s.reinvite)
	admin.POST("/channels/:id/status", s.origin, s.channelStatus)
	admin.POST("/channels/:id/credit", s.origin, s.credit)
	admin.GET("/channels/:id/prices", s.channelPrices)
	admin.POST("/channels/:id/prices", s.origin, s.savePrice)
	admin.POST("/products/:id", s.origin, s.saveProduct)
	admin.GET("/gpt-plans", s.gptPlans)
	admin.GET("/provider", s.provider)
	admin.POST("/provider", s.origin, s.saveProvider)
	admin.GET("/card-rules", s.rules)
	admin.GET("/card-operations", s.cardOperations)
	admin.POST("/card-operations/resolve", s.origin, s.resolveCardOperation)
	admin.GET("/inventory", func(c *gin.Context) {
		if s.Inventory == nil {
			bad(c, commerce.Fail("unavailable", "卡台查询暂不可用", 503))
			return
		}
		data, e := s.Inventory(c, c.Query("kind"))
		if e != nil {
			bad(c, e)
			return
		}
		ok(c, data)
	})
	admin.POST("/card-rules", s.origin, s.saveRule)
	admin.GET("/x-accounts", s.accounts)
	admin.POST("/x-accounts", s.origin, s.saveAccount)
	admin.GET("/audit", s.audit)
	admin.POST("/orders/:id/payment-handoff", s.origin, s.paymentHandoff)
	admin.POST("/orders/:id/payment-regenerate", s.origin, s.paymentRegenerate)
	admin.POST("/orders/:id/reconcile", s.origin, func(c *gin.Context) {
		e := s.Store.Tx(c, func(tx pgx.Tx) error {
			result, e := tx.Exec(c, "UPDATE orders SET next_at=now() WHERE id=$1 AND status IN ('queued','running','unknown')", c.Param("id"))
			if e != nil {
				return e
			}
			if result.RowsAffected() != 1 {
				return commerce.Fail("order_terminal", "订单已结束或不存在", 409)
			}
			return store.Audit(c, tx, p(c).ID, "reconcile_original", c.Param("id"), "")
		})
		if e != nil {
			bad(c, e)
			return
		}
		ok(c, gin.H{"saved": true})
	})
	v := r.Group("/v1", s.auth(true))
	v.GET("/products", s.products)
	v.GET("/balance", s.balance)
	v.GET("/orders", s.orders)
	v.GET("/orders/by-merchant/:number", s.orderByMerchant)
	v.GET("/orders/:id", s.order)
	v.POST("/orders", s.createOrder)
	v.POST("/preflight", s.preflight)
	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != "GET" || strings.HasPrefix(c.Request.URL.Path, "/api") || strings.HasPrefix(c.Request.URL.Path, "/v1") {
			c.Status(404)
			return
		}
		path := strings.TrimPrefix(c.Request.URL.Path, "/")
		if strings.Contains(path, "..") || strings.Contains(path, "\\") {
			c.Status(404)
			return
		}
		if path != "" {
			if f, e := os.Stat(s.WebDir + "/" + path); e == nil && !f.IsDir() {
				c.File(s.WebDir + "/" + path)
				return
			}
		}
		c.File(s.WebDir + "/index.html")
	})
	return r
}
func ok(c *gin.Context, v any) { c.JSON(200, gin.H{"data": v}) }
func bad(c *gin.Context, e error) {
	var known *commerce.Error
	if errors.As(e, &known) {
		c.AbortWithStatusJSON(known.Status, gin.H{"error": gin.H{"code": known.Code, "message": known.Message}})
		return
	}
	c.AbortWithStatusJSON(500, gin.H{"error": gin.H{"code": "internal", "message": "请求未确认，请查询原记录"}})
}
func bind(c *gin.Context, v any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(v); e != nil {
		bad(c, commerce.Fail("invalid_input", "请求资料格式无效", 400))
		return false
	}
	if e := decoder.Decode(new(any)); e != io.EOF {
		bad(c, commerce.Fail("invalid_input", "请求只能包含一个 JSON 对象", 400))
		return false
	}
	return true
}
func p(c *gin.Context) Principal { return c.MustGet("principal").(Principal) }
func (s *Server) origin(c *gin.Context) {
	if c.GetHeader("Origin") != s.Origin {
		bad(c, commerce.Fail("invalid_origin", "请从平台页面操作", 403))
		return
	}
	c.Next()
}
func (s *Server) admin(c *gin.Context) {
	if p(c).Role != "admin" {
		bad(c, commerce.Fail("forbidden", "仅管理员可操作", 403))
		return
	}
	c.Next()
}
func (s *Server) channel(c *gin.Context) {
	if p(c).Role != "channel" {
		bad(c, commerce.Fail("channel_required", "请使用渠道账户", 403))
		return
	}
	c.Next()
}
func (s *Server) auth(signed bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var user Principal
		var e error
		if signed {
			e = s.signed(c, &user)
		} else {
			cookie, _ := c.Cookie("platform_session")
			e = s.Store.DB.QueryRow(c, "SELECT u.id,u.role,u.name,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'", security.Hash(cookie)).Scan(&user.ID, &user.Role, &user.Name, &user.Email)
		}
		if e != nil {
			if known := new(commerce.Error); errors.As(e, &known) {
				bad(c, e)
			} else {
				bad(c, commerce.Fail("unauthorized", "请登录或检查接口签名", 401))
			}
			return
		}
		c.Set("principal", user)
		if e = s.allowRate(c, "api:"+user.ID, 300, time.Minute); e != nil {
			bad(c, e)
			return
		}
		c.Next()
	}
}
func (s *Server) signed(c *gin.Context, user *Principal) error {
	ts := c.GetHeader("X-Timestamp")
	seconds, e := strconv.ParseInt(ts, 10, 64)
	now := time.Now().Unix()
	if e != nil || seconds < now-300 || seconds > now+300 {
		return commerce.Fail("invalid_timestamp", "签名时间超出有效窗口", 401)
	}
	nonce, key := c.GetHeader("X-Nonce"), c.GetHeader("X-Key-Id")
	if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{16,128}$`).MatchString(nonce) {
		return commerce.Fail("invalid_nonce", "Nonce 格式无效", 401)
	}
	var cipher string
	e = s.Store.DB.QueryRow(c, "SELECT u.id,u.role,u.name,u.email,k.secret_cipher FROM api_keys k JOIN users u ON u.id=k.user_id WHERE k.id=$1 AND NOT k.revoked AND u.role='channel' AND u.status='active'", key).Scan(&user.ID, &user.Role, &user.Name, &user.Email, &cipher)
	if e != nil {
		return e
	}
	secret, e := s.Store.Vault.Open("api-key:"+key, cipher)
	if e != nil {
		return e
	}
	body, e := io.ReadAll(c.Request.Body)
	if e != nil {
		return commerce.Fail("body_too_large", "请求体过大", 413)
	}
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
	sig := security.Signature(string(secret), c.Request.Method, c.Request.URL.EscapedPath(), c.Request.URL.Query(), ts, nonce, key, c.GetHeader("Idempotency-Key"), body)
	if !hmac.Equal([]byte(sig), []byte(c.GetHeader("X-Signature"))) {
		return commerce.Fail("invalid_signature", "接口签名无效", 401)
	}
	result, e := s.Store.DB.Exec(c, "INSERT INTO nonces(key_id,nonce,expires_at) VALUES($1,$2,now()+interval '10 minutes') ON CONFLICT DO NOTHING", key, nonce)
	if e != nil {
		return e
	}
	if result.RowsAffected() != 1 {
		return commerce.Fail("replayed_request", "请求已使用", 409)
	}
	return nil
}
func (s *Server) session(c *gin.Context) {
	token, _ := c.Cookie("platform_session")
	var u Principal
	e := s.Store.DB.QueryRow(c, "SELECT u.id,u.role,u.name,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.expires_at>now() AND u.status='active'", security.Hash(token)).Scan(&u.ID, &u.Role, &u.Name, &u.Email)
	if e != nil {
		ok(c, gin.H{"authenticated": false})
		return
	}
	ok(c, gin.H{"authenticated": true, "id": u.ID, "role": u.Role, "name": u.Name, "email": u.Email})
}
func (s *Server) login(c *gin.Context) {
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if b.Email == "" || b.Password == "" || len(b.Email) > 254 || len(b.Password) > 72 {
		bad(c, commerce.Fail("invalid_login", "邮箱或密码无效", 401))
		return
	}
	for _, limit := range []struct {
		bucket string
		max    int
	}{
		{"login-ip:" + c.ClientIP(), 60},
		{"login-account:" + b.Email, 10},
	} {
		if e := s.allowRate(c, limit.bucket, limit.max, 15*time.Minute); e != nil {
			bad(c, e)
			return
		}
	}
	var id, hash string
	e := s.Store.DB.QueryRow(c, "SELECT id,password_hash FROM users WHERE email=$1 AND status='active'", b.Email).Scan(&id, &hash)
	if hash == "" {
		hash = string(unknownPasswordHash)
	}
	passwordErr := bcrypt.CompareHashAndPassword([]byte(hash), []byte(b.Password))
	if e != nil || passwordErr != nil {
		bad(c, commerce.Fail("invalid_login", "邮箱或密码无效", 401))
		return
	}
	token := security.Token()
	_, e = s.Store.DB.Exec(c, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '12 hours')", security.Hash(token), id)
	if e != nil {
		bad(c, e)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie("platform_session", token, 43200, "/", "", s.Secure, true)
	ok(c, gin.H{"authenticated": true})
}
func (s *Server) logout(c *gin.Context) {
	token, _ := c.Cookie("platform_session")
	_, e := s.Store.DB.Exec(c, "DELETE FROM sessions WHERE token_hash=$1", security.Hash(token))
	if e != nil {
		bad(c, e)
		return
	}
	c.SetCookie("platform_session", "", -1, "/", "", s.Secure, true)
	ok(c, gin.H{"saved": true})
}
func (s *Server) accept(c *gin.Context) {
	var b struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !bind(c, &b) {
		return
	}
	if len(b.Token) != 64 || len(b.Password) < 12 || len(b.Password) > 72 {
		bad(c, commerce.Fail("invalid_invitation", "邀请或密码无效，密码需 12–72 字节", 400))
		return
	}
	if e := s.allowRate(c, "invitation:"+c.ClientIP(), 60, time.Hour); e != nil {
		bad(c, e)
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(b.Password), bcrypt.DefaultCost)
	if e != nil {
		bad(c, e)
		return
	}
	e = s.Store.Tx(c, func(tx pgx.Tx) error {
		var user string
		e := tx.QueryRow(c, "SELECT user_id FROM invitations WHERE token_hash=$1", security.Hash(b.Token)).Scan(&user)
		if e != nil {
			return commerce.Fail("invalid_invitation", "邀请已失效，请联系管理员重发", 400)
		}
		var status string
		if e = tx.QueryRow(c, "SELECT status FROM users WHERE id=$1 FOR UPDATE", user).Scan(&status); e != nil {
			return e
		}
		if status != "invited" && status != "active" {
			return commerce.Fail("channel_disabled", "账户已被禁用", 403)
		}
		if e = tx.QueryRow(c, "UPDATE invitations SET used_at=now() WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() RETURNING user_id", security.Hash(b.Token)).Scan(&user); e != nil {
			return commerce.Fail("invalid_invitation", "邀请已失效，请联系管理员重发", 400)
		}
		result, e := tx.Exec(c, "UPDATE users SET password_hash=$2,status='active' WHERE id=$1 AND status IN ('invited','active')", user, string(hash))
		if e != nil {
			return e
		}
		if result.RowsAffected() != 1 {
			return commerce.Fail("channel_disabled", "账户已被禁用", 403)
		}
		if _, e = tx.Exec(c, "DELETE FROM sessions WHERE user_id=$1", user); e != nil {
			return e
		}
		return store.Audit(c, tx, user, "accept_invitation", user, "")
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) queueInvite(ctx context.Context, tx pgx.Tx, id, email string) error {
	if _, e := tx.Exec(ctx, "UPDATE outbox SET status='dead',last_error='invitation_replaced' WHERE user_id=$1 AND kind='invitation' AND status='pending'", id); e != nil {
		return e
	}
	if _, e := tx.Exec(ctx, "UPDATE invitations SET used_at=now() WHERE user_id=$1 AND used_at IS NULL", id); e != nil {
		return e
	}
	token := security.Token()
	if _, e := tx.Exec(ctx, "INSERT INTO invitations(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '48 hours')", security.Hash(token), id); e != nil {
		return e
	}
	event := security.ID("mail")
	body, _ := json.Marshal(gin.H{"email": email, "url": s.Origin + "/join#" + token})
	_, e := tx.Exec(ctx, "INSERT INTO outbox(id,user_id,kind,payload_cipher) VALUES($1,$2,'invitation',$3)", event, id, s.Store.Vault.Seal("outbox:"+event, body))
	return e
}
func (s *Server) invite(c *gin.Context) {
	var b struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if !bind(c, &b) {
		return
	}
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	address, e := mail.ParseAddress(b.Email)
	if e != nil || address.Address != b.Email || len(b.Email) > 254 || len(b.Name) < 1 || len(b.Name) > 80 {
		bad(c, commerce.Fail("invalid_channel", "邮箱或名称格式无效", 400))
		return
	}
	id := security.ID("chn")
	e = s.Store.Tx(c, func(tx pgx.Tx) error {
		var exists bool
		if e := tx.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM users WHERE email=$1)", b.Email).Scan(&exists); e != nil {
			return e
		}
		if exists {
			return commerce.Fail("email_exists", "邮箱已存在，请从渠道详情重发邀请", 409)
		}
		if _, e := tx.Exec(c, "INSERT INTO users(id,email,name,role,status) VALUES($1,$2,$3,'channel','invited')", id, b.Email, b.Name); e != nil {
			return e
		}
		if _, e := tx.Exec(c, "INSERT INTO wallets(user_id) VALUES($1)", id); e != nil {
			return e
		}
		if e := s.queueInvite(c, tx, id, b.Email); e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "invite_channel", id, b.Email)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"id": id, "email_status": "pending"})
}
func (s *Server) reinvite(c *gin.Context) {
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		var email string
		e := tx.QueryRow(c, "SELECT email FROM users WHERE id=$1 AND role='channel' AND status IN ('invited','active') FOR UPDATE", c.Param("id")).Scan(&email)
		if e != nil {
			return commerce.Fail("channel_unavailable", "渠道不可用", 409)
		}
		if e = s.queueInvite(c, tx, c.Param("id"), email); e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "reinvite_channel", c.Param("id"), "")
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"email_status": "pending"})
}
func (s *Server) channelStatus(c *gin.Context) {
	var b struct {
		Status string `json:"status"`
	}
	if !bind(c, &b) {
		return
	}
	if b.Status != "banned" && b.Status != "active" && b.Status != "deleted" {
		bad(c, commerce.Fail("invalid_status", "账户状态无效", 400))
		return
	}
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		var current, hash string
		e := tx.QueryRow(c, "SELECT status,password_hash FROM users WHERE id=$1 AND role='channel' FOR UPDATE", c.Param("id")).Scan(&current, &hash)
		if e != nil || current == "deleted" {
			return commerce.Fail("channel_missing", "渠道不存在", 404)
		}
		if b.Status == "active" && hash == "" {
			return commerce.Fail("invitation_required", "请先完成邀请开户", 409)
		}
		if b.Status == "deleted" {
			var available, frozen int64
			if e = tx.QueryRow(c, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id=$1 FOR UPDATE", c.Param("id")).Scan(&available, &frozen); e != nil {
				return e
			}
			var pending bool
			if e = tx.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status NOT IN ('succeeded','failed'))", c.Param("id")).Scan(&pending); e != nil {
				return e
			}
			if available != 0 || frozen != 0 || pending {
				return commerce.Fail("channel_not_empty", "有余额或未完成订单，暂不能删除", 409)
			}
		}
		if _, e = tx.Exec(c, "UPDATE users SET status=$2 WHERE id=$1", c.Param("id"), b.Status); e != nil {
			return e
		}
		if b.Status != "active" {
			if _, e = tx.Exec(c, "DELETE FROM sessions WHERE user_id=$1", c.Param("id")); e != nil {
				return e
			}
			if _, e = tx.Exec(c, "UPDATE invitations SET used_at=now() WHERE user_id=$1 AND used_at IS NULL", c.Param("id")); e != nil {
				return e
			}
		}
		return store.Audit(c, tx, p(c).ID, "channel."+b.Status, c.Param("id"), "")
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) credit(c *gin.Context) {
	var b struct {
		Amount    int64  `json:"amount_minor"`
		Reference string `json:"reference"`
		Note      string `json:"note"`
	}
	if !bind(c, &b) {
		return
	}
	if e := s.Commerce.Credit(c, p(c).ID, c.Param("id"), b.Reference, b.Note, b.Amount); e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) query(c *gin.Context, sql string, args ...any) {
	rows, e := s.Store.Rows(c, sql, args...)
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, rows)
}
func (s *Server) channels(c *gin.Context) {
	limit, offset := page(c)
	s.query(c, "SELECT u.id,u.email,u.name,u.status,u.created_at,w.available_minor,w.frozen_minor FROM users u JOIN wallets w ON w.user_id=u.id WHERE u.role='channel' AND u.status<>'deleted' AND ($1='' OR u.email ILIKE '%'||$1||'%' OR u.name ILIKE '%'||$1||'%' OR u.id=$1) ORDER BY u.created_at DESC,u.id DESC LIMIT $2 OFFSET $3", c.Query("search"), limit, offset)
}
func (s *Server) overview(c *gin.Context) {
	u := p(c)
	var total, pending, success, volume int64
	e := s.Store.DB.QueryRow(c, "SELECT count(*),count(*) FILTER(WHERE status IN ('queued','running','unknown')),count(*) FILTER(WHERE status='succeeded'),COALESCE(sum(supply_minor) FILTER(WHERE status='succeeded'),0) FROM orders WHERE ($1='admin' OR user_id=$2)", u.Role, u.ID).Scan(&total, &pending, &success, &volume)
	if e != nil {
		bad(c, e)
		return
	}
	wallet := gin.H{}
	if u.Role == "channel" {
		var available, frozen int64
		if e = s.Store.DB.QueryRow(c, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id=$1", u.ID).Scan(&available, &frozen); e != nil {
			bad(c, e)
			return
		}
		wallet = gin.H{"available_minor": available, "frozen_minor": frozen}
	}
	ok(c, gin.H{"total": total, "pending": pending, "succeeded": success, "volume_minor": volume, "wallet": wallet})
}
func (s *Server) balance(c *gin.Context) {
	var a, f int64
	if e := s.Store.DB.QueryRow(c, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id=$1", p(c).ID).Scan(&a, &f); e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"currency": "CNY", "available_minor": a, "frozen_minor": f})
}
func (s *Server) products(c *gin.Context) {
	var rows []map[string]any
	var err error
	if p(c).Role == "admin" {
		rows, err = s.Store.Rows(c, "SELECT * FROM products ORDER BY sort_order,code")
	} else {
		rows, err = s.Store.Rows(c, "SELECT p.code,p.name,p.business,COALESCE(cp.supply_minor,p.supply_minor) supply_minor,p.enabled,p.version FROM products p LEFT JOIN channel_prices cp ON cp.product_code=p.code AND cp.user_id=$1 ORDER BY p.sort_order,p.code", p(c).ID)
	}
	if err != nil {
		bad(c, err)
		return
	}
	if err = s.catalogReadiness(c, rows); err != nil {
		bad(c, err)
		return
	}
	ok(c, rows)
}
func (s *Server) orders(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if offset < 0 || offset > 1000000 {
		offset = 0
	}
	s.query(c, "SELECT "+commerce.OrderColumns+" FROM orders WHERE ($1='admin' OR user_id=$2) AND ($3='' OR status=$3) AND ($4='' OR merchant_order_no ILIKE '%'||$4||'%' OR id=$4) ORDER BY created_at DESC,id DESC LIMIT $5 OFFSET $6", p(c).Role, p(c).ID, c.Query("status"), c.Query("search"), limit, offset)
}
func (s *Server) order(c *gin.Context) {
	o, e := commerce.ScanOrder(s.Store.DB.QueryRow(c, "SELECT "+commerce.OrderColumns+" FROM orders WHERE id=$1 AND ($2='admin' OR user_id=$3)", c.Param("id"), p(c).Role, p(c).ID))
	if e != nil {
		if !errors.Is(e, pgx.ErrNoRows) {
			bad(c, e)
			return
		}
		bad(c, commerce.Fail("order_missing", "订单不存在", 404))
		return
	}
	ok(c, o)
}
func (s *Server) createOrder(c *gin.Context) {
	var b commerce.Request
	if !bind(c, &b) {
		return
	}
	o, created, e := s.Commerce.Create(c, p(c).ID, c.GetHeader("Idempotency-Key"), b)
	if e != nil {
		bad(c, e)
		return
	}
	status := 200
	if created {
		status = 201
	}
	c.JSON(status, gin.H{"data": o})
}
func (s *Server) preflight(c *gin.Context) {
	if e := s.allowRate(c, "preflight:"+p(c).ID, 12, time.Minute); e != nil {
		bad(c, e)
		return
	}
	var b commerce.Request
	if !bind(c, &b) {
		return
	}
	var business string
	e := s.Store.DB.QueryRow(c, "SELECT business FROM products WHERE code=$1 AND enabled", b.ProductCode).Scan(&business)
	if e != nil {
		bad(c, commerce.Fail("product_disabled", "商品暂未开放", 409))
		return
	}
	b.MerchantOrderNo = "preflight"
	if e = commerce.Validate(&b, business, "preflight"); e != nil {
		bad(c, e)
		return
	}
	if s.Preflight == nil {
		bad(c, commerce.Fail("unavailable", "预检服务不可用", 503))
		return
	}
	v, e := s.Preflight(c, b.ProductCode, b.Recipient, b.Credential)
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, v)
}
func (s *Server) ledger(c *gin.Context) {
	limit, offset := page(c)
	s.query(c, "SELECT id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,created_at FROM ledger WHERE ($1='admin' OR user_id=$2) AND ($3='' OR reference ILIKE '%'||$3||'%' OR order_id=$3) ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5", p(c).Role, p(c).ID, c.Query("search"), limit, offset)
}
func (s *Server) keys(c *gin.Context) {
	s.query(c, "SELECT id,label,revoked,created_at FROM api_keys WHERE user_id=$1 ORDER BY created_at DESC", p(c).ID)
}
func (s *Server) createKey(c *gin.Context) {
	var b struct {
		Label string `json:"label"`
	}
	if !bind(c, &b) {
		return
	}
	if len(b.Label) < 1 || len(b.Label) > 80 {
		bad(c, commerce.Fail("invalid_label", "密钥名称无效", 400))
		return
	}
	id, secret := security.ID("key"), security.Token()
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		_, e := tx.Exec(c, "INSERT INTO api_keys(id,user_id,label,secret_cipher) VALUES($1,$2,$3,$4)", id, p(c).ID, b.Label, s.Store.Vault.Seal("api-key:"+id, []byte(secret)))
		if e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "create_key", id, b.Label)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"id": id, "secret": secret})
}
func (s *Server) revokeKey(c *gin.Context) {
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		result, e := tx.Exec(c, "UPDATE api_keys SET revoked=true WHERE id=$1 AND user_id=$2", c.Param("id"), p(c).ID)
		if e != nil {
			return e
		}
		if result.RowsAffected() != 1 {
			return commerce.Fail("key_missing", "密钥不存在", 404)
		}
		return store.Audit(c, tx, p(c).ID, "revoke_key", c.Param("id"), "")
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) webhook(c *gin.Context) {
	rows, e := s.Store.Rows(c, "SELECT url FROM webhook_configs WHERE user_id=$1", p(c).ID)
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, rows)
}
func (s *Server) saveWebhook(c *gin.Context) {
	var b struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	if !bind(c, &b) {
		return
	}
	target, e := url.Parse(b.URL)
	if e != nil || target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Fragment != "" || len(b.URL) > 2048 || len(b.Secret) < 32 || len(b.Secret) > 256 {
		bad(c, commerce.Fail("invalid_webhook", "请填写 HTTPS 回调地址和至少 32 字符的密钥", 400))
		return
	}
	_, e = s.Store.DB.Exec(c, "INSERT INTO webhook_configs(user_id,url,secret_cipher) VALUES($1,$2,$3) ON CONFLICT(user_id) DO UPDATE SET url=excluded.url,secret_cipher=excluded.secret_cipher", p(c).ID, b.URL, s.Store.Vault.Seal("webhook:"+p(c).ID, []byte(b.Secret)))
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) notifications(c *gin.Context) {
	limit, offset := page(c)
	s.query(c, "SELECT id,user_id,kind,status,attempts,last_error,next_at,created_at FROM outbox WHERE ($1='admin' OR user_id=$2) AND ($3='' OR id=$3 OR user_id=$3) ORDER BY created_at DESC,id DESC LIMIT $4 OFFSET $5", p(c).Role, p(c).ID, c.Query("search"), limit, offset)
}
func (s *Server) audit(c *gin.Context) {
	limit, offset := page(c)
	s.query(c, "SELECT * FROM audit WHERE ($1='' OR target ILIKE '%'||$1||'%' OR action ILIKE '%'||$1||'%' OR actor_id=$1) ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3", c.Query("search"), limit, offset)
}
func (s *Server) channelPrices(c *gin.Context) {
	s.query(c, "SELECT p.code,p.name,p.supply_minor default_minor,cp.supply_minor FROM products p LEFT JOIN channel_prices cp ON cp.product_code=p.code AND cp.user_id=$1 ORDER BY p.sort_order,p.code", c.Param("id"))
}
func (s *Server) savePrice(c *gin.Context) {
	var b struct {
		Code   string `json:"product_code"`
		Amount *int64 `json:"supply_minor"`
	}
	if !bind(c, &b) {
		return
	}
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		var active bool
		if e := tx.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='channel' AND status<>'deleted')", c.Param("id")).Scan(&active); e != nil {
			return e
		}
		if !active {
			return commerce.Fail("channel_missing", "渠道不存在", 404)
		}
		var e error
		if b.Amount == nil {
			_, e = tx.Exec(c, "DELETE FROM channel_prices WHERE user_id=$1 AND product_code=$2", c.Param("id"), b.Code)
		} else {
			if *b.Amount <= 0 || *b.Amount > 1000000000 {
				return commerce.Fail("invalid_price", "供货价无效", 400)
			}
			_, e = tx.Exec(c, "INSERT INTO channel_prices(user_id,product_code,supply_minor) VALUES($1,$2,$3) ON CONFLICT(user_id,product_code) DO UPDATE SET supply_minor=excluded.supply_minor", c.Param("id"), b.Code, *b.Amount)
		}
		if e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "channel_price", c.Param("id"), b.Code)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) saveProduct(c *gin.Context) {
	var b struct {
		Name    string         `json:"name"`
		Price   int64          `json:"supply_minor"`
		Enabled bool           `json:"enabled"`
		Plan    *string        `json:"upstream_plan"`
		Config  map[string]any `json:"config"`
	}
	if !bind(c, &b) {
		return
	}
	if len(b.Name) < 1 || len(b.Name) > 100 || b.Price < 0 || b.Price > 1000000000 || b.Enabled && b.Price == 0 || b.Plan != nil && len(*b.Plan) > 80 {
		bad(c, commerce.Fail("invalid_product", "商品名称或价格无效", 400))
		return
	}
	var business, currentPlan string
	var version int64
	var currentConfig map[string]any
	if err := s.Store.DB.QueryRow(c, "SELECT business,upstream_plan,version,config FROM products WHERE code=$1", c.Param("id")).Scan(&business, &currentPlan, &version, &currentConfig); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = commerce.Fail("product_missing", "商品不存在", 404)
		}
		bad(c, err)
		return
	}
	if b.Config == nil {
		b.Config = currentConfig
	}
	if business == "x" && b.Enabled {
		if err := commerce.ValidateXProduct(c.Param("id"), b.Config); err != nil {
			bad(c, err)
			return
		}
	}
	if business != "x" {
		b.Config = currentConfig
	}
	selected := currentPlan
	if b.Plan != nil {
		selected = *b.Plan
	}
	if business == "gpt" && (b.Enabled || selected != "" && selected != currentPlan) {
		if selected == "" {
			bad(c, commerce.Fail("plan_missing", "请先选择读取到的上游套餐", 400))
			return
		}
		plans, err := s.readGPTPlans(c)
		if err != nil {
			bad(c, err)
			return
		}
		valid := false
		for _, plan := range plans {
			if plan.Code == selected && (!b.Enabled || plan.Enabled && plan.Purchasable) {
				valid = true
				break
			}
		}
		if !valid {
			bad(c, commerce.Fail("plan_unavailable", "所选套餐已变更或暂停，请刷新上游套餐后重新选择", 409))
			return
		}
	}
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		result, e := tx.Exec(c, "UPDATE products SET name=$2,supply_minor=$3,enabled=$4,upstream_plan=COALESCE($5,upstream_plan),config=$7,version=version+1 WHERE code=$1 AND version=$6 AND (business<>'gpt' OR NOT $4 OR COALESCE($5,upstream_plan)<>'')", c.Param("id"), b.Name, b.Price, b.Enabled, b.Plan, version, b.Config)
		if e != nil {
			return e
		}
		if result.RowsAffected() != 1 {
			return commerce.Fail("product_changed", "商品配置已变更，请刷新后重新保存", 409)
		}
		return store.Audit(c, tx, p(c).ID, "product", c.Param("id"), "")
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) provider(c *gin.Context) {
	provider, e := s.Commerce.Provider(c)
	if e != nil {
		var known *commerce.Error
		if !errors.As(e, &known) || known.Code != "provider_missing" {
			bad(c, e)
			return
		}
		ok(c, gin.H{"configured": false})
		return
	}
	ok(c, gin.H{"configured": true, "environment": provider.Environment, "api_base": provider.APIBase, "app_id": provider.AppID, "writes_enabled": provider.WritesEnabled, "payment_country": provider.PaymentCountry, "payment_currency": provider.PaymentCurrency, "billing": provider.Billing, "has_api_key": provider.APIKey != "", "has_stripe_key": provider.StripeKey != "", "has_x_bearer": provider.XBearer != ""})
}
func (s *Server) saveProvider(c *gin.Context) {
	var b commerce.ProviderConfig
	if !bind(c, &b) {
		return
	}
	old, oldErr := s.Commerce.Provider(c)
	if oldErr != nil {
		var known *commerce.Error
		if !errors.As(oldErr, &known) || known.Code != "provider_missing" {
			bad(c, oldErr)
			return
		}
	}
	if err := commerce.ValidateCardBase(b.APIBase); err != nil {
		bad(c, err)
		return
	}
	if b.APIKey == "" && b.Environment == old.Environment && b.APIBase == old.APIBase && b.AppID == old.AppID {
		b.APIKey = old.APIKey
	}
	if b.StripeKey == "" {
		b.StripeKey = old.StripeKey
	}
	if b.XBearer == "" {
		b.XBearer = old.XBearer
	}
	if b.Environment != "sandbox" && b.Environment != "production" || !regexp.MustCompile(`^sk_[A-Za-z0-9_-]+$`).MatchString(b.APIKey) || b.PaymentCountry != "" && !regexp.MustCompile(`^[A-Z]{2}$`).MatchString(b.PaymentCountry) || b.PaymentCurrency != "" && !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(b.PaymentCurrency) {
		bad(c, commerce.Fail("invalid_provider", "卡台环境、密钥或支付地区格式无效", 400))
		return
	}
	data, _ := json.Marshal(b)
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		_, e := tx.Exec(c, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1) ON CONFLICT(id) DO UPDATE SET payload_cipher=excluded.payload_cipher,version=settings.version+1,updated_at=now()", s.Store.Vault.Seal("setting:provider", data))
		if e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "provider_config", "provider", b.Environment)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) rules(c *gin.Context) {
	provider, _ := s.Commerce.Provider(c)
	s.query(c, `SELECT r.*,COALESCE((SELECT json_agg(json_build_object('slot',p.slot,'card_id',p.card_id,'last_four',p.last_four,
 'declines',(SELECT count(*) FROM card_declines d WHERE d.environment=p.environment AND d.card_id=p.card_id),
 'replacement_status',COALESCE(m.status,''),'replacement_error',COALESCE(m.last_error,'')) ORDER BY p.slot)
 FROM card_pool_slots p LEFT JOIN card_replacements m ON m.environment=p.environment AND m.old_card_id=p.card_id
 WHERE p.rule_id=r.id AND p.environment=$1),'[]') pool FROM card_rules r ORDER BY business,product_code`, provider.Environment)
}
func (s *Server) saveRule(c *gin.Context) {
	var b commerce.CardRule
	if !bind(c, &b) {
		return
	}
	if b.Business != "x" && b.Business != "gpt" || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(b.CardProduct) || b.CardID < 0 || b.BackupCardID < 0 || b.BackupCardID > 0 && (b.Business != "x" || b.CardID <= 0 || b.CardID == b.BackupCardID || !b.AutoOpen || !b.ReplaceOnDecline) || b.FundingMinor <= 0 || b.FundingMinor > 10000000 || b.MaxOrderMinor <= 0 || b.MaxOrderMinor > b.FundingMinor || b.RefillThresholdMinor < 0 || b.RefillThresholdMinor > 10000000 || b.OpenAmountMinor < 0 || b.OpenAmountMinor > 10000000 || b.OpenAmountMinor > 0 && b.DailyLimitMinor < b.OpenAmountMinor+b.MaxOpenFeeMinor || b.MaxReplacements < 0 || b.MaxReplacements > 5 || b.ReplaceOnDecline && (b.Business != "x" || !b.AutoOpen || b.MaxReplacements == 0) || b.MaxOpenFeeMinor < 0 || b.MaxOpenFeeMinor > 100000 || b.DailyLimitMinor < b.FundingMinor+b.MaxOpenFeeMinor || b.DailyLimitMinor > 1000000000 {
		bad(c, commerce.Fail("invalid_rule", "卡规则或美元分额度无效", 400))
		return
	}
	pair := map[int64]string{}
	environment := ""
	if b.BackupCardID > 0 {
		if s.ValidatePair == nil {
			bad(c, commerce.Fail("card_pair_unavailable", "主备卡校验暂不可用", 503))
			return
		}
		var err error
		pair, err = s.ValidatePair(c, b)
		if err != nil {
			bad(c, err)
			return
		}
		provider, err := s.Commerce.Provider(c)
		if err != nil {
			bad(c, err)
			return
		}
		environment = provider.Environment
	}
	id := security.ID("rule")
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		if b.ProductCode != "" {
			var match bool
			if e := tx.QueryRow(c, "SELECT EXISTS(SELECT 1 FROM products WHERE code=$1 AND business=$2)", b.ProductCode, b.Business).Scan(&match); e != nil {
				return e
			}
			if !match {
				return commerce.Fail("invalid_product", "商品与业务不匹配", 400)
			}
		}
		if _, err := tx.Exec(c, "SELECT pg_advisory_xact_lock(hashtext($1))", "rule-config:"+b.Business+":"+b.ProductCode); err != nil {
			return err
		}
		if err := s.guardPairChange(c, tx, b); err != nil {
			return err
		}
		e := tx.QueryRow(c, "INSERT INTO card_rules(id,business,product_code,card_product,card_id,auto_open,funding_minor,max_order_minor,daily_limit_minor,enabled,max_open_fee_minor,use_provider_limits,refill_threshold_minor,open_amount_minor,replace_on_decline,max_replacements,backup_card_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) ON CONFLICT(business,product_code) DO UPDATE SET card_product=excluded.card_product,card_id=excluded.card_id,auto_open=excluded.auto_open,funding_minor=excluded.funding_minor,max_order_minor=excluded.max_order_minor,daily_limit_minor=excluded.daily_limit_minor,enabled=excluded.enabled,max_open_fee_minor=excluded.max_open_fee_minor,use_provider_limits=excluded.use_provider_limits,refill_threshold_minor=excluded.refill_threshold_minor,open_amount_minor=excluded.open_amount_minor,replace_on_decline=excluded.replace_on_decline,max_replacements=excluded.max_replacements,backup_card_id=excluded.backup_card_id,version=card_rules.version+1 RETURNING id", id, b.Business, b.ProductCode, b.CardProduct, b.CardID, b.AutoOpen, b.FundingMinor, b.MaxOrderMinor, b.DailyLimitMinor, b.Enabled, b.MaxOpenFeeMinor, b.UseProviderLimits, b.RefillThresholdMinor, b.OpenAmountMinor, b.ReplaceOnDecline, b.MaxReplacements, b.BackupCardID).Scan(&id)
		if e != nil {
			return e
		}
		if b.BackupCardID > 0 {
			if _, err := tx.Exec(c, "DELETE FROM card_pool_slots WHERE rule_id=$1 AND environment=$2", id, environment); err != nil {
				return err
			}
			for i, card := range []int64{b.CardID, b.BackupCardID} {
				if _, err := tx.Exec(c, "INSERT INTO card_pool_slots(rule_id,environment,slot,card_product,card_id,last_four) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(rule_id,environment,slot) DO UPDATE SET card_id=excluded.card_id,card_product=excluded.card_product,last_four=excluded.last_four,updated_at=now()", id, environment, i+1, b.CardProduct, card, pair[card]); err != nil {
					return err
				}
			}
		}
		return store.Audit(c, tx, p(c).ID, "card_rule", b.Business, b.CardProduct)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
func (s *Server) accounts(c *gin.Context) {
	s.query(c, "SELECT id,name,daily_limit,enabled FROM x_accounts ORDER BY name")
}
func (s *Server) saveAccount(c *gin.Context) {
	var b struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		AuthToken  string `json:"auth_token"`
		CT0        string `json:"ct0"`
		Proxy      string `json:"proxy"`
		Daily      int    `json:"daily_limit"`
		Enabled    bool   `json:"enabled"`
		ClearProxy bool   `json:"clear_proxy"`
	}
	if !bind(c, &b) {
		return
	}
	if b.ID != "" {
		var cipher string
		if e := s.Store.DB.QueryRow(c, "SELECT payload_cipher FROM x_accounts WHERE id=$1", b.ID).Scan(&cipher); e != nil {
			bad(c, commerce.Fail("account_missing", "X 账号不存在", 404))
			return
		}
		data, e := s.Store.Vault.Open("x-account:"+b.ID, cipher)
		if e != nil {
			bad(c, e)
			return
		}
		var old map[string]string
		if e = json.Unmarshal(data, &old); e != nil {
			bad(c, e)
			return
		}
		if b.AuthToken == "" {
			b.AuthToken = old["auth_token"]
		}
		if b.CT0 == "" {
			b.CT0 = old["ct0"]
		}
		if b.ClearProxy {
			b.Proxy = ""
		}
		if b.Proxy == "" && !b.ClearProxy {
			b.Proxy = old["proxy"]
		}
	}
	if len(b.Name) < 1 || len(b.Name) > 80 || b.Daily < 1 || b.Daily > 300 || len(b.AuthToken) < 1 || len(b.CT0) < 1 || len(b.AuthToken)+len(b.CT0) > 1024 || strings.ContainsAny(b.AuthToken+b.CT0, ";\r\n ") {
		bad(c, commerce.Fail("invalid_account", "X 账号资料无效", 400))
		return
	}
	if b.Proxy != "" {
		u, e := url.Parse(b.Proxy)
		if e != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "socks5" {
			bad(c, commerce.Fail("invalid_proxy", "代理需为 HTTP 或 SOCKS5 地址", 400))
			return
		}
	}
	if b.ID == "" {
		b.ID = security.ID("xac")
	}
	data, _ := json.Marshal(gin.H{"auth_token": b.AuthToken, "ct0": b.CT0, "proxy": b.Proxy})
	e := s.Store.Tx(c, func(tx pgx.Tx) error {
		_, e := tx.Exec(c, "INSERT INTO x_accounts(id,name,payload_cipher,daily_limit,enabled) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO UPDATE SET name=excluded.name,payload_cipher=excluded.payload_cipher,daily_limit=excluded.daily_limit,enabled=excluded.enabled", b.ID, b.Name, s.Store.Vault.Seal("x-account:"+b.ID, data), b.Daily, b.Enabled)
		if e != nil {
			return e
		}
		return store.Audit(c, tx, p(c).ID, "x_account", b.ID, b.Name)
	})
	if e != nil {
		bad(c, e)
		return
	}
	ok(c, gin.H{"saved": true})
}
