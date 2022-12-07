package recharge

import (
	"sync"
	"time"
	// "github.com/gin-contrib/cors"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/producer"
	"github.com/free5gc/util/mongoapi"
	"go.mongodb.org/mongo-driver/bson"
	// logger_util "github.com/free5gc/util/logger"
)

const chargingDataColl = "chargingData"

func OpenServer(wg *sync.WaitGroup) {

	logger.RechargingLog.Info("Recharg server Start")

	// router := logger_util.NewGinWithLogrus(logger.GinLog)

	// AddService(router)

	// router.Use(cors.New(cors.Config{
	// 	AllowMethods: []string{"GET", "POST", "OPTIONS", "PUT", "PATCH", "DELETE"},
	// 	AllowHeaders: []string{
	// 		"Origin", "Content-Length", "Content-Type", "User-Agent",
	// 		"Referrer", "Host", "Token", "X-Requested-With",
	// 	},
	// 	ExposeHeaders:    []string{"Content-Length"},
	// 	AllowCredentials: true,
	// 	AllowAllOrigins:  true,
	// 	MaxAge:           86400,
	// }))

	go func() {
		defer func() {
			logger.RechargingLog.Error("Recharg server stopped")
			wg.Done()
		}()

		for {
			time.Sleep(5 * time.Second)

			filter := bson.M{"recharge": true}

			chargingInterface, err := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)
			if err != nil {
				logger.RechargingLog.Errorf("Get recharge error: %+v", err)
			}

			// need to check
			if chargingInterface == nil {
				continue
			}

			recharge := chargingInterface["rechargeUeId"].(string)
			logger.RechargingLog.Infof("UE[%s] Recharg", recharge)
			producer.NotifyRecharge()

		}
	}()

}
