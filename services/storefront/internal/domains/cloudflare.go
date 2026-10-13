package domains

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Record struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}
type Hostname struct {
	ID                 string   `json:"id"`
	Hostname           string   `json:"hostname"`
	Status             string   `json:"status"`
	VerificationErrors []string `json:"verification_errors"`
	SSL                struct {
		Status            string `json:"status"`
		ValidationRecords []struct {
			Name  string `json:"txt_name"`
			Value string `json:"txt_value"`
		} `json:"validation_records"`
	} `json:"ssl"`
	Ownership struct {
		Name  string `json:"name"`
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"ownership_verification"`
}

// Provider routing approval is separate from both matching DNS addresses and DCV.
// Translate known errors instead of exposing arbitrary provider responses.
func (h Hostname) PendingState() (string, string) {
	if h.Status != "active" {
		for _, message := range h.VerificationErrors {
			message = strings.ToLower(message)
			if strings.Contains(message, "does not cname to this zone") || strings.Contains(message, "dns target needs to point to the saas zone") {
				if isApex(h.Hostname) {
					return "pending_dns", "Cloudflare 尚未确认根域名接入，证书无法继续签发。即使 IP 相同，第三方 DNS 的 ALIAS/扁平化也可能无法通过。建议将 DNS 托管到 Cloudflare，添加 @ CNAME 指向接入地址并设为“仅 DNS”；或改绑 shop 等子域名并添加普通 CNAME。不要直接填写 Cloudflare IP。"
				}
				return "pending_dns", "Cloudflare 尚未确认域名接入。请将该子域名的 CNAME 指向接入地址，关闭该记录的代理和 CNAME 扁平化，等待解析生效后重新检查。"
			}
		}
		return "pending_dns", "Cloudflare 尚未完成域名接入验证。请核对 CNAME 和下方域名验证记录，解析生效后重新检查。"
	}
	return "pending_certificate", "域名接入已通过，正在验证并签发 HTTPS 证书；如有额外验证记录，请一并添加。"
}

func (h Hostname) Records() []Record {
	records := []Record{}
	if h.Ownership.Name != "" && h.Ownership.Value != "" {
		records = append(records, Record{"TXT", h.Ownership.Name, h.Ownership.Value})
	}
	for _, r := range h.SSL.ValidationRecords {
		if r.Name != "" && r.Value != "" {
			records = append(records, Record{"TXT", r.Name, r.Value})
		}
	}
	return records
}

type Provider interface {
	Ensure(context.Context, string) (Hostname, error)
	Find(context.Context, string) (Hostname, error)
	Get(context.Context, string) (Hostname, error)
	Delete(context.Context, string) error
}

var ErrProviderMissing = errors.New("custom hostname missing")

type Cloudflare struct {
	Zone, Token string
	Limit       int
	Client      *http.Client
}

func (p *Cloudflare) call(ctx context.Context, method, path string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.cloudflare.com/client/v4/zones/"+url.PathEscape(p.Zone)+"/custom_hostnames"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("domain provider unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return ErrProviderMissing
	}
	var envelope struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&envelope) != nil || res.StatusCode < 200 || res.StatusCode >= 300 || !envelope.Success {
		return errors.New("domain provider request failed")
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// The caller holds a database-wide lock. Listing first recovers an ambiguous create.
func (p *Cloudflare) Ensure(ctx context.Context, host string) (Hostname, error) {
	h, err := p.Find(ctx, host)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, ErrProviderMissing) {
		return Hostname{}, err
	}
	var quota struct {
		Used *int64 `json:"used"`
	}
	if err = p.call(ctx, "GET", "/quota", nil, &quota); err != nil {
		return Hostname{}, err
	}
	if quota.Used == nil || p.Limit < 1 || p.Limit > 100 || *quota.Used >= int64(p.Limit) {
		return Hostname{}, errors.New("custom hostname quota reached")
	}
	err = p.call(ctx, "POST", "", map[string]any{"hostname": host, "ssl": map[string]any{"method": "http", "type": "dv", "settings": map[string]any{"min_tls_version": "1.2"}}}, &h)
	return h, err
}
func (p *Cloudflare) Get(ctx context.Context, id string) (Hostname, error) {
	var h Hostname
	err := p.call(ctx, "GET", "/"+url.PathEscape(id), nil, &h)
	return h, err
}
func (p *Cloudflare) Delete(ctx context.Context, id string) error {
	err := p.call(ctx, "DELETE", "/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, ErrProviderMissing) {
		return nil
	}
	return err
}

func (p *Cloudflare) Find(ctx context.Context, host string) (Hostname, error) {
	var list []Hostname
	if err := p.call(ctx, "GET", "?hostname="+url.QueryEscape(host), nil, &list); err != nil {
		return Hostname{}, err
	}
	for _, h := range list {
		if h.Hostname == host {
			return h, nil
		}
	}
	return Hostname{}, ErrProviderMissing
}
