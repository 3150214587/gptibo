package mailer

import (
	_ "embed"
	"encoding/json"
	"errors"
	"html"
	"regexp"
	"strings"
)

//go:embed templates.json
var templateJSON []byte

type Message struct {
	SenderName string `json:"-"`
	Subject    string `json:"subject"`
	HTML       string `json:"html"`
	Text       string `json:"text"`
}

func Template(kind, origin string, fields map[string]string) (Message, error) {
	return BrandedTemplate(kind, PlatformBrand(origin), fields)
}

func BrandedTemplate(kind string, brand Brand, fields map[string]string) (Message, error) {
	var templates map[string]Message
	if e := json.Unmarshal(templateJSON, &templates); e != nil {
		return Message{}, e
	}
	message, ok := templates[kind]
	if !ok {
		return Message{}, errors.New("unknown email template")
	}
	values := map[string]string{"BRAND_NAME": brand.Name, "BRAND_LOGO": brand.Logo, "BRAND_ORDERS_URL": brand.OrdersURL, "BRAND_WORKSPACE_URL": brand.WorkspaceURL, "BRAND_ADMIN_URL": brand.AdminURL}
	for k, v := range fields {
		if !strings.HasPrefix(k, "BRAND_") {
			values[k] = v
		}
	}
	message.SenderName = brand.Name
	missing := false
	pattern := regexp.MustCompile(`__RECHARGE_([A-Z0-9_]+)__`)
	replace := func(escaped bool) func(string) string {
		return func(marker string) string {
			key := strings.TrimSuffix(strings.TrimPrefix(marker, "__RECHARGE_"), "__")
			value, ok := values[key]
			if !ok {
				missing = true
				return marker
			}
			if escaped {
				return strings.ReplaceAll(html.EscapeString(value), "\n", "<br />")
			}
			return value
		}
	}
	// One pass: customer content must never become another template placeholder.
	message.Subject = pattern.ReplaceAllStringFunc(message.Subject, replace(false))
	message.HTML = pattern.ReplaceAllStringFunc(message.HTML, replace(true))
	message.Text = pattern.ReplaceAllStringFunc(message.Text, replace(false))
	if missing {
		return Message{}, errors.New("incomplete email template")
	}
	return message, nil
}
