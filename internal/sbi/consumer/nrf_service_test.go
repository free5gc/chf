package consumer

import (
	"context"
	"testing"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/app"
	"github.com/free5gc/openapi-r17"
	"github.com/h2non/gock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func Test_nnrfService_RegisterNFInstance(t *testing.T) {
	defer gock.Off() // Flush pending mocks after test execution

	// gock.InterceptClient(openapi.GetHttpClient())
	// defer gock.RestoreClient(openapi.GetHttpClient())
	openapi.InterceptH2CClient()
	defer openapi.RestoreH2CClient()

	gock.New("http://127.0.0.10:8000").
		Put("/nnrf-nfm/v1/nf-instances/1").
		Reply(200).
		JSON(map[string]string{})

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockApp := app.NewMockApp(ctrl)
	consumer, err := NewConsumer(mockApp)
	require.NoError(t, err)

	mockApp.EXPECT().Context().Times(1).Return(
		&chf_context.CHFContext{
			NrfUri: "http://127.0.0.10:8000",
			NfId:   "1",
		},
	)

	_, _, err = consumer.RegisterNFInstance(context.TODO())
	require.NoError(t, err)
}

func Test_nnrfService_SendDeregisterNFInstance(t *testing.T) {
	defer gock.Off() // Flush pending mocks after test execution

	// gock.InterceptClient(openapi.GetHttpClient())
	// defer gock.RestoreClient(openapi.GetHttpClient())
	openapi.InterceptH2CClient()
	defer openapi.RestoreH2CClient()

	gock.New("http://127.0.0.10:8000").
		Put("/nnrf-nfm/v1/nf-instances/1").
		Reply(200).
		JSON(map[string]string{})

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockApp := app.NewMockApp(ctrl)
	consumer, err := NewConsumer(mockApp)
	require.NoError(t, err)

	mockApp.EXPECT().Context().Times(1).Return(
		&chf_context.CHFContext{
			NrfUri: "http://127.0.0.10:8000",
			NfId:   "1",
		},
	)

	_, err = consumer.SendDeregisterNFInstance()
	require.NoError(t, err)
}
