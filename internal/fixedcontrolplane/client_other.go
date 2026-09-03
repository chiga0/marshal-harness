//go:build !darwin || !arm64

package fixedcontrolplane

import (
	"context"
	"time"

	"github.com/chiga0/marshal-harness/internal/application"
	"github.com/chiga0/marshal-harness/internal/productionruntime"
)

type StartRunClientResult struct {
	Projection application.RunStartProjection
	Receipt    productionruntime.FixedDeliveryReceipt
}

func CallStatus(context.Context, *productionruntime.FixedEndpointAuthority, string, time.Time) (application.StatusProjection, error) {
	return application.StatusProjection{}, ErrUnavailable
}
func CallInspectRun(context.Context, *productionruntime.FixedEndpointAuthority, string, application.InspectRunRequest, time.Time) (application.RunProjection, error) {
	return application.RunProjection{}, ErrUnavailable
}
func CallStartRun(context.Context, *productionruntime.FixedEndpointAuthority, string, application.StartRunRequest, time.Time) (StartRunClientResult, error) {
	return StartRunClientResult{}, ErrUnavailable
}
