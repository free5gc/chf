package abmf

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

func SendAccountDebitRequest(ue *chf_context.ChfUe, ccr *charging_datatype.AccountDebitRequest) (*charging_datatype.AccountDebitResponse, error) {
	self := chf_context.CHF_Self()
	ue.AbmfMux.Handle("CCA", HandleCCA(ue.AcctChan))

	conn, err := ue.AbmfClient.DialNetwork("tcp", self.AbmfAddr)
	if err != nil {
		return nil, err
	}

	meta, ok := smpeer.FromContext(conn.Context())
	if !ok {
		return nil, fmt.Errorf("peer metadata unavailable")
	}

	ccr.DestinationRealm = datatype.DiameterIdentity(meta.OriginRealm)
	ccr.DestinationHost = datatype.DiameterIdentity(meta.OriginHost)

	msg := diam.NewRequest(rate_code.ABMF_CreditControl, rate_code.Re_interface, dict.Default)

	err = msg.Marshal(ccr)
	if err != nil {
		return nil, fmt.Errorf("Marshal CCR Failed: %s\n", err)
	}

	_, err = msg.WriteTo(conn)
	if err != nil {
		return nil, fmt.Errorf("Failed to send message from %s: %s\n",
			conn.RemoteAddr(), err)
	}

	select {
	case m := <-ue.AcctChan:
		var cca charging_datatype.AccountDebitResponse
		if err := m.Unmarshal(&cca); err != nil {
			return nil, fmt.Errorf("Failed to parse message from %v", err)
		}

		return &cca, nil
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("timeout: no rate answer received")
	}
}

func HandleCCA(rgChan chan *diam.Message) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		logger.RatingLog.Tracef("Received CCA from %s", c.RemoteAddr())

		rgChan <- m
	}
}
