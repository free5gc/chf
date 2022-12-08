package rating_test

import (
	"testing"
	"time"

	tarrif_asn "github.com/free5gc/TarrifUtil/asn"
	"github.com/free5gc/TarrifUtil/tarrifType"
	"github.com/free5gc/chf/internal/rating"
	"github.com/stretchr/testify/require"
)

func TestRun(t *testing.T) {

	normalRequest := tarrifType.ServiceUsageRequest{
		SessionID: 1,
		SubscriptionID: &tarrifType.SubscriptionID{
			SubscriptionIDType: &tarrifType.SubscriptionIDType{Value: tarrifType.END_USER_IMSI},
			SubscriptionIDData: tarrif_asn.UTF8String("208930000000003"),
		},
		ActualTime: time.Now(),
		ServiceRating: &tarrifType.ServiceRating{
			RequestedUnits: 150,
			ConsumedUnits:  100,
			RequestSubType: &tarrifType.RequestSubType{
				Value: tarrifType.REQ_SUBTYPE_RESERVE,
			},
			CurrentTariff: &tarrifType.CurrentTariff{
				RateElement: &tarrifType.RateElement{
					UnitCost: &tarrifType.UnitCost{
						Exponent:    2,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: uint32(100000),
		},
	}

	expectResponse := tarrifType.ServiceUsageResponse{
		SessionID: 1,
		ServiceRating: &tarrifType.ServiceRating{
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

	lastGrantedQuotaRequest := tarrifType.ServiceUsageRequest{
		SessionID: 1,
		SubscriptionID: &tarrifType.SubscriptionID{
			SubscriptionIDType: &tarrifType.SubscriptionIDType{Value: tarrifType.END_USER_IMSI},
			SubscriptionIDData: tarrif_asn.UTF8String("208930000000003"),
		},
		ActualTime: time.Now(),
		ServiceRating: &tarrifType.ServiceRating{
			RequestedUnits: 1000,
			ConsumedUnits:  3500,
			RequestSubType: &tarrifType.RequestSubType{
				Value: tarrifType.REQ_SUBTYPE_RESERVE,
			},
			CurrentTariff: &tarrifType.CurrentTariff{
				RateElement: &tarrifType.RateElement{
					UnitCost: &tarrifType.UnitCost{
						Exponent:    1,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: uint32(remainQuota),
		},
	}

	expectResponse = tarrifType.ServiceUsageResponse{
		SessionID: 1,
		ServiceRating: &tarrifType.ServiceRating{
			Price:         uint32(70000),
			MonetaryQuota: uint32(80000),
			AllowedUnits:  uint32(500),
		},
	}
	rsp, _, lastgrantedquota = rating.ServiceUsageRetrieval(lastGrantedQuotaRequest)
	remainQuota = int(rsp.ServiceRating.MonetaryQuota - rsp.ServiceRating.Price)

	require.Equal(t, rsp, expectResponse)
	require.Equal(t, remainQuota, 10000)
	require.Equal(t, lastgrantedquota, true)

	outOfQuota := tarrifType.ServiceUsageRequest{
		SessionID: 1,
		SubscriptionID: &tarrifType.SubscriptionID{
			SubscriptionIDType: &tarrifType.SubscriptionIDType{Value: tarrifType.END_USER_IMSI},
			SubscriptionIDData: tarrif_asn.UTF8String("208930000000003"),
		},
		ActualTime: time.Now(),
		ServiceRating: &tarrifType.ServiceRating{
			RequestedUnits: 1000,
			ConsumedUnits:  600,
			RequestSubType: &tarrifType.RequestSubType{
				Value: tarrifType.REQ_SUBTYPE_RESERVE,
			},
			CurrentTariff: &tarrifType.CurrentTariff{
				RateElement: &tarrifType.RateElement{
					UnitCost: &tarrifType.UnitCost{
						Exponent:    1,
						ValueDigits: 2,
					},
				},
			},
			MonetaryQuota: uint32(remainQuota),
		},
	}
	expectResponse = tarrifType.ServiceUsageResponse{
		SessionID: 1,
		ServiceRating: &tarrifType.ServiceRating{
			Price:         uint32(12000),
			MonetaryQuota: uint32(10000),
			AllowedUnits:  uint32(0),
		},
	}
	rsp, _, lastgrantedquota = rating.ServiceUsageRetrieval(outOfQuota)
	remainQuota = int(rsp.ServiceRating.MonetaryQuota) - int(rsp.ServiceRating.Price)

	require.Equal(t, rsp, expectResponse)
	require.Equal(t, remainQuota, -2000)
	require.Equal(t, lastgrantedquota, false)

	time.Sleep(300 * time.Millisecond)
}
