package charging

import (
	"math"
	"testing"

	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/stretchr/testify/require"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
)

func TestAccountArithmeticAndInvalidRequests(t *testing.T) {
	request := accountRequest()
	response, remaining, err := calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Zero(t, remaining)
	require.Equal(t, datatype.Unsigned64(20), response.MultipleServicesCreditControl.GrantedServiceUnit.CCTotalOctets)
	require.NotNil(t, response.MultipleServicesCreditControl.FinalUnitIndication)
	request.RequestedAction = charging_datatype.REFUND_ACCOUNT
	response, remaining, err = calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Equal(t, int64(60), remaining)
	require.Equal(t, datatype.Integer64(60), response.RemainingBalance.UnitValue.ValueDigits)
	_, _, err = calculateAccountResponse(request, math.MaxInt64)
	require.Error(t, err)
	request.MultipleServicesCreditControl.RequestedServiceUnit = nil
	_, _, err = calculateAccountResponse(request, 20)
	require.Error(t, err)
	request.RequestedAction = charging_datatype.DIRECT_DEBITING
	request.CcRequestType = charging_datatype.TERMINATION_REQUEST
	request.MultipleServicesCreditControl.UsedServiceUnit = &charging_datatype.UsedServiceUnit{CCTotalOctets: 40}
	_, remaining, err = calculateAccountResponse(request, 20)
	require.NoError(t, err)
	require.Equal(t, int64(-20), remaining)
	_, _, err = calculateAccountResponse(request, math.MinInt64)
	require.Error(t, err)
}
