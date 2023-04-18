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
	"log"
	"math"
	"strconv"
	"sync"
	"time"

	_ "net/http/pprof"

	charging_datatype "github.com/free5gc/ChargingUtil/datatype"
	"github.com/free5gc/util/mongoapi"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/fiorix/go-diameter/diam"
	"github.com/fiorix/go-diameter/diam/datatype"
	"github.com/fiorix/go-diameter/diam/dict"
	"github.com/fiorix/go-diameter/diam/sm"
	charging_dict "github.com/free5gc/ChargingUtil/dict"
	"github.com/free5gc/chf/internal/logger"
)

const chargingDataColl = "chargingData"

func OpenServer(wg *sync.WaitGroup) {
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
	mux.Handle("CCR", handleCCR())
	mux.HandleFunc("ALL", handleALL) // Catch all.

	// Print error reports.
	go printErrors(mux.ErrorReports())
	go func() {
		defer func() {
			logger.AcctLog.Error("ABMF server stopped")
			wg.Done()
		}()

		err := listen(":3869", "", "", mux)
		if err != nil {
			log.Fatal(err)
		}
	}()
}

func printErrors(ec <-chan *diam.ErrorReport) {
	for err := range ec {
		logger.AcctLog.Errorf("Diam Error Report: %v", err)
	}
}

func listen(addr, cert, key string, handler diam.Handler) error {
	// Start listening for connections.
	if len(cert) > 0 && len(key) > 0 {
		logger.AcctLog.Infof("Starting secure diameter server on", addr)
		return diam.ListenAndServeTLS(addr, cert, key, handler, nil)
	}

	logger.AcctLog.Infof("Starting diameter server on", addr)
	return diam.ListenAndServe(addr, handler, nil)
}

func handleCCR() diam.HandlerFunc {
	return func(c diam.Conn, m *diam.Message) {
		var ccr charging_datatype.AccountDebitRequest
		var cca charging_datatype.AccountDebitResponse
		var subscriberId string
		var creditControl *charging_datatype.MultipleServicesCreditControl

		if err := m.Unmarshal(&ccr); err != nil {
			logger.AcctLog.Errorf("Failed to parse message from %s: %s\n%s",
				c.RemoteAddr(), err, m)
			return
		}

		switch ccr.SubscriptionId.SubscriptionIdType {
		case charging_datatype.END_USER_IMSI:
			subscriberId = "imsi-" + string(ccr.SubscriptionId.SubscriptionIdData)
		}

		mscc := ccr.MultipleServicesCreditControl
		rg := mscc.RatingGroup

		// Retrieve quota into mongoDB
		filter := bson.M{"ueId": subscriberId, "ratingGroup": rg}
		chargingInterface, err := mongoapi.RestfulAPIGetOne(chargingDataColl, filter)
		if err != nil {
			logger.AcctLog.Errorf("Get quota error: %+v", err)
		}

		quotaStr := chargingInterface["quota"].(string)
		quota, _ := strconv.ParseInt(quotaStr, 10, 64)

		switch ccr.RequestedAction {
		case charging_datatype.CHECK_BALANCE:
			logger.AcctLog.Errorf("CHECK_BALANCE not supported")
		case charging_datatype.PRICE_ENQUIRY:
			logger.AcctLog.Errorf("Should use rating function for PRICE_ENQUIRY")
		case charging_datatype.REFUND_ACCOUNT:
			logger.AcctLog.Infof("Refund Account")
			refundQuota := int64(mscc.RequestedServiceUnit.CCTotalOctets)
			quota += refundQuota
		case charging_datatype.DIRECT_DEBITING:
			switch ccr.CcRequestType {
			case charging_datatype.INITIAL_REQUEST, charging_datatype.UPDATE_REQUEST:
				var finalUnitIndication *charging_datatype.FinalUnitIndication
				requestQuota := int64(mscc.RequestedServiceUnit.CCTotalOctets)
				if requestQuota > quota {
					logger.AcctLog.Warnf("Last granted quota")
					finalUnitIndication = &charging_datatype.FinalUnitIndication{
						FinalUnitAction: charging_datatype.TERMINATE,
					}

					requestQuota = quota
				}

				creditControl = &charging_datatype.MultipleServicesCreditControl{
					RatingGroup: rg,
					GrantedServiceUnit: &charging_datatype.GrantedServiceUnit{
						CCTotalOctets: datatype.Unsigned64(requestQuota),
					},
					FinalUnitIndication: finalUnitIndication,
				}

				quota -= requestQuota
			case charging_datatype.TERMINATION_REQUEST:
				usedQuota := int64(mscc.UsedServiceUnit.CCTotalOctets)
				quota -= usedQuota
			}

			// Convvert quota into valuedigits and expontent expresstion
			quotaStr = strconv.FormatInt(quota, 10)
			quotaInt, _ := strconv.ParseInt(quotaStr, 10, 64)
			quotaLen := len(quotaStr)
			quotaExp := quotaLen - 1
			quotaVal := quotaInt / int64(math.Pow10(quotaExp))

			cca = charging_datatype.AccountDebitResponse{
				SessionId:       ccr.SessionId,
				OriginHost:      ccr.DestinationHost,
				OriginRealm:     ccr.DestinationRealm,
				CcRequestType:   ccr.CcRequestType,
				CcRequestNumber: ccr.CcRequestNumber,
				EventTimestamp:  datatype.Time(time.Now()),
				RemainingBalance: &charging_datatype.RemainingBalance{
					UnitValue: &charging_datatype.UnitValue{
						ValueDigits: datatype.Integer64(quotaVal),
						Exponent:    datatype.Integer32(quotaExp),
					},
				},
				MultipleServicesCreditControl: creditControl,
			}

		}

		logger.AcctLog.Infof("UE [%s], Rating group [%d], quota [%d]", rg, subscriberId, quota)

		chargingBsonM := make(bson.M)
		chargingBsonM["quota"] = strconv.FormatInt(quota, 10)
		if _, err := mongoapi.RestfulAPIPutOne(chargingDataColl, filter, chargingBsonM); err != nil {
			logger.AcctLog.Errorf("RestfulAPIPutOne err: %+v", err)
		}

		a := m.Answer(diam.Success)

		err = a.Marshal(&cca)
		if err != nil {
			logger.AcctLog.Errorf("Marshal CCA Err: %+v:", err)
		}

		_, err = a.WriteTo(c)
		if err != nil {
			logger.AcctLog.Errorf("Failed to write message to %s: %s\n%s\n",
				c.RemoteAddr(), err, a)
			return
		}
	}
}

func handleALL(c diam.Conn, m *diam.Message) {
	logger.AcctLog.Warnf("Received unexpected message from %s:\n%s", c.RemoteAddr(), m)
}
