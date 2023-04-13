package rating

import (
	"fmt"
	"math"
	"time"

	chf_context "github.com/free5gc/chf/internal/context"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm/smpeer"
	rate_code "github.com/free5gc/RatingUtil/code"
	"github.com/free5gc/RatingUtil/dataType"
	rate_datatype "github.com/free5gc/RatingUtil/dataType"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi/models"
)

const chargingDataColl = "chargingData"

func ServiceUsageRetrieval(serviceUsage rate_datatype.ServiceUsageRequest) (rate_datatype.ServiceUsageResponse, *models.ProblemDetails, bool) {
	lastgrantedquota := false

	unitCost := (serviceUsage.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits) * datatype.Integer64(math.Pow10(int(serviceUsage.ServiceRating.MonetaryTariff.RateElement.UnitCost.Exponent)))
	monetaryCost := datatype.Integer64(serviceUsage.ServiceRating.ConsumedUnits) * unitCost
	monetaryRequest := datatype.Integer64(serviceUsage.ServiceRating.RequestedUnits) * unitCost

	logger.ChargingdataPostLog.Tracef("Cost per Byte[%d]", unitCost)
	rsp := dataType.ServiceUsageResponse{
		SessionId: serviceUsage.SessionId,
		ServiceRating: &dataType.ServiceRating{
			Price:         datatype.Unsigned32(monetaryCost),
			MonetaryQuota: serviceUsage.ServiceRating.MonetaryQuota,
		},
	}

	if serviceUsage.ServiceRating.RequestSubType == dataType.REQ_SUBTYPE_DEBIT {
		logger.ChargingdataPostLog.Warnf("Out of Monetary Quota, Debit mode")
		rsp.ServiceRating.AllowedUnits = 0
		return rsp, nil, lastgrantedquota
	} else if serviceUsage.ServiceRating.RequestSubType == dataType.REQ_SUBTYPE_RESERVE {
		if monetaryCost < datatype.Integer64(serviceUsage.ServiceRating.MonetaryQuota) {
			monetaryRemain := datatype.Integer64(serviceUsage.ServiceRating.MonetaryQuota) - monetaryCost
			if (monetaryRemain - monetaryRequest) > 0 {
				rsp.ServiceRating.AllowedUnits = serviceUsage.ServiceRating.RequestedUnits
			} else {
				rsp.ServiceRating.AllowedUnits = datatype.Unsigned32(monetaryRemain / unitCost)
				logger.ChargingdataPostLog.Warn("Last granted Quota")
				lastgrantedquota = true
			}
		} else {
			logger.ChargingdataPostLog.Warn("Out of Monetary Quota")
			rsp.ServiceRating.AllowedUnits = 0
			return rsp, nil, lastgrantedquota
		}
	} else {
		logger.ChargingdataPostLog.Warnf("Unsupport RequestSubType")
	}

	return rsp, nil, lastgrantedquota
}

func SendServiceUsageRequest(ue *chf_context.ChfUe, sur *dataType.ServiceUsageRequest) (*dataType.ServiceUsageResponse, error) {
	self := chf_context.CHF_Self()
	ue.RatingMux.Handle("SUA", HandleSUA(ue.RatingChan))

	conn, err := ue.RatingClient.DialNetwork("tcp", self.RatingAddr)
	if err != nil {
		return nil, err
	}

	meta, ok := smpeer.FromContext(conn.Context())
	if !ok {
		return nil, fmt.Errorf("peer metadata unavailable")
	}

	sur.DestinationRealm = datatype.DiameterIdentity(meta.OriginRealm)
	sur.DestinationHost = datatype.DiameterIdentity(meta.OriginHost)

	msg := diam.NewRequest(rate_code.ServiceUsageMessage, rate_code.Re_interface, dict.Default)
	msg.Marshal(sur)
	_, err = msg.WriteTo(conn)
	if err != nil {
		return nil, fmt.Errorf("Failed to send message from %s: %s\n",
			conn.RemoteAddr(), err)
	}

	select {
	case m := <-ue.RatingChan:
		var sua rate_datatype.ServiceUsageResponse
		if err := m.Unmarshal(&sua); err != nil {
			return nil, fmt.Errorf("Failed to parse message from %v", err)
		}
		return &sua, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout: no rate answer received")
	}
}

func HandleSUA(rgChan chan *diam.Message) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		logger.RatingLog.Tracef("Received SUA from %s", c.RemoteAddr())

		rgChan <- m
	}
}
