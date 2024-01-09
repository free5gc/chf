package abmf

import (
	"fmt"
	"strconv"
	"time"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/factory"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm/smpeer"
	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/logger"
)

func SendAccountDebitRequest(
	ue *chf_context.ChfUe,
	ccr *charging_datatype.AccountDebitRequest,
) (*charging_datatype.AccountDebitResponse, error) {
	ue.AbmfMux.Handle("CCA", HandleCCA(ue.AcctChan))
	abmfDiameter := factory.ChfConfig.Configuration.AbmfDiameter
	addr := abmfDiameter.HostIPv4 + ":" + strconv.Itoa(abmfDiameter.Port)
	conn, err := ue.AbmfClient.DialNetworkTLS(abmfDiameter.Protocol, addr, abmfDiameter.Tls.Pem, abmfDiameter.Tls.Key)

	if err != nil {
		return nil, err
	}

	meta, ok := smpeer.FromContext(conn.Context())
	if !ok {
		return nil, fmt.Errorf("peer metadata unavailable")
	}

	ccr.DestinationRealm = datatype.DiameterIdentity(meta.OriginRealm)
	ccr.DestinationHost = datatype.DiameterIdentity(meta.OriginHost)

	msg := diam.NewRequest(charging_code.ABMF_CreditControl, charging_code.Re_interface, dict.Default)

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

func HandleCCA(abmfChan chan *diam.Message) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		logger.AcctLog.Tracef("Received CCA from %s", c.RemoteAddr())

		abmfChan <- m
	}
}
