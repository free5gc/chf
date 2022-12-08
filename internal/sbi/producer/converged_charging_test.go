package producer_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/free5gc/TarrifUtil/tarrifType"
	"github.com/free5gc/chf/internal/ftp"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/internal/sbi/producer"
	"github.com/free5gc/chf/pkg/service"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/util/mongoapi"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

const chargingDataColl = "chargingData"

var CHF = &service.CHF{}

type ChargingData struct {
	OnlineCharging bool                     `json:"onlineChargingChk,omitempty" yaml:"onlineChargingChk" bson:"onlineChargingChk" mapstructure:"onlineChargingChk"`
	Quota          uint32                   `json:"quota" yaml:"quota" bson:"quota" mapstructure:"quota"`
	UnitCost       string                   `json:"unitCost,omitempty" yaml:"unitCost" bson:"unitCost" mapstructure:"unitCost"`
	CurrentTariff  tarrifType.CurrentTariff `json:"tarrif,omitempty" bson:"tarrif"`
}

func toBsonM(data interface{}) (ret bson.M) {
	tmp, _ := json.Marshal(data)
	json.Unmarshal(tmp, &ret)
	return
}

func TestRun(t *testing.T) {
	initTime := time.Now()
	wg := sync.WaitGroup{}
	wg.Add(1)
	ftp.OpenServer(&wg)

	dataBase := ChargingData{
		OnlineCharging: true,
		Quota:          10000,
		UnitCost:       "",
		CurrentTariff: tarrifType.CurrentTariff{
			RateElement: &tarrifType.RateElement{
				CCUnitType: &tarrifType.CCUnitType{
					Value: tarrifType.MONEY,
				},
				UnitCost: &tarrifType.UnitCost{
					ValueDigits: 1,
					Exponent:    3,
				},
			},
		},
	}

	if err := mongoapi.SetMongoDB("ChargingTest", "mongodb://localhost:27017"); err != nil {
		logger.ChargingdataPostLog.Errorf("Server start err: %+v", err)
		return
	}

	chargingBsonA := make([]interface{}, 0, 1)
	chargingBsonM := toBsonM(dataBase)
	chargingBsonM["ueId"] = "imsi-208930000000003"
	chargingBsonA = append(chargingBsonA, chargingBsonM)
	filter := bson.M{"ueId": "imsi-208930000000003"}

	if err := mongoapi.RestfulAPIDeleteMany(chargingDataColl, filter); err != nil {
		logger.ChargingdataPostLog.Errorf("PutSubscriberByID err: %+v", err)
	}

	if err := mongoapi.RestfulAPIPostMany(chargingDataColl, filter, chargingBsonA); err != nil {
		logger.ChargingdataPostLog.Errorf("PutSubscriberByID err: %+v", err)
	}

	t.Run("Charging Data Request", func(t *testing.T) {
		chargingData := models.ChargingDataRequest{
			ChargingId:           1,
			SubscriberIdentifier: "imsi-208930000000003",
			NfConsumerIdentification: &models.NfIdentification{
				NodeFunctionality: models.NodeFunctionality_SMF,
				NFName:            "SMF",
				// not sure if NFIPv4Address is RegisterIPv4 or BindingIPv4
				NFIPv4Address: "127.0.0.2",
			},
			InvocationTimeStamp:      &initTime,
			InvocationSequenceNumber: 1,
			Triggers: []models.Trigger{
				{
					TriggerType:     "",
					TriggerCategory: models.TriggerCategory_IMMEDIATE_REPORT,
				},
			},
			PDUSessionChargingInformation: &models.PduSessionChargingInformation{
				ChargingId: 1,
				UserInformation: &models.UserInformation{
					ServedGPSI: "msisdn-0900000000",
				},
				PduSessionInformation: &models.PduSessionInformation{
					PduSessionID: 1,
					NetworkSlicingInfo: &models.NetworkSlicingInfo{
						SNSSAI: &models.Snssai{
							Sst: 1,
							Sd:  "010203",
						},
					},

					PduType: models.PduSessionType_IPV4,
					ServingNetworkFunctionID: &models.ServingNetworkFunctionId{
						ServingNetworkFunctionInformation: &models.NfIdentification{
							NodeFunctionality: models.NodeFunctionality_AMF,
						},
					},
					DnnId: "internet",
				},
			},
			MultipleUnitUsage: []models.MultipleUnitUsage{
				{
					RatingGroup: 1,
					RequestedUnit: &models.RequestedUnit{
						TotalVolume:    800,
						UplinkVolume:   800,
						DownlinkVolume: 800,
					},
					UsedUnitContainer: []models.UsedUnitContainer{
						{
							TriggerTimestamp:         &initTime,
							QuotaManagementIndicator: models.QuotaManagementIndicator_ONLINE_CHARGING,
						},
					},
				},
			},
			NotifyUri: fmt.Sprintf("%s://%s:%d/nsmf-callback/notify",
				"http",
				"127.0.0.2",
				8000,
			),
		}
		rsp, _, problemDetails := producer.ChargingDataCreate(chargingData)

		expRsp := &models.ChargingDataResponse{
			InvocationSequenceNumber: 1,
			InvocationTimeStamp:      rsp.InvocationTimeStamp,
			MultipleUnitInformation: []models.MultipleUnitInformation{
				{
					RatingGroup:          1,
					VolumeQuotaThreshold: 640,
					GrantedUnit: &models.GrantedUnit{
						TotalVolume:    800,
						UplinkVolume:   800,
						DownlinkVolume: 800,
					},
					FinalUnitIndication: &models.FinalUnitIndication{},
				},
			},
		}

		chargingInterface, _ := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)

		remainQuota := uint32(0)
		switch value := chargingInterface["quota"].(type) {
		case int:
			remainQuota = uint32(value)
		case float64:
			remainQuota = uint32(value)
		}
		require.Equal(t, remainQuota, uint32(10000))
		require.Equal(t, rsp, expRsp)
		require.Nil(t, problemDetails)
	})

	t.Run("Last Granted Quota", func(t *testing.T) {
		chargingData := models.ChargingDataRequest{
			ChargingId:           1,
			SubscriberIdentifier: "imsi-208930000000003",
			NfConsumerIdentification: &models.NfIdentification{
				NodeFunctionality: models.NodeFunctionality_SMF,
				NFName:            "SMF",
				// not sure if NFIPv4Address is RegisterIPv4 or BindingIPv4
				NFIPv4Address: "127.0.0.2",
			},
			InvocationTimeStamp:      &initTime,
			InvocationSequenceNumber: 1,
			Triggers: []models.Trigger{
				{
					TriggerType:     models.TriggerType_QUOTA_THRESHOLD,
					TriggerCategory: models.TriggerCategory_IMMEDIATE_REPORT,
				},
			},
			PDUSessionChargingInformation: &models.PduSessionChargingInformation{
				ChargingId: 1,
				UserInformation: &models.UserInformation{
					ServedGPSI: "msisdn-0900000000",
				},
				PduSessionInformation: &models.PduSessionInformation{
					PduSessionID: 1,
					NetworkSlicingInfo: &models.NetworkSlicingInfo{
						SNSSAI: &models.Snssai{
							Sst: 1,
							Sd:  "010203",
						},
					},

					PduType: models.PduSessionType_IPV4,
					ServingNetworkFunctionID: &models.ServingNetworkFunctionId{
						ServingNetworkFunctionInformation: &models.NfIdentification{
							NodeFunctionality: models.NodeFunctionality_AMF,
						},
					},
					DnnId: "internet",
				},
			},
			MultipleUnitUsage: []models.MultipleUnitUsage{
				{
					RatingGroup: 1,
					RequestedUnit: &models.RequestedUnit{
						TotalVolume:    1200,
						UplinkVolume:   1200,
						DownlinkVolume: 1200,
					},
					UsedUnitContainer: []models.UsedUnitContainer{
						{
							TriggerTimestamp:         &initTime,
							QuotaManagementIndicator: models.QuotaManagementIndicator_ONLINE_CHARGING,
							TotalVolume:              850,
							UplinkVolume:             450,
							DownlinkVolume:           400,
						},
					},
				},
			},
			NotifyUri: fmt.Sprintf("%s://%s:%d/nsmf-callback/notify",
				"http",
				"127.0.0.2",
				8000,
			),
		}
		rsp, problemDetails := producer.ChargingDataUpdate(chargingData, "imsi-208930000000003SMF0")

		expRsp := &models.ChargingDataResponse{
			InvocationSequenceNumber: 1,
			InvocationTimeStamp:      rsp.InvocationTimeStamp,
			MultipleUnitInformation: []models.MultipleUnitInformation{
				{
					RatingGroup:          1,
					VolumeQuotaThreshold: 120,
					GrantedUnit: &models.GrantedUnit{
						TotalVolume:    150,
						UplinkVolume:   150,
						DownlinkVolume: 150,
					},
					FinalUnitIndication: &models.FinalUnitIndication{
						FinalUnitAction: models.FinalUnitAction_TERMINATE,
					},
				},
			},
		}

		filter = bson.M{"ueId": chargingData.SubscriberIdentifier}
		chargingInterface, _ := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)

		remainQuota := uint32(0)
		switch value := chargingInterface["quota"].(type) {
		case int:
			remainQuota = uint32(value)
		case int64:
			remainQuota = uint32(value)
		case float64:
			remainQuota = uint32(value)
		}

		require.Equal(t, remainQuota, uint32(1500))
		require.Equal(t, rsp, expRsp)
		require.Nil(t, problemDetails)
	})

	t.Run("Quota Exsualt", func(t *testing.T) {
		chargingData := models.ChargingDataRequest{
			ChargingId:           1,
			SubscriberIdentifier: "imsi-208930000000003",
			NfConsumerIdentification: &models.NfIdentification{
				NodeFunctionality: models.NodeFunctionality_SMF,
				NFName:            "SMF",
				// not sure if NFIPv4Address is RegisterIPv4 or BindingIPv4
				NFIPv4Address: "127.0.0.2",
			},
			InvocationTimeStamp:      &initTime,
			InvocationSequenceNumber: 1,
			Triggers: []models.Trigger{
				{
					TriggerType:     models.TriggerType_QUOTA_THRESHOLD,
					TriggerCategory: models.TriggerCategory_IMMEDIATE_REPORT,
				},
			},
			PDUSessionChargingInformation: &models.PduSessionChargingInformation{
				ChargingId: 1,
				UserInformation: &models.UserInformation{
					ServedGPSI: "msisdn-0900000000",
				},
				PduSessionInformation: &models.PduSessionInformation{
					PduSessionID: 1,
					NetworkSlicingInfo: &models.NetworkSlicingInfo{
						SNSSAI: &models.Snssai{
							Sst: 1,
							Sd:  "010203",
						},
					},

					PduType: models.PduSessionType_IPV4,
					ServingNetworkFunctionID: &models.ServingNetworkFunctionId{
						ServingNetworkFunctionInformation: &models.NfIdentification{
							NodeFunctionality: models.NodeFunctionality_AMF,
						},
					},
					DnnId: "internet",
				},
			},
			MultipleUnitUsage: []models.MultipleUnitUsage{
				{
					RatingGroup: 1,
					RequestedUnit: &models.RequestedUnit{
						TotalVolume:    1200,
						UplinkVolume:   1200,
						DownlinkVolume: 1200,
					},
					UsedUnitContainer: []models.UsedUnitContainer{
						{
							TriggerTimestamp:         &initTime,
							QuotaManagementIndicator: models.QuotaManagementIndicator_ONLINE_CHARGING,
							TotalVolume:              160,
							UplinkVolume:             160,
							DownlinkVolume:           160,
						},
					},
				},
			},
			NotifyUri: fmt.Sprintf("%s://%s:%d/nsmf-callback/notify",
				"http",
				"127.0.0.2",
				8000,
			),
		}
		rsp, problemDetails := producer.ChargingDataUpdate(chargingData, "imsi-208930000000003SMF0")

		expRsp := &models.ChargingDataResponse{
			InvocationSequenceNumber: 1,
			InvocationTimeStamp:      rsp.InvocationTimeStamp,
			MultipleUnitInformation: []models.MultipleUnitInformation{
				{
					RatingGroup:         1,
					FinalUnitIndication: &models.FinalUnitIndication{},
				},
			},
		}

		filter = bson.M{"ueId": chargingData.SubscriberIdentifier}
		chargingInterface, _ := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)

		remainQuota := uint32(0)
		switch value := chargingInterface["quota"].(type) {
		case int:
			remainQuota = uint32(value)
		case int64:
			remainQuota = uint32(value)
		case float64:
			remainQuota = uint32(value)
		}

		require.Equal(t, remainQuota, uint32(0))
		require.Equal(t, rsp, expRsp)
		require.Nil(t, problemDetails)
	})
}
