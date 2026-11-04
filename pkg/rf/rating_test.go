package rf

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"sync"
	"testing"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"

	charging_code "github.com/free5gc/chf/ccs_diameter/code"
	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
)

var loadRateDictionary sync.Once

// answerRecorder is a diam.Conn that keeps what the handler writes.
type answerRecorder struct {
	bytes.Buffer
	ctx context.Context
}

func (r *answerRecorder) Close()                         {}
func (r *answerRecorder) LocalAddr() net.Addr            { return &net.TCPAddr{} }
func (r *answerRecorder) RemoteAddr() net.Addr           { return &net.TCPAddr{} }
func (r *answerRecorder) TLS() *tls.ConnectionState      { return nil }
func (r *answerRecorder) Dictionary() *dict.Parser       { return dict.Default }
func (r *answerRecorder) Context() context.Context       { return r.ctx }
func (r *answerRecorder) SetContext(ctx context.Context) { r.ctx = ctx }
func (r *answerRecorder) Connection() net.Conn           { return nil }

// rate sends handleSUR a reservation request for rating group 10 with a
// monetary quota of 10, answered from chargingData, and returns the answer.
func rate(t *testing.T, chargingData map[string]interface{}) *charging_datatype.ServiceUsageResponse {
	t.Helper()
	loadRateDictionary.Do(func() {
		require.NoError(t, dict.Default.Load(bytes.NewReader([]byte(charging_dict.RateDictionary))))
	})

	sur := &charging_datatype.ServiceUsageRequest{
		SessionId:   "1",
		OriginHost:  "chf",
		OriginRealm: "free5gc",
		SubscriptionId: &charging_datatype.SubscriptionId{
			SubscriptionIdType: charging_datatype.END_USER_IMSI,
			SubscriptionIdData: "208930000000001",
		},
		ServiceRating: &charging_datatype.ServiceRating{
			ServiceIdentifier: 10,
			MonetaryQuota:     10,
			RequestSubType:    charging_datatype.REQ_SUBTYPE_RESERVE,
		},
	}
	req := diam.NewRequest(charging_code.ServiceUsageMessage, charging_code.Re_interface, dict.Default)
	require.NoError(t, req.Marshal(sur))

	var filter bson.M
	conn := &answerRecorder{ctx: context.Background()}
	handleSUR(func(f bson.M) (map[string]interface{}, error) {
		filter = f
		return chargingData, nil
	})(conn, req)
	require.Equal(t, bson.M{"ueId": "imsi-208930000000001", "ratingGroup": uint32(10)}, filter)

	ans, err := diam.ReadMessage(conn, dict.Default)
	require.NoError(t, err, "no answer written")
	var sua charging_datatype.ServiceUsageResponse
	require.NoError(t, ans.Unmarshal(&sua))
	require.NotNil(t, sua.ServiceRating)
	return &sua
}

func TestHandleSURPricesWithUnitCost(t *testing.T) {
	sua := rate(t, map[string]interface{}{"ueId": "imsi-208930000000001", "ratingGroup": 10, "unitCost": "2"})

	require.Equal(t, datatype.Integer64(2), sua.ServiceRating.MonetaryTariff.RateElement.UnitCost.ValueDigits)
	require.Equal(t, datatype.Unsigned32(5), sua.ServiceRating.AllowedUnits)
}

// A document without unitCost, or with one that is not a string, is answered
// like a missing document: a unit cost of 1 and no allowed units.
func TestHandleSURWithoutUnitCost(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"no document":           nil,
		"no unitCost":           {"ueId": "imsi-208930000000001", "ratingGroup": 10, "quota": "1000"},
		"unitCost not a string": {"ueId": "imsi-208930000000001", "ratingGroup": 10, "unitCost": 2},
	}
	for name, chargingData := range cases {
		t.Run(name, func(t *testing.T) {
			sua := rate(t, chargingData)

			unitCost := sua.ServiceRating.MonetaryTariff.RateElement.UnitCost
			require.Equal(t, datatype.Integer64(1), unitCost.ValueDigits)
			require.Equal(t, datatype.Integer32(0), unitCost.Exponent)
			require.Equal(t, datatype.Unsigned32(0), sua.ServiceRating.AllowedUnits)
		})
	}
}
