//go:build !darwin && !linux

package terminal

import (
	"context"
	"time"
)

type unsupportedProcessController struct{}

func defaultProcessController() processController             { return unsupportedProcessController{} }
func (unsupportedProcessController) Supported() bool          { return false }
func (unsupportedProcessController) GroupID(int) (int, error) { return 0, ErrUnsupported }
func (unsupportedProcessController) Pause(int) ([]int, error) { return nil, ErrUnsupported }
func (unsupportedProcessController) Resume(int, []int) error  { return ErrUnsupported }
func (unsupportedProcessController) Terminate(context.Context, int, time.Duration) error {
	return ErrUnsupported
}
