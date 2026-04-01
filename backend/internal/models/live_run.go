package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// LiveRunSplit : un kilomètre annoncé (temps sur ce km, allure implicite).
type LiveRunSplit struct {
	Km               int     `bson:"km" json:"km"`
	SplitSec         float64 `bson:"split_sec" json:"split_sec"`
	PaceSecPerKm     float64 `bson:"pace_sec_per_km" json:"pace_sec_per_km"`
	EndTimestampMs   int64   `bson:"end_timestamp_ms" json:"end_timestamp_ms"`
}

// LiveRunTrackPoint : échantillon de trace (navigateur).
type LiveRunTrackPoint struct {
	Lat        float64  `bson:"lat" json:"lat"`
	Lng        float64  `bson:"lng" json:"lng"`
	TMs        int64    `bson:"t_ms" json:"t_ms"`
	AccuracyM  *float64 `bson:"accuracy_m,omitempty" json:"accuracy_m,omitempty"`
	AltM       *float64 `bson:"alt_m,omitempty" json:"alt_m,omitempty"`
	HeadingDeg *float64 `bson:"heading_deg,omitempty" json:"heading_deg,omitempty"`
	SpeedMps   *float64 `bson:"speed_mps,omitempty" json:"speed_mps,omitempty"`
}

// LiveRun : session course en direct enregistrée depuis le front.
type LiveRun struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID    primitive.ObjectID `bson:"user_id" json:"-"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`

	TargetKm   float64 `bson:"target_km" json:"target_km"`
	DistanceM  float64 `bson:"distance_m" json:"distance_m"`
	MovingSec  float64 `bson:"moving_sec" json:"moving_sec"`
	WallSec    float64 `bson:"wall_sec" json:"wall_sec"`
	GpsStartTsMs int64 `bson:"gps_start_ts_ms" json:"gps_start_ts_ms"`
	GpsEndTsMs   int64 `bson:"gps_end_ts_ms" json:"gps_end_ts_ms"`

	AvgPaceSecPerKm    float64  `bson:"avg_pace_sec_per_km" json:"avg_pace_sec_per_km"`
	MaxImpliedSpeedKmh float64  `bson:"max_implied_speed_kmh,omitempty" json:"max_implied_speed_kmh,omitempty"`
	Splits             []LiveRunSplit `bson:"splits" json:"splits"`
	TrackPoints        []LiveRunTrackPoint `bson:"track_points,omitempty" json:"track_points,omitempty"`

	ClientVersion      string `bson:"client_version,omitempty" json:"client_version,omitempty"`
	UserAgent          string `bson:"user_agent,omitempty" json:"user_agent,omitempty"`
	NavigatorLanguage  string `bson:"navigator_language,omitempty" json:"navigator_language,omitempty"`
	ScreenW            int    `bson:"screen_w,omitempty" json:"screen_w,omitempty"`
	ScreenH            int    `bson:"screen_h,omitempty" json:"screen_h,omitempty"`
	OnlineAtEnd        bool   `bson:"online_at_end,omitempty" json:"online_at_end,omitempty"`
	AutoPauseDetected  bool   `bson:"auto_pause_detected,omitempty" json:"auto_pause_detected,omitempty"`
}

// LiveRunListItem : résumé pour GET /api/live-runs (sans trace complète).
type LiveRunListItem struct {
	ID               string  `json:"id"`
	CreatedAt        string  `json:"created_at"`
	TargetKm         float64 `json:"target_km"`
	DistanceM        float64 `json:"distance_m"`
	MovingSec        float64 `json:"moving_sec"`
	WallSec          float64 `json:"wall_sec"`
	AvgPaceSecPerKm  float64 `json:"avg_pace_sec_per_km"`
	SplitCount       int     `json:"split_count"`
}
