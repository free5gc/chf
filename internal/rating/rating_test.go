package rating_test

import (
	"testing"
	"time"

	"github.com/fiorix/go-diameter/diam/datatype"
	rate_datatype "github.com/free5gc/RatingUtil/dataType"
	"github.com/free5gc/chf/internal/rating"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {

	normalRequest := rate_datatype.ServiceUsageRequest{
		SessionId: datatype.UTF8String("1"),
		SubscriptionId: &rate_datatype.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String("208930000000003"),
		},
		ActualTime: datatype.Time(time.Now()),
		ServiceRating: &rate_datatype.ServiceRating{
			RequestedUnits: 150,
			ConsumedUnits:  100,
			RequestSubType: rate_datatype.REQ_SUBTYPE_RESERVE,
			MonetaryTariff: &rate_datatype.MonetaryTariff{
				RateElement: &rate_datatype.RateElement{
					UnitCost: &rate_datatype.UnitCost{
						Exponent:    2,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: datatype.Unsigned32(100000),
		},
	}

	expectResponse := rate_datatype.ServiceUsageResponse{
		SessionId: datatype.UTF8String("1"),
		ServiceRating: &rate_datatype.ServiceRating{
			Price:         20000,
			MonetaryQuota: 100000,
			AllowedUnits:  150,
		},
	}
	rsp, _, lastgrantedquota := rating.ServiceUsageRetrieval(normalRequest)
	remainQuota := int(rsp.ServiceRating.MonetaryQuota - rsp.ServiceRating.Price)

	require.Equal(t, rsp, expectResponse)
	require.Equal(t, remainQuota, 80000)
	require.Equal(t, lastgrantedquota, false)

	lastGrantedQuotaRequest := rate_datatype.ServiceUsageRequest{
		SessionId: datatype.UTF8String("2"),
		SubscriptionId: &rate_datatype.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String("208930000000003"),
		},
		ActualTime: datatype.Time(time.Now()),
		ServiceRating: &rate_datatype.ServiceRating{
			RequestedUnits: 1000,
			ConsumedUnits:  3500,
			RequestSubType: rate_datatype.REQ_SUBTYPE_RESERVE,
			MonetaryTariff: &rate_datatype.MonetaryTariff{
				RateElement: &rate_datatype.RateElement{
					UnitCost: &rate_datatype.UnitCost{
						Exponent:    1,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: datatype.Unsigned32(remainQuota),
		},
	}

	expectResponse = rate_datatype.ServiceUsageResponse{
		SessionId: datatype.UTF8String("2"),
		ServiceRating: &rate_datatype.ServiceRating{
			Price:         datatype.Unsigned32(70000),
			MonetaryQuota: datatype.Unsigned32(80000),
			AllowedUnits:  datatype.Unsigned32(500),
		},
	}
	rsp, _, lastgrantedquota = rating.ServiceUsageRetrieval(lastGrantedQuotaRequest)
	remainQuota = int(rsp.ServiceRating.MonetaryQuota - rsp.ServiceRating.Price)

	require.Equal(t, rsp, expectResponse)
	require.Equal(t, remainQuota, 10000)
	require.Equal(t, lastgrantedquota, true)

	outOfQuota := rate_datatype.ServiceUsageRequest{
		SessionId: datatype.UTF8String("3"),
		SubscriptionId: &rate_datatype.SubscriptionId{
			SubscriptionIdType: rate_datatype.END_USER_IMSI,
			SubscriptionIdData: datatype.UTF8String("208930000000003"),
		},
		ActualTime: datatype.Time(time.Now()),
		ServiceRating: &rate_datatype.ServiceRating{
			RequestedUnits: 1000,
			ConsumedUnits:  600,
			RequestSubType: rate_datatype.REQ_SUBTYPE_RESERVE,
			MonetaryTariff: &rate_datatype.MonetaryTariff{
				RateElement: &rate_datatype.RateElement{
					UnitCost: &rate_datatype.UnitCost{
						Exponent:    1,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: datatype.Unsigned32(remainQuota),
		},
	}
	expectResponse = rate_datatype.ServiceUsageResponse{
		SessionId: datatype.UTF8String("3"),
		ServiceRating: &rate_datatype.ServiceRating{
			Price:         datatype.Unsigned32(12000),
			MonetaryQuota: datatype.Unsigned32(10000),
			AllowedUnits:  datatype.Unsigned32(0),
		},
	}
	rsp, _, lastgrantedquota = rating.ServiceUsageRetrieval(outOfQuota)
	remainQuota = int(rsp.ServiceRating.MonetaryQuota) - int(rsp.ServiceRating.Price)

	require.Equal(t, rsp, expectResponse)
	require.Equal(t, remainQuota, -2000)
	require.Equal(t, lastgrantedquota, false)

	time.Sleep(300 * time.Millisecond)
}
