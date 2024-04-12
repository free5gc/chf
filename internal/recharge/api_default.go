package recharge

// import (
// 	"net/http"
// 	"strconv"
// 	"strings"

// 	"github.com/free5gc/chf/internal/logger"
// 	"github.com/free5gc/chf/internal/sbi/producer"
// 	"github.com/gin-gonic/gin"
// )

// func RechargeGet(c *gin.Context) {
// 	c.String(http.StatusOK, "recharge")
// }

// func RechargePut(c *gin.Context) {
// 	rechargingInfo := c.Param("rechargingInfo")
// 	ueIdRatingGroup := strings.Split(rechargingInfo, "_")
// 	ueId := ueIdRatingGroup[0]
// 	rgStr := ueIdRatingGroup[1]
// 	rg, err := strconv.Atoi(rgStr)
// 	if err != nil {
// 		logger.RechargingLog.Errorf("UE[%s] fail to recharge for rating group %s", ueId, rgStr)
// 	}

// 	logger.RechargingLog.Warnf("UE[%s] Recharg for rating group %d", ueId, rg)

// 	producer.NotifyRecharge(ueId, int32(rg))

// 	c.JSON(http.StatusNoContent, gin.H{})
// }
