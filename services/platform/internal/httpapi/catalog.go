package httpapi

import (
	"context"
	"github.com/gin-gonic/gin"
	"gptibo/platform/internal/commerce"
)

func (s *Server) readGPTPlans(ctx context.Context) ([]commerce.UpstreamPlan, error) {
	if s.GPTPlans == nil {
		return nil, commerce.Fail("unavailable", "上游套餐查询暂不可用", 503)
	}
	return s.GPTPlans(ctx)
}

func (s *Server) gptPlans(c *gin.Context) {
	rows, err := s.readGPTPlans(c)
	if err != nil {
		bad(c, err)
		return
	}
	if rows == nil {
		rows = []commerce.UpstreamPlan{}
	}
	ok(c, rows)
}
