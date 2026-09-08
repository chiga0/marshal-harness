package cli

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTeamProgressShortActionsServeEveryPhaseDespiteBufferedTicks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 8)
	for range 8 {
		ticks <- time.Now()
	}
	close(ticks)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var order []int
	actions := make([]func(context.Context) error, 4)
	for i := range actions {
		actions[i] = func(step context.Context) error {
			order = append(order, i)
			if len(order) == 1 {
				close(entered)
				select {
				case <-release:
				case <-step.Done():
					return step.Err()
				}
			}
			return nil
		}
	}
	go func() {
		defer close(done)
		driveResidentShortActions(ctx, ticks, actions, func(err error) { t.Error(err) })
	}()
	<-entered // Slow first callback has all subsequent ticks waiting already.
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not drain")
	}
	if !reflect.DeepEqual(order, []int{0, 1, 2, 3, 0, 1, 2, 3}) {
		t.Fatalf("phase starvation: %v", order)
	}
}

func TestTeamProgressLongActionWaitsForAdmissionThenYieldsAndDrains(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workers sync.WaitGroup
	entered, allowAdmission, running := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	advance := newResidentLongAction(ctx, &workers, time.Minute, func(step context.Context, admitted func()) error {
		calls.Add(1)
		close(entered)
		select {
		case <-allowAdmission:
		case <-step.Done():
			return step.Err()
		}
		admitted()
		admitted() // Idempotent notification does not release execution twice.
		close(running)
		<-step.Done()
		return step.Err()
	}, func(err error) { t.Error(err) })
	turnDone := make(chan error, 1)
	go func() { turnDone <- advance(ctx) }()
	<-entered
	select {
	case <-turnDone:
		t.Fatal("yielded before lane/admission handshake")
	default:
	}
	close(allowAdmission)
	if err := <-turnDone; err != nil {
		t.Fatal(err)
	}
	<-running
	ticks := make(chan time.Time, 8)
	for range 8 {
		ticks <- time.Now()
	}
	close(ticks)
	counts := [3]int{}
	actions := []func(context.Context) error{
		func(context.Context) error { counts[0]++; return nil },
		func(context.Context) error { counts[1]++; return nil },
		func(context.Context) error { counts[2]++; return nil },
		advance,
	}
	driveResidentShortActions(ctx, ticks, actions, func(err error) { t.Error(err) })
	if counts != [3]int{2, 2, 2} || calls.Load() != 1 {
		t.Fatalf("long verify starved siblings or duplicated: %v calls=%d", counts, calls.Load())
	}
	cancel()
	workers.Wait()
}

func TestTeamProgressLongActionCancelsUnadmittedWork(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	turn, cancelTurn := context.WithCancel(parent)
	var workers sync.WaitGroup
	entered, stopped := make(chan struct{}), make(chan struct{})
	advance := newResidentLongAction(parent, &workers, time.Minute, func(step context.Context, _ func()) error {
		close(entered)
		<-step.Done()
		close(stopped)
		return nil
	}, func(err error) { t.Error(err) })
	done := make(chan error, 1)
	go func() { done <- advance(turn) }()
	<-entered
	cancelTurn()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("turn cancellation lost: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("unadmitted work continued")
	}
	workers.Wait()
}
