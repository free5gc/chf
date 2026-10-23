package abmf

import (
	"fmt"
	"strconv"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm/smpeer"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
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

	request := *ccr
	ccr = &request
	ccr.DestinationRealm = datatype.DiameterIdentity(meta.OriginRealm)
	ccr.DestinationHost = datatype.DiameterIdentity(meta.OriginHost)

	msg := diam.NewRequest(charging_code.ABMF_CreditControl, charging_code.Re_interface, dict.Default)

	err = msg.Marshal(ccr)
	if err != nil {
		return nil, fmt.Errorf("marshal CCR Failed: %s", err)
	}

	_, err = msg.WriteTo(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to send message from %s: %s",
			conn.RemoteAddr(), err)
	}

	select {
	case m := <-ue.AcctChan:
		return decodeAccountAnswer(ccr, m)

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

// An error CCA, or a late answer for a different request, is not an acknowledgement.
func validateAccountAnswer(
	request *charging_datatype.AccountDebitRequest, response *charging_datatype.AccountDebitResponse,
) error {
	if response.ResultCode != diam.Success {
		return fmt.Errorf("ABMF rejected account operation: result=%d", response.ResultCode)
	}
	if response.SessionId != request.SessionId || response.CcRequestNumber != request.CcRequestNumber ||
		response.CcRequestType != request.CcRequestType {
		return fmt.Errorf("ABMF answer does not match account request")
	}
	return nil
}

func decodeAccountAnswer(
	request *charging_datatype.AccountDebitRequest, message *diam.Message,
) (*charging_datatype.AccountDebitResponse, error) {
	var response charging_datatype.AccountDebitResponse
	if err := message.Unmarshal(&response); err != nil {
		return nil, fmt.Errorf("decode account answer: %w", err)
	}
	if err := validateAccountAnswer(request, &response); err != nil {
		return nil, err
	}
	return &response, nil
}
