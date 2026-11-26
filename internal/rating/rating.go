package rating

import (
	"fmt"
	"strconv"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm"
	"github.com/fiorix/go-diameter/diam/sm/smpeer"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
)

func SendServiceUsageRequest(
	ue *chf_context.ChfUe, sur *charging_datatype.ServiceUsageRequest,
) (*charging_datatype.ServiceUsageResponse, error) {
	answers := make(chan *diam.Message, 1)
	client := *ue.RatingClient
	client.Handler = sm.New(ue.RatingMux.Settings())
	client.Handler.Handle("SUA", HandleSUA(answers))
	rfDiameter := factory.ChfConfig.Configuration.RfDiameter
	addr := rfDiameter.HostIPv4 + ":" + strconv.Itoa(rfDiameter.Port)
	conn, err := client.DialNetworkTLS(rfDiameter.Protocol, addr, rfDiameter.Tls.Pem, rfDiameter.Tls.Key)
	if err != nil {
		return nil, err
	}

	defer conn.Close()

	meta, ok := smpeer.FromContext(conn.Context())
	if !ok {
		return nil, fmt.Errorf("peer metadata unavailable")
	}

	sur.DestinationRealm = datatype.DiameterIdentity(meta.OriginRealm)
	sur.DestinationHost = datatype.DiameterIdentity(meta.OriginHost)

	msg := diam.NewRequest(charging_code.ServiceUsageMessage, charging_code.Re_interface, dict.Default)
	err = msg.Marshal(sur)
	if err != nil {
		return nil, fmt.Errorf("marshal SUR Failed: %s", err)
	}

	_, err = msg.WriteTo(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to send message from %s: %s",
			conn.RemoteAddr(), err)
	}

	select {
	case m := <-answers:
		return decodeServiceUsageAnswer(sur, m)
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout: no rate answer received")
	}
}

func decodeServiceUsageAnswer(
	request *charging_datatype.ServiceUsageRequest, message *diam.Message,
) (*charging_datatype.ServiceUsageResponse, error) {
	var response charging_datatype.ServiceUsageResponse
	if err := message.Unmarshal(&response); err != nil {
		return nil, fmt.Errorf("decode rating answer: %w", err)
	}
	if response.SessionId != request.SessionId {
		return nil, fmt.Errorf("rating answer does not match service usage request")
	}
	switch response.ResultCode {
	case diam.Success:
		return &response, nil
	case charging_code.DiameterUserUnknown:
		return nil, fmt.Errorf("%w: no tariff for rating group %d",
			charging.ErrChargingDataNotFound, request.ServiceRating.ServiceIdentifier)
	default:
		return nil, fmt.Errorf("rating function rejected service usage: result=%d", response.ResultCode)
	}
}

func HandleSUA(rgChan chan *diam.Message) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		logger.RatingLog.Tracef("Received SUA from %s", c.RemoteAddr())

		select {
		case rgChan <- m:
		default:
		}
	}
}
