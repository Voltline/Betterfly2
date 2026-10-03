package abtest

import (
	"errors"
	"testing"
	"time"
)

func editableExperiment() Experiment {
	return Experiment{ID: 1, ExperimentKey: "editable", ExperimentType: ExperimentTypeClient, Status: StatusRunning,
		StartTime: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), DurationSeconds: 7200, Version: 1,
		Groups:    []Group{{ID: 1, GroupKey: "control", TrafficBasisPoints: 10000, Config: map[string]interface{}{"flag": false}}, {ID: 2, GroupKey: "variant", Config: map[string]interface{}{"flag": true}}},
		Overrides: []Override{{ID: 1, SubjectType: SubjectTypeDevice, SubjectID: "phone", Action: OverrideForceGroup, GroupKey: "variant"}}}
}

func TestGroupMutationsInvalidateEvaluationAndOverrideCaches(t *testing.T) {
	for _, action := range []string{"update", "delete_group", "delete_override"} {
		t.Run(action, func(t *testing.T) {
			store := &memoryStore{experiments: []Experiment{editableExperiment()}}
			service := NewService(store)
			req := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "phone"}
			before, err := service.Evaluate(req)
			if err != nil || before.MergedConfig["flag"] != true {
				t.Fatal(before, err)
			}
			switch action {
			case "update":
				_, err = service.UpdateGroup(1, 2, UpdateGroupRequest{Config: map[string]interface{}{"flag": "changed"}})
			case "delete_group":
				_, err = service.DeleteGroup(1, 2)
			case "delete_override":
				_, err = service.DeleteOverride(1, 1)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := service.Evaluate(req)
			if err != nil || len(after.Experiments) != 1 || after.Experiments[0].Version != 2 {
				t.Fatal(after, err)
			}
			want := interface{}(false)
			if action == "update" {
				want = "changed"
			}
			if after.MergedConfig["flag"] != want {
				t.Fatal("stale config or override after mutation", after)
			}
			if snapshots, overrides := store.counts(); snapshots != 2 || overrides != 2 {
				t.Fatal("both caches must reload after a successful write", snapshots, overrides)
			}
		})
	}
}

func TestFailedGroupMutationDoesNotInvalidateOrChangeConfiguration(t *testing.T) {
	store := &memoryStore{experiments: []Experiment{editableExperiment()}, mutationErr: errors.New("database unavailable")}
	service := NewService(store)
	req := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "phone"}
	if _, err := service.Evaluate(req); err != nil {
		t.Fatal(err)
	}
	before := service.snapshot.Load()
	if _, err := service.UpdateGroup(1, 2, UpdateGroupRequest{Config: map[string]interface{}{}}); err == nil {
		t.Fatal("update ignored database error")
	}
	if _, err := service.DeleteGroup(1, 2); err == nil {
		t.Fatal("delete group ignored database error")
	}
	if _, err := service.DeleteOverride(1, 1); err == nil {
		t.Fatal("delete override ignored database error")
	}
	after, err := service.Evaluate(req)
	if err != nil || after.MergedConfig["flag"] != true || service.snapshot.Load() != before {
		t.Fatal("failed mutation changed configuration", after, err)
	}
}
