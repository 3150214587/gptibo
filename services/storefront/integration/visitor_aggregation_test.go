package integration_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gptibo/storefront/internal/management"
)

func TestVisitorDistributionAcrossStoresAndDateRanges(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('a','AAA001','A',true),('b','BBB001','B',false),('empty','CCC001','Empty',true)`)
	execSQL(t, owner, `INSERT INTO partner_stores(id,partner_id,slot,slug,name,active) VALUES('a-second','a',2,'AAA002','Second',false)`)
	day := func(offset int) string {
		return time.Now().In(time.FixedZone("CST", 8*3600)).AddDate(0, 0, offset).Format("2006-01-02")
	}
	execSQL(t, owner, `INSERT INTO visitor_days(day,shop_id,visitor_hash,page_views) VALUES
 ($1,'platform','shared',2),($1,'a','shared',3),($1,'a-second','shared',4),
 ($1,'a-second','other',5),($1,'b','shared',6),($2,'a','shared',7),
 ($3,'a','month-visitor',8),($4,'a-second','quarter-visitor',9),($5,'a','too-old',100)`, day(0), day(-1), day(-10), day(-40), day(-100))
	s := management.Service{Store: app}
	for _, tc := range []struct {
		days, views, visitors int
	}{{7, 19, 2}, {30, 27, 3}, {90, 36, 4}} {
		t.Run(fmt.Sprint(tc.days), func(t *testing.T) {
			out, e := s.Visitors(context.Background(), tc.days, "", true)
			if e != nil {
				t.Fatal(e)
			}
			if out["pageViews"] != int64(tc.views+8) || out["visitors"] != int64(tc.visitors) || len(out["trend"].([]map[string]any)) != tc.days {
				t.Fatal("wrong global totals or trend", out)
			}
			platform := out["platform"].(map[string]any)
			if platform["pageViews"] != int64(2) || platform["visitors"] != int64(1) {
				t.Fatal("wrong platform totals", platform)
			}
			partners := out["partners"].([]map[string]any)
			if len(partners) != 3 || partners[0]["id"] != "a" || partners[1]["id"] != "b" || partners[2]["id"] != "empty" {
				t.Fatal("missing or incorrectly ordered partners", partners)
			}
			if partners[0]["pageViews"] != int64(tc.views) || partners[0]["visitors"] != int64(tc.visitors) || partners[1]["pageViews"] != int64(6) || partners[1]["visitors"] != int64(1) || partners[2]["pageViews"] != int64(0) || partners[2]["visitors"] != int64(0) {
				t.Fatal("wrong partner totals, cross-store deduplication or inactive history", partners)
			}
			own, e := s.Visitors(context.Background(), tc.days, "a", false)
			if e != nil || own["pageViews"] != partners[0]["pageViews"] || own["visitors"] != partners[0]["visitors"] {
				t.Fatal("partner and admin reports disagree", own, e)
			}
		})
	}
}

func TestVisitorDistributionScalesWithoutPerPartnerScans(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name) SELECT 'traffic-'||n,'T'||lpad(n::text,5,'0'),'Traffic '||n FROM generate_series(1,200) n`)
	day := time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02")
	execSQL(t, owner, `INSERT INTO visitor_days(day,shop_id,visitor_hash,page_views) SELECT $1,p.id,md5(v::text),2 FROM partners p CROSS JOIN generate_series(1,500) v`, day)
	execSQL(t, owner, `ANALYZE partners; ANALYZE partner_stores; ANALYZE visitor_days`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	out, e := (management.Service{Store: app}).Visitors(ctx, 7, "", true)
	if e != nil {
		t.Fatal("100,000 visitor rows across 200 partners must fit the request deadline", e)
	}
	if out["pageViews"] != int64(200000) || out["visitors"] != int64(500) || len(out["partners"].([]map[string]any)) != 200 {
		t.Fatal("wrong bulk totals", out)
	}
	for _, p := range out["partners"].([]map[string]any) {
		if p["pageViews"] != int64(1000) || p["visitors"] != int64(500) {
			t.Fatal("wrong partner bulk totals", p)
		}
	}
	t.Logf("100,000 visitor rows / 200 partners: %s", time.Since(start))
}
