package processor

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	"github.com/free5gc/chf/internal/charging"
	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi/models"
)

// Rating group 10 has a balance and rating group 20 has none. The ABMF answers
// rating group 20 with DIAMETER_CREDIT_LIMIT_REACHED, which the client decodes
// as charging.ErrNoBalance; any other account error stays an error.
func noBalanceForRatingGroup20(accountErr error) accountDebitFunc {
	return func(ue *chf_context.ChfUe, request *charging_datatype.AccountDebitRequest) (
		*charging_datatype.AccountDebitResponse, error,
	) {
		if request.MultipleServicesCreditControl.RatingGroup == 20 {
			return nil, accountErr
		}
		return grantQuotaRequest(ue, request)
	}
}

func fundedAndUnfundedUsage(requestCredit bool) []models.Chf_ConvCharging_MultipleUnitUsage {
	funded := onlineQuotaUsage("upf-a", 0, requestCredit)
	unfunded := onlineQuotaUsage("upf-a", 0, requestCredit)
	unfunded.RatingGroup = 20
	return []models.Chf_ConvCharging_MultipleUnitUsage{funded, unfunded}
}

func TestChargingRatingGroupWithoutBalanceGetsQuotaLimitReached(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	p.accountDebit = noBalanceForRatingGroup20(fmt.Errorf("account operation: %w", charging.ErrNoBalance))
	request.MultipleUnitUsage = fundedAndUnfundedUsage(true)

	response, problem := p.ChargingDataUpdate(request, ref)
	require.Nil(t, problem)
	require.Len(t, response.MultipleUnitInformation, 2)
	funded := response.MultipleUnitInformation[0]
	require.Equal(t, int32(10), funded.RatingGroup)
	require.Empty(t, funded.ResultCode)
	require.Equal(t, int32(100), funded.GrantedUnit.TotalVolume)
	require.Equal(t, models.Chf_ConvCharging_MultipleUnitInformation{
		ResultCode:  models.Chf_ConvCharging_ResultCode_QUOTA_LIMIT_REACHED,
		RatingGroup: 20,
		UPFID:       "upf-a",
	}, response.MultipleUnitInformation[1], "no grantedUnit, triggers or final unit indication")

	// The final report must not fail on rating group 20.
	request.InvocationSequenceNumber++
	request.MultipleUnitUsage = fundedAndUnfundedUsage(false)
	require.Nil(t, p.ChargingDataRelease(request, ref))
}

func TestChargingBalanceLookupFailureStaysAnError(t *testing.T) {
	p, request, ref := chargingRequestSession(t)
	p.serviceUsage = fixedQuotaRating(1)
	p.accountDebit = noBalanceForRatingGroup20(errors.New("ABMF rejected account operation: result=5012"))
	request.MultipleUnitUsage = fundedAndUnfundedUsage(true)

	_, problem := p.ChargingDataUpdate(request, ref)
	require.NotNil(t, problem)
	require.Equal(t, int32(http.StatusServiceUnavailable), problem.Status)
}
