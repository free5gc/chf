// Copyright 2013-2015 go-diameter authors.  All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Diameter server example. This is by no means a complete server.
//
// If you'd like to test diameter over SSL, generate SSL certificates:
//   go run $GOROOT/src/crypto/tls/generate_cert.go --host localhost
//
// And start the server with `-cert_file cert.pem -key_file key.pem`.
//
// By default this server runs in a single OS thread. If you want to
// make it run on more, set the GOMAXPROCS=n environment variable.
// See Go's FAQ for details: http://golang.org/doc/faq#Why_no_multi_CPU

package abmf

import (
	"bytes"
	"context"
	_ "net/http/pprof"
	"strconv"
	"sync"
	"time"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm"

	charging_datatype "github.com/free5gc/chf/ccs_diameter/datatype"
	charging_dict "github.com/free5gc/chf/ccs_diameter/dict"
	"github.com/free5gc/chf/internal/charging"
	"github.com/free5gc/chf/internal/logger"
	"github.com/free5gc/chf/pkg/factory"
)

func OpenServer(ctx context.Context, wg *sync.WaitGroup, accounts *charging.Accounts) {
	// Load our custom dictionary on top of the default one, which
	// always have the Base Protocol (RFC6733) and Credit Control
	// Application (RFC4006).
	logger.AcctLog.Infof("Open Account Balance Management Server")

	err := dict.Default.Load(bytes.NewReader([]byte(charging_dict.AbmfDictionary)))
	if err != nil {
		logger.RatingLog.Error(err)
	}
	settings := &sm.Settings{
		OriginHost:       datatype.DiameterIdentity("server"),
		OriginRealm:      datatype.DiameterIdentity("go-diameter"),
		VendorID:         13,
		ProductName:      "go-diameter",
		FirmwareRevision: 1,
	}

	// Create the state machine (mux) and set its message handlers.
	mux := sm.New(settings)
	mux.Handle("CCR", handleCCR(accounts))
	mux.HandleFunc("ALL", handleALL) // Catch all.

	// Print error reports.
	go printErrors(mux.ErrorReports())
	go func() {
		defer func() {
			logger.AcctLog.Infoln("ABMF server stopped")
			wg.Done()
		}()
		<-ctx.Done()
	}()
	abmfDiameter := factory.ChfConfig.Configuration.AbmfDiameter
	addr := abmfDiameter.HostIPv4 + ":" + strconv.Itoa(abmfDiameter.Port)
	go func() {
		errListen := diam.ListenAndServeTLS(addr, abmfDiameter.Tls.Pem, abmfDiameter.Tls.Key, mux, nil)
		if errListen != nil {
			logger.AcctLog.Errorf("ABMF server fail to listen: %V", errListen)
		}
	}()
}

func printErrors(ec <-chan *diam.ErrorReport) {
	for err := range ec {
		logger.AcctLog.Errorf("Diam Error Report: %v", err)
	}
}

// Commenting the unused function
// func listen(addr, cert, key string, handler diam.Handler) error {
// 	// Start listening for connections.
// 	if len(cert) > 0 && len(key) > 0 {
// 		logger.AcctLog.Infof("Starting secure diameter server on %s", addr)
// 		return diam.ListenAndServeTLS(addr, cert, key, handler, nil)
// 	}

// 	logger.AcctLog.Infof("Starting diameter server on %s", addr)
// 	return diam.ListenAndServe(addr, handler, nil)
// }

// Diameter Credit-Control-Request
func handleCCR(accounts *charging.Accounts) diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		var request charging_datatype.AccountDebitRequest
		if err := m.Unmarshal(&request); err != nil {
			logger.AcctLog.Errorf("Parse CCR: %v", err)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		response, err := accounts.Apply(ctx, &request)
		resultCode := uint32(diam.Success)
		if err != nil {
			logger.AcctLog.Errorf("Account operation failed: %v", err)
			resultCode = diam.UnableToComply
			response = &charging_datatype.AccountDebitResponse{
				SessionId: request.SessionId, CcRequestNumber: request.CcRequestNumber,
				CcRequestType: request.CcRequestType, ResultCode: datatype.Unsigned32(resultCode),
				OriginHost: request.DestinationHost, OriginRealm: request.DestinationRealm,
			}
		}
		answer := m.Answer(resultCode)
		if err = answer.Marshal(response); err != nil {
			logger.AcctLog.Errorf("Marshal CCA: %v", err)
			return
		}
		if _, err = answer.WriteTo(c); err != nil {
			logger.AcctLog.Errorf("Write CCA: %v", err)
		}
	}
}

func handleALL(c diam.Conn, m *diam.Message) {
	logger.AcctLog.Warnf("Received unexpected message from %s:\n%s", c.RemoteAddr(), m)
}
