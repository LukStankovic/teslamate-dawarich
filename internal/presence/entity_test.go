package presence

import (
	"context"
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/homeassistant"
)

type fakeHistory struct {
	changes []homeassistant.StateChange
}

func (f *fakeHistory) History(context.Context, string, time.Time, time.Time) ([]homeassistant.StateChange, error) {
	return f.changes, nil
}

func change(state string, minutesIntoDrive int) homeassistant.StateChange {
	return homeassistant.StateChange{State: state, Since: driveStart.Add(time.Duration(minutesIntoDrive) * time.Minute)}
}

func TestEntitySignal(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		changes   []homeassistant.StateChange
		wantKnown bool
		want      Verdict
	}{
		"connected before the drive and throughout": {
			[]homeassistant.StateChange{change("on", -30)}, true, Present,
		},
		"connected a minute after starting": {
			[]homeassistant.StateChange{change("off", -30), change("on", 1)}, true, Present,
		},
		"connected only while handing the car over": {
			[]homeassistant.StateChange{change("on", -5), change("off", 1)}, true, Absent,
		},
		"never connected": {
			[]homeassistant.StateChange{change("off", -120)}, true, Absent,
		},
		"phone unavailable for most of the drive": {
			[]homeassistant.StateChange{change("on", -10), change("unavailable", 1), change("off", 9)}, false, Pending,
		},
		"phone unavailable for the whole drive": {
			[]homeassistant.StateChange{change("unavailable", -10)}, false, Pending,
		},
		"entity has no history": {
			nil, false, Pending,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			signal := NewEntitySignal(&fakeHistory{changes: tc.changes}, "binary_sensor.in_tesla")
			verdict, known, err := signal.Witness(context.Background(), finishedDrive())
			if err != nil {
				t.Fatalf("Witness: %v", err)
			}
			if known != tc.wantKnown || (known && verdict != tc.want) {
				t.Errorf("verdict = %v (known %v), want %v (known %v)", verdict, known, tc.want, tc.wantKnown)
			}
		})
	}
}
