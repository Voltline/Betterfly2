package abtest

import (
	"reflect"
	"testing"
	"time"
)

func TestNormalizeCreateExperimentDefaultsAndDerivesEndTime(t *testing.T) {
	start := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	req := CreateExperimentRequest{
		ExperimentKey:   "  home_feed  ",
		Name:            "  Home Feed  ",
		StartTime:       start.Format(time.RFC3339),
		DurationSeconds: 3600,
	}
	if err := normalizeCreateExperiment(&req); err != nil {
		t.Fatal(err)
	}
	if req.ExperimentKey != "home_feed" || req.Name != "Home Feed" {
		t.Fatalf("names were not normalized: %+v", req)
	}
	if req.ExperimentType != ExperimentTypeClient || req.Status != StatusDraft || req.Salt != "home_feed" {
		t.Fatalf("defaults were not applied: %+v", req)
	}
	if req.EndTime != start.Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("unexpected derived end time: %s", req.EndTime)
	}
}

func TestNormalizeCreateExperimentRejectsInvalidInputs(t *testing.T) {
	valid := CreateExperimentRequest{
		ExperimentKey: "test", Name: "Test", StartTime: time.Now().UTC().Format(time.RFC3339), DurationSeconds: 60,
	}
	tests := []struct {
		name   string
		mutate func(*CreateExperimentRequest)
	}{
		{name: "missing key", mutate: func(req *CreateExperimentRequest) { req.ExperimentKey = " " }},
		{name: "invalid type", mutate: func(req *CreateExperimentRequest) { req.ExperimentType = "mobile" }},
		{name: "invalid status", mutate: func(req *CreateExperimentRequest) { req.Status = "unknown" }},
		{name: "rolled out directly", mutate: func(req *CreateExperimentRequest) { req.Status = StatusRolledOut }},
		{name: "non-positive duration", mutate: func(req *CreateExperimentRequest) { req.DurationSeconds = 0 }},
		{name: "invalid start", mutate: func(req *CreateExperimentRequest) { req.StartTime = "tomorrow" }},
		{name: "invalid end", mutate: func(req *CreateExperimentRequest) { req.EndTime = "later" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			tt.mutate(&req)
			if err := normalizeCreateExperiment(&req); err == nil {
				t.Fatalf("expected invalid input to be rejected: %+v", req)
			}
		})
	}
}

func TestNormalizeGroupAndOverrideInputs(t *testing.T) {
	group := GroupInput{GroupKey: " variant ", TrafficRatio: 37.5}
	if err := normalizeGroupInput(&group); err != nil {
		t.Fatal(err)
	}
	if group.GroupKey != "variant" || group.TrafficBasisPoints != 3750 {
		t.Fatalf("unexpected normalized group: %+v", group)
	}
	for _, basisPoints := range []int{-1, 10001} {
		invalid := GroupInput{GroupKey: "group", TrafficBasisPoints: basisPoints}
		if err := normalizeGroupInput(&invalid); err == nil {
			t.Fatalf("expected traffic %d to be rejected", basisPoints)
		}
	}

	validOverride := OverrideInput{SubjectType: " device ", SubjectID: " phone-1 ", Action: OverrideForceGroup, GroupKey: "variant"}
	if err := normalizeOverrideInput(&validOverride); err != nil {
		t.Fatal(err)
	}
	if validOverride.SubjectType != "device" || validOverride.SubjectID != "phone-1" {
		t.Fatalf("override identity was not normalized: %+v", validOverride)
	}
	invalidOverrides := []OverrideInput{
		{SubjectType: "device", SubjectID: "phone-1", Action: OverrideForceGroup},
		{SubjectType: "device", SubjectID: "phone-1", Action: "unknown"},
	}
	for _, override := range invalidOverrides {
		if err := normalizeOverrideInput(&override); err == nil {
			t.Fatalf("expected override to be rejected: %+v", override)
		}
	}
}

func TestJSONHelpersAndTargetingConversions(t *testing.T) {
	original := map[string]interface{}{"flag": true, "nested": map[string]interface{}{"value": "x"}}
	raw, err := mapToJSON(original)
	if err != nil {
		t.Fatal(err)
	}
	if decoded := jsonToMap(raw); !reflect.DeepEqual(decoded, original) {
		t.Fatalf("JSON round trip mismatch: got %#v want %#v", decoded, original)
	}
	if got := jsonToMap("not-json"); len(got) != 0 {
		t.Fatalf("malformed JSON should return an empty map: %#v", got)
	}
	if got := stringSlice([]interface{}{" ios ", 42, "android"}); !reflect.DeepEqual(got, []string{"ios", "android"}) {
		t.Fatalf("unexpected string slice conversion: %#v", got)
	}
	if !containsString([]string{"ios", "android"}, "android") || containsString([]string{"ios"}, "web") {
		t.Fatal("containsString returned an unexpected result")
	}
}
