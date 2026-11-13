package processor

import (
	"net/http"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/require"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/openapi"
	"github.com/free5gc/openapi/models"
)

func TestChargingNotificationWithoutOAuth(t *testing.T) {
	const baseURL = "http://smf.example"
	openapi.InterceptInnerHttp2Client(t, false)
	t.Cleanup(gock.Off)
	self := chf_context.GetSelf()
	previous := self.OAuth2Required
	self.OAuth2Required = true
	t.Cleanup(func() { self.OAuth2Required = previous })
	gock.New(baseURL).Post("/notify").AddMatcher(func(req *http.Request, _ *gock.Request) (bool, error) {
		_, present := req.Header["Authorization"]
		return !present, nil
	}).Reply(http.StatusNoContent)

	p := &Processor{}
	p.SendChargingNotification(baseURL+"/notify", models.Chf_ConvCharging_ChargingNotifyRequest{})
	require.True(t, gock.IsDone(), "expected notification without Authorization")
}
