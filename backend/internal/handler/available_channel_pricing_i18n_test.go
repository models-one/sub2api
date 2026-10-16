//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pricingI18nGroupRepo struct {
	service.GroupRepository
	groups []service.Group
}

func (r pricingI18nGroupRepo) ListActive(context.Context) ([]service.Group, error) {
	return r.groups, nil
}

type pricingI18nUserRepo struct{ service.UserRepository }

func (pricingI18nUserRepo) GetByID(_ context.Context, id int64) (*service.User, error) {
	return &service.User{ID: id, Status: service.StatusActive, Role: service.RoleUser}, nil
}

type pricingI18nSubRepo struct {
	service.UserSubscriptionRepository
}

func (pricingI18nSubRepo) ListActiveByUserID(context.Context, int64) ([]service.UserSubscription, error) {
	return nil, nil
}

// fork 的定价页接口（/pricing/groups、/pricing/public/groups）与上游用户侧接口同口径，
// 按 Accept-Language 返回分组名称译文；公开接口带 Vary: Accept-Language。
func TestPricingGroupListsLocalizeGroupNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groups := []service.Group{{
		ID: 7, Name: "标准", Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1,
		I18n: service.GroupI18n{"en": {Name: "Standard"}, "zh-TW": {Name: "標準"}},
	}}
	apiKeyService := service.NewAPIKeyService(nil, pricingI18nUserRepo{}, pricingI18nGroupRepo{groups: groups},
		pricingI18nSubRepo{}, nil, nil, &config.Config{})
	h := &AvailableChannelHandler{apiKeyService: apiKeyService}

	for _, tc := range []struct {
		acceptLanguage string
		want           string
	}{
		{"en", "Standard"},
		{"zh-TW,zh;q=0.9", "標準"},
		{"", "标准"},
	} {
		for _, endpoint := range []struct {
			name   string
			public bool
			call   func(*gin.Context)
		}{
			{"authenticated", false, h.PricingGroupList},
			{"public", true, h.PricingGroupListPublic},
		} {
			t.Run(endpoint.name+"/"+tc.acceptLanguage, func(t *testing.T) {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/pricing/groups", nil)
				if tc.acceptLanguage != "" {
					c.Request.Header.Set("Accept-Language", tc.acceptLanguage)
				}
				if !endpoint.public {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 51})
				}

				endpoint.call(c)

				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				var resp struct {
					Data []struct {
						ID   int64  `json:"id"`
						Name string `json:"name"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Len(t, resp.Data, 1)
				require.Equal(t, tc.want, resp.Data[0].Name)
				if endpoint.public {
					require.Contains(t, w.Header().Values("Vary"), "Accept-Language")
				}
			})
		}
	}
}
