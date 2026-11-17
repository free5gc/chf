package abmf

import (
	"bytes"
	"sync"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/stretchr/testify/require"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
	"github.com/free5gc/chf/internal/charging"
)

var abmfDictionary sync.Once

func loadAbmfDictionary(t *testing.T) {
	t.Helper()
	abmfDictionary.Do(func() {
		require.NoError(t, dict.Default.Load(bytes.NewReader([]byte(charging_dict.AbmfDictionary))))
	})
}

func TestAccountWireAnswerRejectsFailureAndStaleResponse(t *testing.T) {
	loadAbmfDictionary(t)
	request := &charging_datatype.AccountDebitRequest{
		SessionId: "operation-7", CcRequestNumber: 7,
		CcRequestType: charging_datatype.UPDATE_REQUEST,
	}
	for _, kind := range []string{"success", "failure", "session", "sequence", "type"} {
		t.Run(kind, func(t *testing.T) {
			response := &charging_datatype.AccountDebitResponse{
				SessionId: request.SessionId, CcRequestNumber: 7,
				CcRequestType: request.CcRequestType, ResultCode: diam.Success,
			}
			code := uint32(diam.Success)
			if kind == "failure" {
				code = diam.UnableToComply
				response.ResultCode = diam.UnableToComply
			}
			switch kind {
			case "session":
				response.SessionId = "old-operation"
			case "sequence":
				response.CcRequestNumber++
			case "type":
				response.CcRequestType = charging_datatype.TERMINATION_REQUEST
			}
			message := diam.NewRequest(charging_code.ABMF_CreditControl, charging_code.Re_interface, dict.Default).Answer(code)
			require.NoError(t, message.Marshal(response))
			decoded, err := decodeAccountAnswer(request, message)
			if kind == "success" {
				require.NoError(t, err)
				require.Equal(t, request.SessionId, decoded.SessionId)
			} else {
				require.Error(t, err)
				require.Nil(t, decoded)
			}
		})
	}
}

func TestAccountWireAnswerCreditLimitReachedIsNoBalance(t *testing.T) {
	loadAbmfDictionary(t)
	request := &charging_datatype.AccountDebitRequest{
		SessionId: "operation-8", CcRequestNumber: 8,
		CcRequestType: charging_datatype.UPDATE_REQUEST,
	}
	for _, stale := range []bool{false, true} {
		response := &charging_datatype.AccountDebitResponse{
			SessionId: request.SessionId, CcRequestNumber: request.CcRequestNumber,
			CcRequestType: request.CcRequestType, ResultCode: charging_code.DiameterCreditLimitReached,
		}
		if stale {
			response.SessionId = "old-operation"
		}
		message := diam.NewRequest(charging_code.ABMF_CreditControl, charging_code.Re_interface, dict.Default).
			Answer(charging_code.DiameterCreditLimitReached)
		require.NoError(t, message.Marshal(response))
		decoded, err := decodeAccountAnswer(request, message)
		require.Nil(t, decoded)
		if stale {
			require.Error(t, err)
			require.NotErrorIs(t, err, charging.ErrNoBalance, "a stale answer is not this request's denial")
		} else {
			require.ErrorIs(t, err, charging.ErrNoBalance)
		}
	}
}
