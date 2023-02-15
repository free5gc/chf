package recharge

import (
	"net/http"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/producer"
	"github.com/gin-gonic/gin"
)

func RechargeGet(c *gin.Context) {
	c.String(http.StatusOK, "recharge")
}

func RechargePut(c *gin.Context) {
	ueid := c.Param("UeId")

	logger.RechargingLog.Warnf("UE[%s] Recharg", ueid)
	producer.NotifyRecharge(ueid)

	c.JSON(http.StatusNoContent, gin.H{})
}
