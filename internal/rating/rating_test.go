package rating

import (
	"strings"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/stretchr/testify/require"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
	"github.com/free5gc/chf/internal/charging"
)

func TestDecodeServiceUsageAnswerChecksResultCode(t *testing.T) {
	require.NoError(t, dict.Default.Load(strings.NewReader(charging_dict.RateDictionary)))
	request := &charging_datatype.ServiceUsageRequest{
		SessionId: "request-id", ServiceRating: &charging_datatype.ServiceRating{ServiceIdentifier: 13},
	}
	for _, tt := range []struct {
		name string
		code uint32
	}{
		{"success", diam.Success},
		{"missing tariff", charging_code.DiameterUserUnknown},
		{"backend failure", diam.UnableToComply},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message := diam.NewRequest(charging_code.ServiceUsageMessage, charging_code.Re_interface, dict.Default)
			answer := message.Answer(tt.code)
			require.NoError(t, answer.Marshal(&charging_datatype.ServiceUsageResponse{
				SessionId: request.SessionId, ResultCode: datatype.Unsigned32(tt.code),
				ServiceRating: &charging_datatype.ServiceRating{},
			}))
			response, err := decodeServiceUsageAnswer(request, answer)
			switch tt.code {
			case diam.Success:
				require.NoError(t, err)
				require.NotNil(t, response)
			case charging_code.DiameterUserUnknown:
				require.ErrorIs(t, err, charging.ErrChargingDataNotFound)
				require.Nil(t, response)
			default:
				require.Error(t, err)
				require.NotErrorIs(t, err, charging.ErrChargingDataNotFound)
				require.Nil(t, response)
			}
		})
	}
}
