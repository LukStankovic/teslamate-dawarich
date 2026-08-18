package syncer

import (
	"strings"

	"github.com/LukStankovic/teslamate-dawarich/internal/dawarich"
	"github.com/LukStankovic/teslamate-dawarich/internal/teslamate"
)

const kmhPerMeterPerSecond = 3.6

func toPoint(position teslamate.Position, trackerPrefix string) dawarich.Point {
	point := dawarich.Point{
		Latitude:       position.Latitude,
		Longitude:      position.Longitude,
		Timestamp:      position.Date,
		AltitudeMeters: position.ElevationMeters,
		BatteryPercent: position.BatteryPercent,
		DeviceID:       deviceID(trackerPrefix, position.CarName),
	}

	if position.SpeedKmh != nil {
		metersPerSecond := float64(*position.SpeedKmh) / kmhPerMeterPerSecond
		point.SpeedMS = &metersPerSecond
	}
	if position.PowerKilowatts != nil {
		chargingDrawsNegativePower := *position.PowerKilowatts < 0
		point.Charging = &chargingDrawsNegativePower
	}
	return point
}

func deviceID(prefix, carName string) string {
	prefix, carName = strings.TrimSpace(prefix), strings.TrimSpace(carName)
	switch {
	case prefix == "":
		return carName
	case carName == "":
		return prefix
	default:
		return prefix + carName
	}
}
