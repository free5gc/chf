package consumer

import (
	"context"

	chf_context "github.com/free5gc/chf/internal/context"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/openapi"
	"github.com/free5gc/openapi/models"
	"github.com/free5gc/openapi/oauth"
)

func GetTokenCtx(scope, targetNF string) (context.Context, *models.ProblemDetails, error) {
	if chf_context.GetSelf().OAuth2Required {
		logger.ConsumerLog.Debugln("GetToekenCtx")
		chfSelf := chf_context.GetSelf()
		tok, pd, err := oauth.SendAccTokenReq(chfSelf.NfId, models.NfType_CHF, scope, targetNF, chfSelf.NrfUri)
		if err != nil {
			return nil, pd, err
		}
		return context.WithValue(context.Background(),
			openapi.ContextOAuth2, tok), pd, nil
	}
	return context.TODO(), nil, nil
}
