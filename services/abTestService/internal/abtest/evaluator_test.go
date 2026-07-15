package abtest

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	experiments   []Experiment
	mu            sync.Mutex
	evalLoads     int
	overrideLoads int
	loadDelay     time.Duration
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

func (s *memoryStore) PushFullGroup(experimentID, groupID int64) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) WithdrawExperiment(id int64) (Experiment, error) {
	return Experiment{}, nil
}

func (s *memoryStore) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	return Group{}, nil
}

func (s *memoryStore) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	return Override{}, nil
}

func (s *memoryStore) ListEvaluationExperiments() ([]Experiment, error) {
	s.mu.Lock()
	s.evalLoads++
	delay := s.loadDelay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	return s.experiments, nil
}

func (s *memoryStore) ListOverridesForSubject(subjectType, subjectID string, experimentIDs []int64) ([]Override, error) {
	s.mu.Lock()
	s.overrideLoads++
	s.mu.Unlock()
	active := make(map[int64]struct{}, len(experimentIDs))
	for _, id := range experimentIDs {
		active[id] = struct{}{}
	}
	var result []Override
	for _, experiment := range s.experiments {
		if _, ok := active[experiment.ID]; !ok {
			continue
		}
		for _, override := range experiment.Overrides {
			if override.SubjectType == subjectType && override.SubjectID == subjectID {
				override.ExperimentID = experiment.ID
				result = append(result, override)
			}
		}
	}
	return result, nil
}

func (s *memoryStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.evalLoads, s.overrideLoads
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

func TestEvaluateRolledOutIgnoresTimeTargetingAndTraffic(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "released_feature",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRolledOut,
		StartTime:       now.Add(-48 * time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(time.Hour / time.Second),
		EndTime:         now.Add(-47 * time.Hour).Format(time.RFC3339),
		RolloutGroupKey: "variant",
		Targeting: map[string]interface{}{
			"platforms": []interface{}{"ios"},
		},
		Groups: []Group{
			{ID: 1, GroupKey: "control", TrafficBasisPoints: 10000, Config: map[string]interface{}{"flag": false}},
			{ID: 2, GroupKey: "variant", TrafficBasisPoints: 0, Config: map[string]interface{}{"flag": true}},
		},
	}}})

	resp, err := service.Evaluate(EvaluateRequest{
		SubjectType: SubjectTypeDevice,
		SubjectID:   "android-device-outside-window",
		Context:     map[string]string{"platform": "android"},
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 1 {
		t.Fatalf("expected rolled out assignment, got %#v", resp.Experiments)
	}
	assignment := resp.Experiments[0]
	if assignment.GroupKey != "variant" || assignment.Config["flag"] != true {
		t.Fatalf("expected rolled out variant assignment, got %#v", assignment)
	}
}

func TestEvaluateRunningExpiredStillSkipped(t *testing.T) {
	now := time.Now().UTC()
	service := NewService(&memoryStore{experiments: []Experiment{{
		ID:              1,
		ExperimentKey:   "expired_running_feature",
		ExperimentType:  ExperimentTypeClient,
		Status:          StatusRunning,
		StartTime:       now.Add(-48 * time.Hour).Format(time.RFC3339),
		DurationSeconds: int64(time.Hour / time.Second),
		EndTime:         now.Add(-47 * time.Hour).Format(time.RFC3339),
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
	})
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if len(resp.Experiments) != 0 {
		t.Fatalf("expected expired running experiment to be skipped, got %#v", resp.Experiments)
	}
}

func TestEvaluateCachesImmutableSnapshotAndQueriesOnlySubjectOverrides(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	service := NewServiceWithInvalidation(store, nil, time.Second)
	defer service.Close()
	for _, subjectID := range []string{"device-a", "device-b"} {
		if _, err := service.Evaluate(EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: subjectID}); err != nil {
			t.Fatal(err)
		}
	}
	evaluationLoads, overrideLoads := store.counts()
	if evaluationLoads != 1 || overrideLoads != 2 {
		t.Fatalf("unexpected hot-path queries: snapshot=%d overrides=%d", evaluationLoads, overrideLoads)
	}
}

func TestEvaluateSnapshotReloadUsesSingleflight(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}, loadDelay: 25 * time.Millisecond}
	service := NewServiceWithInvalidation(store, nil, time.Second)
	defer service.Close()
	var workers sync.WaitGroup
	errorsByWorker := make(chan error, 24)
	for index := 0; index < 24; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			_, err := service.Evaluate(EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-singleflight"})
			errorsByWorker <- err
		}(index)
	}
	workers.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatal(err)
		}
	}
	evaluationLoads, overrideLoads := store.counts()
	if evaluationLoads != 1 || overrideLoads != 1 {
		t.Fatalf("cache miss stampede loaded snapshot=%d overrides=%d times", evaluationLoads, overrideLoads)
	}
}

func TestEvaluateNegativeOverrideCacheAvoidsRepeatedDatabaseQuery(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	service := NewServiceWithInvalidation(store, nil, time.Second)
	defer service.Close()
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-without-overrides"}
	for index := 0; index < 3; index++ {
		if _, err := service.Evaluate(request); err != nil {
			t.Fatal(err)
		}
	}
	_, overrideLoads := store.counts()
	if overrideLoads != 1 {
		t.Fatalf("negative override result was not cached: queries=%d", overrideLoads)
	}
}

