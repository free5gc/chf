package abmf

import (
	"bytes"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/stretchr/testify/require"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
)

func TestAccountAnswerRequiresSuccessAndMatchingIdentity(t *testing.T) {
	request := &charging_datatype.AccountDebitRequest{
		SessionId: "operation-7", CcRequestNumber: 7,
		CcRequestType: charging_datatype.UPDATE_REQUEST,
	}
	valid := charging_datatype.AccountDebitResponse{
		SessionId: request.SessionId, CcRequestNumber: 7,
		CcRequestType: request.CcRequestType, ResultCode: diam.Success,
	}
	require.NoError(t, validateAccountAnswer(request, &valid))
	for _, field := range []string{"result", "session", "sequence", "type"} {
		t.Run(field, func(t *testing.T) {
			answer := valid
			switch field {
			case "result":
				answer.ResultCode = diam.UnableToComply
			case "session":
				answer.SessionId = "old-operation"
			case "sequence":
				answer.CcRequestNumber++
			case "type":
				answer.CcRequestType = charging_datatype.TERMINATION_REQUEST
			}
			require.Error(t, validateAccountAnswer(request, &answer))
		})
	}
}

func TestAccountWireAnswerRejectsFailureAndStaleResponse(t *testing.T) {
	require.NoError(t, dict.Default.Load(bytes.NewReader([]byte(charging_dict.AbmfDictionary))))
	request := &charging_datatype.AccountDebitRequest{
		SessionId: "operation-7", CcRequestNumber: 7,
		CcRequestType: charging_datatype.UPDATE_REQUEST,
	}
	for _, kind := range []string{"success", "failure", "late answer"} {
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
			if kind == "late answer" {
				response.SessionId = "old-operation"
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
