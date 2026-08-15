package main

import (
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/broker/application"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
)

func TestScheduleProjectionPrecedenceAndConsumption(t *testing.T) {
	hold := application.Hold{WALSequence: 40, ReasonCode: "hold", LeaseUntil: time.Now().Add(time.Minute)}
	reviewing := application.OperatorInput{WALSequence: 35, Status: application.OperatorInputReviewing}
	pause := &application.ControlRequest{Kind: application.ControlPause, WALSequence: 10, ReasonCode: "pause"}
	stop := &application.ControlRequest{Kind: application.ControlStop, WALSequence: 20, ReasonCode: "stop"}
	completion := &application.Completion{WALSequence: 30, Summary: "done"}

	for name, test := range map[string]struct {
		projection broker.SessionScheduleResult
		want       stadoruntime.ScheduleState
	}{
		"hold":       {broker.SessionScheduleResult{ActiveHolds: []application.Hold{hold}}, stadoruntime.ScheduleHeld},
		"reviewing":  {broker.SessionScheduleResult{ReviewingOperatorInputs: []application.OperatorInput{reviewing}, ActiveHolds: []application.Hold{hold}}, stadoruntime.ScheduleHeld},
		"pause":      {broker.SessionScheduleResult{ReviewingOperatorInputs: []application.OperatorInput{reviewing}, ActiveHolds: []application.Hold{hold}, LatestPause: pause}, stadoruntime.SchedulePaused},
		"completion": {broker.SessionScheduleResult{ReviewingOperatorInputs: []application.OperatorInput{reviewing}, ActiveHolds: []application.Hold{hold}, LatestPause: pause, LatestCompletion: completion}, stadoruntime.ScheduleCompleted},
		"stop":       {broker.SessionScheduleResult{ReviewingOperatorInputs: []application.OperatorInput{reviewing}, ActiveHolds: []application.Hold{hold}, LatestPause: pause, LatestCompletion: completion, LatestStop: stop}, stadoruntime.ScheduleStopped},
	} {
		t.Run(name, func(t *testing.T) {
			session := &BrokerSession{}
			got := session.scheduleStatusFromProjection(test.projection)
			if got.State != test.want {
				t.Fatalf("state=%s want=%s", got.State, test.want)
			}
		})
	}

	reviewStatus := (&BrokerSession{}).scheduleStatusFromProjection(broker.SessionScheduleResult{
		ReviewingOperatorInputs: []application.OperatorInput{reviewing},
	})
	if reviewStatus.ReasonCode != "operator-input.reviewing" || reviewStatus.Sequence != reviewing.WALSequence {
		t.Fatalf("reviewing status=%+v", reviewStatus)
	}

	session := &BrokerSession{}
	projection := broker.SessionScheduleResult{ActiveHolds: []application.Hold{hold}, LatestCompletion: completion}
	if got := session.scheduleStatusFromProjection(projection); got.State != stadoruntime.ScheduleCompleted {
		t.Fatalf("first projection state=%s", got.State)
	}
	if got := session.scheduleStatusFromProjection(projection); got.State != stadoruntime.ScheduleHeld {
		t.Fatalf("consumed completion state=%s, want held", got.State)
	}
}
