package abtest

import (
	"testing"
	"time"
)

type memoryStore struct {
	experiments []Experiment
}

func (s *memoryStore) ListExperiments() ([]Experiment, error) {
	return s.experiments, nil
}

func (s *memoryStore) GetExperiment(id int64) (Experiment, error) {
	for _, experiment := range s.experiments {
		if experiment.ID == id {
			return experiment, nil
		}
	}
	return Experiment{}, nil
}

func (s *memoryStore) CreateExperiment(req CreateExperimentRequest) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) SetExperimentStatus(id int64, status string) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) RenewExperiment(id int64, req RenewExperimentRequest) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) WithdrawExperiment(id int64) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	return Group{}, nil
}

func (s *memoryStore) UpdateGroup(experimentID, groupID int64, req GroupInput) (Group, error) {
	return Group{}, nil
}

func (s *memoryStore) DeleteGroup(experimentID, groupID int64) error {
	return nil
}

func (s *memoryStore) PushFullGroup(experimentID, groupID int64) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	return Override{}, nil
}

func (s *memoryStore) UpdateOverride(experimentID, overrideID int64, req OverrideInput) (Override, error) {
	return Override{}, nil
}

func (s *memoryStore) DeleteOverride(experimentID, overrideID int64) error {
	return nil
}

func (s *memoryStore) ListEvaluationExperiments() ([]Experiment, error) {
	return s.experiments, nil
}

