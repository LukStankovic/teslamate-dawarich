package syncer

import (
	"testing"
	"time"

	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

func TestToPointConvertsSpeedToMetersPerSecond(t *testing.T) {
	t.Parallel()

	point := toPoint(teslamate.Position{SpeedKmh: ptr(int32(90))}, "")

	if point.SpeedMS == nil {
		t.Fatal("SpeedMS = nil, want 25")
	}
	if *point.SpeedMS < 24.99 || *point.SpeedMS > 25.01 {
		t.Errorf("SpeedMS = %v, want 25", *point.SpeedMS)
	}
}

func TestToPointReadsChargingFromPowerSign(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		power        *float64
		wantCharging *bool
	}{
		"negative power is charging":  {ptr(-11.0), ptr(true)},
		"positive power is discharge": {ptr(35.0), ptr(false)},
		"unreported power is unknown": {nil, nil},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			charging := toPoint(teslamate.Position{PowerKilowatts: tc.power}, "").Charging

			switch {
			case tc.wantCharging == nil && charging != nil:
				t.Errorf("Charging = %v, want nil", *charging)
			case tc.wantCharging != nil && charging == nil:
				t.Errorf("Charging = nil, want %v", *tc.wantCharging)
			case tc.wantCharging != nil && *charging != *tc.wantCharging:
				t.Errorf("Charging = %v, want %v", *charging, *tc.wantCharging)
			}
		})
	}
}

func TestToPointLeavesUnreportedFieldsUnset(t *testing.T) {
	t.Parallel()

	point := toPoint(teslamate.Position{CarName: "Model Y"}, "")

	if point.SpeedMS != nil {
		t.Errorf("SpeedMS = %v, want nil", *point.SpeedMS)
	}
	if point.AltitudeMeters != nil {
		t.Errorf("AltitudeMeters = %v, want nil", *point.AltitudeMeters)
	}
	if point.BatteryPercent != nil {
		t.Errorf("BatteryPercent = %v, want nil", *point.BatteryPercent)
	}
}

func TestToPointCopiesPositionFields(t *testing.T) {
	t.Parallel()

	position := teslamate.Position{
		CarName:         "Model Y",
		Date:            time.Date(2026, time.August, 18, 17, 42, 3, 0, time.UTC),
		Latitude:        50.0755,
		Longitude:       14.4378,
		ElevationMeters: ptr(int32(214)),
		BatteryPercent:  ptr(int32(72)),
	}

	point := toPoint(position, "tesla-")

	if point.Latitude != position.Latitude || point.Longitude != position.Longitude {
		t.Errorf("coordinates = %v,%v, want %v,%v",
			point.Latitude, point.Longitude, position.Latitude, position.Longitude)
	}
	if !point.Timestamp.Equal(position.Date) {
		t.Errorf("Timestamp = %v, want %v", point.Timestamp, position.Date)
	}
	if *point.AltitudeMeters != *position.ElevationMeters {
		t.Errorf("AltitudeMeters = %v, want %v", *point.AltitudeMeters, *position.ElevationMeters)
	}
	if *point.BatteryPercent != *position.BatteryPercent {
		t.Errorf("BatteryPercent = %v, want %v", *point.BatteryPercent, *position.BatteryPercent)
	}
}

func TestDeviceID(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prefix, carName, want string
	}{
		"prefix and name": {"tesla-", "Model Y", "tesla-Model Y"},
		"name only":       {"", "Model Y", "Model Y"},
		"prefix only":     {"tesla-", "", "tesla-"},
		"whitespace":      {" tesla- ", " Model Y ", "tesla-Model Y"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := deviceID(tc.prefix, tc.carName); got != tc.want {
				t.Errorf("deviceID(%q, %q) = %q, want %q", tc.prefix, tc.carName, got, tc.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
