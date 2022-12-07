package recharge

import (
	"net/http"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/producer"
	// "github.com/free5gc/openapi"
	// "github.com/free5gc/openapi/models"
	// "github.com/free5gc/util/httpwrapper"
	"github.com/gin-gonic/gin"
)


func RechargeGet(c *gin.Context) {
	c.String(http.StatusOK, "recharge")
}

func RechargePut(c *gin.Context) {
	ueid := c.Param("UeId")
	logger.RechargingLog.Warnf("UE[%s] Recharg", ueid)
	producer.NotifyRecharge()

	c.JSON(http.StatusNoContent, gin.H{})
}
