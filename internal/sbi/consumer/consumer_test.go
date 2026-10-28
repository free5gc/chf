package consumer

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/pkg/app"
	"github.com/free5gc/chf/pkg/factory"
)

func newTestConsumer(t *testing.T, ctx *chf_context.CHFContext) *Consumer {
	t.Helper()

	controller := gomock.NewController(t)
	mockApp := app.NewMockApp(controller)
	mockApp.EXPECT().Context().Return(ctx).AnyTimes()
	mockApp.EXPECT().Config().Return(&factory.Config{
		Configuration: &factory.Configuration{},
	}).AnyTimes()

	testConsumer, err := NewConsumer(mockApp)
	require.NoError(t, err)

	return testConsumer
}
