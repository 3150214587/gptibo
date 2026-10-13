package management

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	_ "image/jpeg"
	"image/png"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

type Branding struct {
	PageTitle   string `json:"pageTitle"`
	Headline    string `json:"headline"`
	Description string `json:"description"`
	Name        string `json:"name"`
	Logo        string `json:"logo"`
	Version     int64  `json:"version"`
}

func (s Service) Branding(ctx context.Context, id string) (Branding, error) {
	var b Branding
	e := s.Store.DB.QueryRow(ctx, `SELECT COALESCE(b.name,''),COALESCE(b.logo,''),COALESCE(b.version,0),COALESCE(b.page_title,''),COALESCE(b.headline,''),COALESCE(b.description,'') FROM partners p LEFT JOIN partner_branding b ON b.partner_id=p.id WHERE p.id=$1`, id).Scan(&b.Name, &b.Logo, &b.Version, &b.PageTitle, &b.Headline, &b.Description)
	if e == pgx.ErrNoRows {
		return b, fault.New(404, "代理不存在。")
	}
	return b, e
}

var logoPath = regexp.MustCompile(`^/store-logos/[a-f0-9]{64}/[a-f0-9]{64}\.png$`)

func cleanBranding(in Branding) (Branding, error) {
	in.Name = strings.TrimSpace(in.Name)
	for _, field := range []struct {
		value *string
		max   int
	}{{&in.PageTitle, 80}, {&in.Headline, 80}, {&in.Description, 200}} {
		*field.value = strings.TrimSpace(*field.value)
		if !utf8.ValidString(*field.value) || utf8.RuneCountInString(*field.value) > field.max || strings.ContainsFunc(*field.value, unicode.IsControl) {
			return in, fault.New(400, "网页标题和首页标题最多 80 个字，简介最多 200 个字，不能包含控制字符。")
		}
	}
	if in.Version < 0 || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 32 || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return in, fault.New(400, "店铺名称最多 32 个字，不能包含控制字符。")
	}
	if in.Logo != "" && !logoPath.MatchString(in.Logo) {
		return in, fault.New(400, "请通过上传按钮选择 Logo。")
	}
	return in, nil
}

type PreparedLogo struct {
	Key  string `json:"key"`
	Data string `json:"data"`
}

func (s Service) PrepareLogo(ctx context.Context, owner, logo string) (PreparedLogo, error) {
	if e := s.Auth.Limit(ctx, "partner-logo:"+owner, 20, 3600); e != nil {
		return PreparedLogo{}, e
	}
	var in PreparedLogo
	const maxBytes = 256 * 1024
	if len(logo) > 350000 {
		return in, fault.New(400, "Logo 过大，请重新选择图片。")
	}
	prefix, encoded, ok := strings.Cut(logo, ",")
	if !ok || (prefix != "data:image/png;base64" && prefix != "data:image/jpeg;base64") {
		return in, fault.New(400, "Logo 仅支持 PNG 或 JPEG 图片。")
	}
	raw, e := base64.StdEncoding.Strict().DecodeString(encoded)
	if e != nil || len(raw) > maxBytes {
		return in, fault.New(400, "Logo 图片无效或过大。")
	}
	cfg, format, e := image.DecodeConfig(bytes.NewReader(raw))
	if e != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 256 || cfg.Height > 256 || (format != "png" && format != "jpeg") {
		return in, fault.New(400, "Logo 图片无效，请通过上传按钮重新选择。")
	}
	img, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		return in, fault.New(400, "无法读取 Logo 图片。")
	}
	var output bytes.Buffer
	if e = png.Encode(&output, img); e != nil {
		return in, e
	}
	if output.Len() > maxBytes {
		return in, fault.New(400, "Logo 过大，请使用较简单的图片。")
	}
	// Re-encoding strips metadata and trailing content. The edge stores these bytes in R2.
	in.Key = "store-logos/" + security.Hash(owner) + "/" + security.Hash(output.String()) + ".png"
	in.Data = base64.StdEncoding.EncodeToString(output.Bytes())
	return in, nil
}

func (s Service) SaveBranding(ctx context.Context, token string, in Branding) error {
	in, e := cleanBranding(in)
	if e != nil {
		return e
	}
	return s.write(ctx, "partner", token, func(tx pgx.Tx, p auth.Principal) error {
		if in.Logo != "" && !strings.HasPrefix(in.Logo, "/store-logos/"+security.Hash(p.Subject)+"/") {
			return fault.New(400, "Logo 不属于当前店铺，请重新上传。")
		}
		if _, e := tx.Exec(ctx, `INSERT INTO partner_branding(partner_id) VALUES($1) ON CONFLICT DO NOTHING`, p.Subject); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `UPDATE partner_branding SET name=$1,logo=$2,page_title=$5,headline=$6,description=$7,version=version+1 WHERE partner_id=$3 AND version=$4`, in.Name, in.Logo, p.Subject, in.Version, in.PageTitle, in.Headline, in.Description)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return conflict()
		}
		if _, e = tx.Exec(ctx, `UPDATE partner_stores SET settings=settings-ARRAY['brandName','logo','pageTitle','headline','description'],version=version+1 WHERE id=$1`, p.Subject); e != nil {
			return e
		}
		return audit(ctx, tx, p, "partner-branding", p.Subject, map[string]any{"name": in.Name, "hasLogo": in.Logo != "", "version": in.Version + 1})
	})
}
