package util

import (
	"net/http"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"

	// "github.com/free5gc/openapi/models"
	"github.com/free5gc/openapi-r17/models"
	"github.com/gin-gonic/gin"
)

type NFContextGetter func() *chf_context.CHFContext

type RouterAuthorizationCheck struct {
	serviceName models.ServiceName
}

func NewRouterAuthorizationCheck(serviceName models.ServiceName) *RouterAuthorizationCheck {
	return &RouterAuthorizationCheck{
		serviceName: serviceName,
	}
}

func (rac *RouterAuthorizationCheck) Check(c *gin.Context, chfContext chf_context.NFContext) {
	token := c.Request.Header.Get("Authorization")
	err := chfContext.AuthorizationCheck(token, rac.serviceName)

	if err != nil {
		logger.UtilLog.Debugf("RouterAuthorizationCheck::Check Unauthorized: %s", err.Error())
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		c.Abort()
		return
	}

	logger.UtilLog.Debugf("RouterAuthorizationCheck::Check Authorized")
}