func TestEvaluateReturnsStableClientConfig(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "new_chat_ui",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		EndTime:         now.Add(time.Hour).Format(time.RFC3339),
		Salt:            "stable",
		Version:         3,
		Targeting: map[string]interface{}{
			"platforms":       []interface{}{"ios"},
			"min_app_version": "1.2.0",
		},
		Groups: []Group{{
			ID:                 1,
			GroupKey:           "variant",
			TrafficBasisPoints: 10000,
			Config: map[string]interface{}{
				"enable_new_chat_ui": true,
			},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
		Context: map[string]string{
			"platform":    "ios",
			"app_version": "1.2.1",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(resp.Experiments))
	}
	if resp.Experiments[0].GroupKey != "variant" {
		t.Fatalf("expected variant group, got %q", resp.Experiments[0].GroupKey)
	}
	if resp.MergedConfig["enable_new_chat_ui"] != true {
		t.Fatalf("expected merged config to enable new chat ui, got %#v", resp.MergedConfig)
	}
}

func TestEvaluateSkipsTargetingMismatch(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "ios_only",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		Targeting: map[string]interface{}{
			"platforms": []interface{}{"ios"},
		},
		Groups: []Group{{
			ID:                 1,
			GroupKey:           "variant",
			TrafficBasisPoints: 10000,
			Config:             map[string]interface{}{"flag": true},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
		Context:     map[string]string{"platform": "android"},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 0 {
		t.Fatalf("expected no assignments, got %#v", resp.Experiments)
	}
}

func TestEvaluateForceGroupOverride(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "override_test",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		Groups: []Group{
			{ID: 1, GroupKey: "control", TrafficBasisPoints: 10000, Config: map[string]interface{}{"flag": false}},
			{ID: 2, GroupKey: "variant", TrafficBasisPoints: 0, Config: map[string]interface{}{"flag": true}},
		},
		Overrides: []Override{{
			SubjectType: SubjectTypeDevice,
			SubjectID:   "device-001",
			Action:      OverrideForceGroup,
			GroupKey:    "variant",
			Config:      map[string]interface{}{"debug": true},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected 1 assignment, got %d", len(resp.Experiments))
	}
	assignment := resp.Experiments[0]
	if assignment.GroupKey != "variant" || assignment.Config["flag"] != true || assignment.Config["debug"] != true {
		t.Fatalf("unexpected assignment: %#v", assignment)
	}
	if !assignment.OverrideApplied {
		t.Fatal("expected override to be marked")
	}
}

func TestEvaluateDefaultsZeroTrafficToFirstGroup(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "zero_traffic",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
		Groups: []Group{{
			ID:                 1,
			GroupKey:           "default",
			TrafficBasisPoints: 0,
			Config:             map[string]interface{}{"flag": true},
		}},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "device-001",
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected default assignment, got %#v", resp.Experiments)
	}
	if resp.Experiments[0].GroupKey != "default" || resp.MergedConfig["flag"] != true {
		t.Fatalf("unexpected assignment: %#v", resp.Experiments[0])
	}
}

func TestApplyDefaultGroupTraffic(t *testing.T) {
	groups := applyDefaultGroupTraffic([]GroupInput{
		{GroupKey: "control"},
		{GroupKey: "variant"},
	})

	if groups[0].TrafficBasisPoints+groups[1].TrafficBasisPoints != 10000 {
		t.Fatalf("expected default traffic to add up to 10000, got %#v", groups)
	}
	if groups[0].TrafficBasisPoints != 5000 || groups[1].TrafficBasisPoints != 5000 {
		t.Fatalf("expected default traffic to split evenly, got %#v", groups)
	}
}

func TestApplyDefaultGroupTrafficKeepsExplicitTraffic(t *testing.T) {
	groups := applyDefaultGroupTraffic([]GroupInput{
		{GroupKey: "control", TrafficBasisPoints: 10000},
		{GroupKey: "variant"},
	})

	if groups[0].TrafficBasisPoints != 10000 || groups[1].TrafficBasisPoints != 0 {
		t.Fatalf("expected explicit traffic to be preserved, got %#v", groups)
	}
}

func TestValidateGroupTrafficTotalRejectsOverflow(t *testing.T) {
	err := validateGroupTrafficInputs([]GroupInput{
		{GroupKey: "control", TrafficBasisPoints: 7000},
		{GroupKey: "variant", TrafficBasisPoints: 4000},
	})
	if err == nil {
		t.Fatal("expected overflow traffic to be rejected")
	}
}

func TestRenewedExperimentTimesExtendsFutureEnd(t *testing.T) {
	start := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	end, duration, err := renewedExperimentTimes(Experiment{
		StartTime:       start.Format(time.RFC3339),
		DurationSeconds: int64(time.Hour / time.Second),
		EndTime:         start.Add(time.Hour).Format(time.RFC3339),
	}, int64(30*time.Minute/time.Second), start.Add(10*time.Minute))
	if err != nil {
		t.Fatalf("renewedExperimentTimes() error = %v", err)
	}
	if end != start.Add(90*time.Minute).Format(time.RFC3339) {
		t.Fatalf("expected end to be extended from current end, got %s", end)
	}
	if duration != int64(90*time.Minute/time.Second) {
		t.Fatalf("expected duration 90 minutes, got %d", duration)
	}
}

func TestRenewedExperimentTimesRestartsExpiredEndFromNow(t *testing.T) {
	start := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	now := start.Add(3 * time.Hour)
	end, _, err := renewedExperimentTimes(Experiment{
		StartTime:       start.Format(time.RFC3339),
		DurationSeconds: int64(time.Hour / time.Second),
		EndTime:         start.Add(time.Hour).Format(time.RFC3339),
	}, int64(30*time.Minute/time.Second), now)
	if err != nil {
		t.Fatalf("renewedExperimentTimes() error = %v", err)
	}
	if end != now.Add(30*time.Minute).Format(time.RFC3339) {
		t.Fatalf("expected expired experiment to renew from now, got %s", end)
	}
}

func TestUpdatedExperimentEndTimeTracksDurationChange(t *testing.T) {
	start := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	nextDuration := int64(3 * time.Hour / time.Second)
	end, err := updatedExperimentEndTime(Experiment{
		StartTime:       start.Format(time.RFC3339),
		DurationSeconds: int64(time.Hour / time.Second),
	}, UpdateExperimentRequest{
		DurationSeconds: &nextDuration,
	})
	if err != nil {
		t.Fatalf("updatedExperimentEndTime() error = %v", err)
	}
	if end != start.Add(3*time.Hour).Format(time.RFC3339) {
		t.Fatalf("expected end_time to track duration change, got %s", end)
	}
}

func TestUpdatedExperimentEndTimeTracksStartTimeChange(t *testing.T) {
	start := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	nextStart := start.Add(time.Hour).Format(time.RFC3339)
	end, err := updatedExperimentEndTime(Experiment{
		StartTime:       start.Format(time.RFC3339),
		DurationSeconds: int64(2 * time.Hour / time.Second),
	}, UpdateExperimentRequest{
		StartTime: &nextStart,
	})
	if err != nil {
		t.Fatalf("updatedExperimentEndTime() error = %v", err)
	}
	if end != start.Add(3*time.Hour).Format(time.RFC3339) {
		t.Fatalf("expected end_time to track start_time change, got %s", end)
	}
}
