/*
 * Nchf_SpendingLimitControl
 *
 * ConvergedCharging Service
 * © 2021, 3GPP Organizational Partners (ARIB, ATIS, CCSA, ETSI, TSDSI, TTA, TTC). All rights reserved.
 *
 * API version: 3.0.3
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package sbi

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *Server) getSpendingLimitControlRoutes() []Route {
	return []Route{
		{
			Name:    "Index",
			Method:  http.MethodGet,
			Pattern: "/",
			APIFunc: Index,
		},
		{
			Name:    "SubscriptionsPost",
			Method:  http.MethodPost,
			Pattern: "/subscriptions",
			APIFunc: s.SubscriptionsPost,
		},
		{
			Name:    "SubscriptionsSubscriptionIdDelete",
			Method:  http.MethodDelete,
			Pattern: "/subscriptions/:subscriptionId",
			APIFunc: s.SubscriptionsSubscriptionIdDelete,
		},
		{
			Name:    "SubscriptionsSubscriptionIdPut",
			Method:  http.MethodPut,
			Pattern: "/subscriptions/:subscriptionId",
			APIFunc: s.SubscriptionsSubscriptionIdPut,
		},
	}
}

func (s *Server) SubscriptionsPost(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{})
}

func (s *Server) SubscriptionsSubscriptionIdDelete(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{})
}

func (s *Server) SubscriptionsSubscriptionIdPut(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{})
}
