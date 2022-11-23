package recharge

import (
	"sync"
	"time"

	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/producer"
	"github.com/free5gc/util/mongoapi"
	"go.mongodb.org/mongo-driver/bson"
)

const chargingDataColl = "chargingData"

func OpenServer(wg *sync.WaitGroup) {

	logger.RechargingLog.Info("Recharg server Start")

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
