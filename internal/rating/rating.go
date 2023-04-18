package rating

import (
	"fmt"
	"time"

	chf_context "github.com/free5gc/chf/internal/context"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm/smpeer"
	rate_code "github.com/free5gc/ChargingUtil/code"
	charging_datatype "github.com/free5gc/ChargingUtil/datatype"
	"github.com/free5gc/chf/internal/logger"
)

func SendServiceUsageRequest(ue *chf_context.ChfUe, sur *charging_datatype.ServiceUsageRequest) (*charging_datatype.ServiceUsageResponse, error) {
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
	err = msg.Marshal(sur)
	if err != nil {
		return nil, fmt.Errorf("Marshal SUR Failed: %s\n", err)
	}

	_, err = msg.WriteTo(conn)
	if err != nil {
		return nil, fmt.Errorf("Failed to send message from %s: %s\n",
			conn.RemoteAddr(), err)
	}

	select {
	case m := <-ue.RatingChan:
		var sua charging_datatype.ServiceUsageResponse
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