func TestEvaluateUsesConfiguredMaximumStaleness(t *testing.T) {
	t.Setenv("ABTEST_CACHE_MAX_STALENESS", "20ms")
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	service := NewService(store)
	defer service.Close()
	if service.cacheTTL != 20*time.Millisecond {
		t.Fatalf("ABTEST_CACHE_MAX_STALENESS was ignored: cache_ttl=%s", service.cacheTTL)
	}
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-staleness"}
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	loads, _ := store.counts()
	if loads != 2 {
		t.Fatalf("snapshot remained stale beyond configured maximum: loads=%d", loads)
	}
}

func TestAdminWriteImmediatelyInvalidatesLocalSnapshot(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	service := NewServiceWithInvalidation(store, nil, time.Second)
	defer service.Close()
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-local"}
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddGroup(1, GroupInput{GroupKey: "new", TrafficBasisPoints: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	evaluationLoads, _ := store.counts()
	if evaluationLoads != 2 {
		t.Fatalf("admin write did not invalidate local snapshot: loads=%d", evaluationLoads)
	}
}

type memoryInvalidationBus struct {
	mu          sync.Mutex
	subscribers []func()
}

type lifecycleInvalidationBus struct {
	started chan struct{}
	stopped chan struct{}
}

func (b *lifecycleInvalidationBus) Publish(context.Context) error { return nil }

func (b *lifecycleInvalidationBus) Subscribe(ctx context.Context, _ func()) error {
	close(b.started)
	<-ctx.Done()
	close(b.stopped)
	return nil
}

func TestServiceCloseStopsInvalidationSubscription(t *testing.T) {
	bus := &lifecycleInvalidationBus{started: make(chan struct{}), stopped: make(chan struct{})}
	service := NewServiceWithContext(context.Background(), &memoryStore{}, bus, time.Second)
	select {
	case <-bus.started:
	case <-time.After(time.Second):
		t.Fatal("invalidation subscription did not start")
	}
	service.Close()
	select {
	case <-bus.stopped:
	case <-time.After(time.Second):
		t.Fatal("service close did not cancel invalidation subscription")
	}
}

func (b *memoryInvalidationBus) Publish(context.Context) error {
	b.mu.Lock()
	subscribers := append([]func(){}, b.subscribers...)
	b.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber()
	}
	return nil
}

func (b *memoryInvalidationBus) Subscribe(ctx context.Context, invalidate func()) error {
	b.mu.Lock()
	b.subscribers = append(b.subscribers, invalidate)
	b.mu.Unlock()
	<-ctx.Done()
	return nil
}

func (b *memoryInvalidationBus) subscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

func TestCrossReplicaInvalidationReloadsPeerSnapshot(t *testing.T) {
	bus := &memoryInvalidationBus{}
	writerStore := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	readerStore := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	writer := NewServiceWithInvalidation(writerStore, bus, time.Second)
	reader := NewServiceWithInvalidation(readerStore, bus, time.Second)
	defer writer.Close()
	defer reader.Close()
	deadline := time.Now().Add(time.Second)
	for bus.subscriberCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-peer"}
	if _, err := reader.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.AddGroup(1, GroupInput{GroupKey: "peer-change"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	loads, _ := readerStore.counts()
	if loads != 2 {
		t.Fatalf("peer invalidation did not reload snapshot: loads=%d", loads)
	}
}

func TestSnapshotShortTTLConvergesWithoutNotification(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{activeTestExperiment()}}
	service := NewServiceWithInvalidation(store, nil, 20*time.Millisecond)
	defer service.Close()
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-ttl"}
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := service.Evaluate(request); err != nil {
		t.Fatal(err)
	}
	loads, _ := store.counts()
	if loads != 2 {
		t.Fatalf("short TTL did not converge after missed notification: loads=%d", loads)
	}
}

func TestEvaluateDoesNotExposeSharedSnapshotMaps(t *testing.T) {
	experiment := activeTestExperiment()
	experiment.Groups[0].Config = map[string]interface{}{"nested": map[string]interface{}{"enabled": true}}
	service := NewServiceWithInvalidation(&memoryStore{experiments: []Experiment{experiment}}, nil, time.Second)
	defer service.Close()
	request := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "device-map"}
	first, err := service.Evaluate(request)
	if err != nil {
		t.Fatal(err)
	}
	first.Experiments[0].Config["nested"].(map[string]interface{})["enabled"] = false
	second, err := service.Evaluate(request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Experiments[0].Config["nested"].(map[string]interface{})["enabled"] != true {
		t.Fatal("caller mutation leaked into cached immutable snapshot")
	}
}

func activeTestExperiment() Experiment {
	now := time.Now().UTC()
	return Experiment{
		ID: 1, ExperimentKey: "cached", ExperimentType: ExperimentTypeClient, Status: StatusRunning,
		StartTime: now.Add(-time.Hour).Format(time.RFC3339), DurationSeconds: int64(2 * time.Hour / time.Second),
		Groups: []Group{{ID: 1, ExperimentID: 1, GroupKey: "variant", TrafficBasisPoints: 10000, Config: map[string]interface{}{"enabled": true}}},
		Overrides: []Override{
			{ExperimentID: 1, SubjectType: SubjectTypeDevice, SubjectID: "device-a", Action: OverrideMergeConfig, Config: map[string]interface{}{"device_a": true}},
			{ExperimentID: 1, SubjectType: SubjectTypeDevice, SubjectID: "someone-else", Action: OverrideExclude},
		},
	}
}
