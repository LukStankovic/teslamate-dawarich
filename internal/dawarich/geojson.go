package dawarich

import "time"

type batch struct {
	Locations []feature `json:"locations"`
}

type feature struct {
	Type       string     `json:"type"`
	Geometry   geometry   `json:"geometry"`
	Properties properties `json:"properties"`
}

type geometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

type properties struct {
	Timestamp    string   `json:"timestamp"`
	DeviceID     string   `json:"device_id,omitempty"`
	Altitude     *int32   `json:"altitude,omitempty"`
	Speed        *float64 `json:"speed,omitempty"`
	BatteryLevel *float64 `json:"battery_level,omitempty"`
	BatteryState string   `json:"battery_state,omitempty"`
	Course       *float64 `json:"course,omitempty"`
}

const percentPerFraction = 100

func features(points []Point) []feature {
	out := make([]feature, 0, len(points))
	for _, point := range points {
		out = append(out, feature{
			Type: "Feature",
			Geometry: geometry{
				Type:        "Point",
				Coordinates: longitudeLatitude(point),
			},
			Properties: properties{
				Timestamp:    point.Timestamp.UTC().Format(time.RFC3339Nano),
				DeviceID:     point.DeviceID,
				Altitude:     point.AltitudeMeters,
				Speed:        point.SpeedMS,
				BatteryLevel: batteryFraction(point.BatteryPercent),
				BatteryState: batteryState(point.Charging),
				Course:       point.CourseDegrees,
			},
		})
	}
	return out
}

func longitudeLatitude(point Point) [2]float64 {
	return [2]float64{point.Longitude, point.Latitude}
}

func batteryFraction(percent *int32) *float64 {
	if percent == nil {
		return nil
	}
	fraction := float64(*percent) / percentPerFraction
	return &fraction
}

func batteryState(charging *bool) string {
	switch {
	case charging == nil:
		return ""
	case *charging:
		return "charging"
	default:
		return "unplugged"
	}
}
