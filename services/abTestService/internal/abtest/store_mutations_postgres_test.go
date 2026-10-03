package abtest

import (
	"Betterfly2/shared/db"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func mutationPostgres(t *testing.T) (*GormStore, *gorm.DB) {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("abtest_mutations_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := base.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
		sql, _ := base.DB()
		sql.Close()
	})
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := database.DB(); sql.Close() })
	if err := database.AutoMigrate(&db.ABExperiment{}, &db.ABExperimentGroup{}, &db.ABExperimentOverride{}); err != nil {
		t.Fatal(err)
	}
	previous := db.DB
	db.DB = func(...interface{}) *gorm.DB { return database }
	t.Cleanup(func() { db.DB = previous })
	return &GormStore{}, database
}

func mutationExperiment(t *testing.T, store *GormStore, key, status string, traffic ...int) Experiment {
	t.Helper()
	weights := []int{10000, 0}
	if len(traffic) > 0 {
		weights = traffic
	}
	experiment, err := store.CreateExperiment(CreateExperimentRequest{ExperimentKey: key, Name: key, Status: status,
		StartTime: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), DurationSeconds: 7200,
		Groups: []GroupInput{{GroupKey: "control", TrafficBasisPoints: weights[0], Config: map[string]interface{}{"flag": false}}, {GroupKey: "variant", TrafficBasisPoints: weights[1], Config: map[string]interface{}{"flag": true}}}})
	if err != nil {
		t.Fatal(err)
	}
	return experiment
}

func TestGroupMutationsPostgresRunningAndPaused(t *testing.T) {
	store, _ := mutationPostgres(t)
	for _, status := range []string{StatusRunning, StatusPaused} {
		t.Run(status, func(t *testing.T) {
			exp := mutationExperiment(t, store, status, status)
			service := NewService(store)
			forced, err := service.AddOverride(exp.ID, OverrideInput{SubjectType: SubjectTypeDevice, SubjectID: "phone", Action: OverrideForceGroup, GroupKey: "variant"})
			if err != nil {
				t.Fatal(err)
			}
			req := EvaluateRequest{SubjectType: SubjectTypeDevice, SubjectID: "phone"}
			if _, err := service.Evaluate(req); err != nil {
				t.Fatal(err)
			}
			changed, err := service.UpdateGroup(exp.ID, exp.Groups[1].ID, UpdateGroupRequest{Config: map[string]interface{}{"flag": "new", "nested": map[string]interface{}{"text": "updated"}}})
			if err != nil || changed.Config["flag"] != "new" || changed.TrafficBasisPoints != 0 {
				t.Fatal("configuration edit failed or changed traffic", changed, err)
			}
			current, err := store.GetExperiment(exp.ID)
			if err != nil || current.Status != status || current.Version != exp.Version+2 {
				t.Fatal("mutation changed experiment lifecycle", current, err)
			}
			if status == StatusPaused {
				if _, err := service.SetExperimentStatus(exp.ID, StatusRunning); err != nil {
					t.Fatal(err)
				}
			}
			result, err := service.Evaluate(req)
			if err != nil || result.MergedConfig["flag"] != "new" {
				t.Fatal("cached configuration was not invalidated", result, err)
			}
			if _, err := service.DeleteOverride(exp.ID, forced.ID); err != nil {
				t.Fatal(err)
			}
			result, err = service.Evaluate(req)
			if err != nil || result.MergedConfig["flag"] != false || result.Experiments[0].OverrideApplied {
				t.Fatal("deleted override remained in cache", result, err)
			}
			for _, input := range []OverrideInput{
				{SubjectType: "device", SubjectID: "forced", Action: OverrideForceGroup, GroupKey: "variant"},
				{SubjectType: "device", SubjectID: "excluded", Action: OverrideExclude},
				{SubjectType: "device", SubjectID: "merged", Action: OverrideMergeConfig, GroupKey: "variant", Config: map[string]interface{}{"extra": true}},
			} {
				if _, err := service.AddOverride(exp.ID, input); err != nil {
					t.Fatal(err)
				}
			}
			if status == StatusPaused {
				if _, err := service.SetExperimentStatus(exp.ID, StatusPaused); err != nil {
					t.Fatal(err)
				}
			}
			remaining, err := service.DeleteGroup(exp.ID, exp.Groups[1].ID)
			if err != nil || len(remaining.Groups) != 1 || len(remaining.Overrides) != 2 || remaining.Status != status || remaining.Groups[0].TrafficBasisPoints != 10000 {
				t.Fatal("group deletion or force override cleanup failed", remaining, err)
			}
			if _, err := service.DeleteGroup(exp.ID, exp.Groups[0].ID); !errors.Is(err, ErrGroupConflict) {
				t.Fatal("last group was deleted", err)
			}
		})
	}
}

func TestGroupMutationsPostgresScopeRolloutAndEmptyConfig(t *testing.T) {
	store, _ := mutationPostgres(t)
	exp := mutationExperiment(t, store, "rollout", StatusRunning)
	other := mutationExperiment(t, store, "other", StatusRunning)
	forced, err := store.AddOverride(exp.ID, OverrideInput{SubjectType: "device", SubjectID: "phone", Action: OverrideForceGroup, GroupKey: "variant"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateGroup(other.ID, exp.Groups[1].ID, UpdateGroupRequest{Config: map[string]interface{}{}}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("cross-experiment update allowed", err)
	}
	if _, err := store.DeleteGroup(other.ID, exp.Groups[1].ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("cross-experiment group deletion allowed", err)
	}
	if _, err := store.DeleteOverride(other.ID, forced.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("cross-experiment override deletion allowed", err)
	}
	if _, err := store.PushFullGroup(exp.ID, exp.Groups[1].ID); err != nil {
		t.Fatal(err)
	}
	group, err := store.UpdateGroup(exp.ID, exp.Groups[1].ID, UpdateGroupRequest{Config: map[string]interface{}{}})
	if err != nil || len(group.Config) != 0 || group.TrafficBasisPoints != 10000 {
		t.Fatal("rollout JSON cannot be cleared", group, err)
	}
	if _, err := store.DeleteGroup(exp.ID, exp.Groups[1].ID); !errors.Is(err, ErrGroupConflict) {
		t.Fatal("current rollout group was deleted", err)
	}
	if remaining, err := store.DeleteGroup(exp.ID, exp.Groups[0].ID); err != nil || len(remaining.Groups) != 1 || remaining.Status != StatusRolledOut || remaining.RolloutGroupKey != "variant" {
		t.Fatal("non-rollout group cannot be removed", remaining, err)
	}
	for _, input := range []UpdateGroupRequest{{}, {TrafficBasisPoints: intPointer(-1)}, {TrafficBasisPoints: intPointer(10001)}, {Config: map[string]interface{}{"bad": func() {}}}} {
		if _, err := store.UpdateGroup(exp.ID, exp.Groups[1].ID, input); !errors.Is(err, ErrInvalidGroupUpdate) {
			t.Fatal("invalid group edit accepted", err)
		}
	}
}

func intPointer(value int) *int { return &value }

func TestGroupMutationsPostgresConcurrency(t *testing.T) {
	store, database := mutationPostgres(t)
	exp := mutationExperiment(t, store, "concurrent", StatusRunning, 4000, 4000)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, group := range exp.Groups {
		wg.Add(1)
		go func(groupID int64) {
			defer wg.Done()
			_, err := store.UpdateGroup(exp.ID, groupID, UpdateGroupRequest{TrafficBasisPoints: intPointer(6000)})
			results <- err
		}(group.ID)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrInvalidGroupUpdate) {
			t.Fatal(err)
		}
	}
	var total int
	if err := database.Model(&db.ABExperimentGroup{}).Where("experiment_id = ?", exp.ID).Select("sum(traffic_basis_points)").Scan(&total).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || total != 10000 {
		t.Fatal("concurrent edits exceeded traffic cap", succeeded, total)
	}
	if _, err := store.AddGroup(exp.ID, GroupInput{GroupKey: "extra", TrafficBasisPoints: 1}); !errors.Is(err, ErrInvalidGroupUpdate) {
		t.Fatal("added group exceeded traffic cap", err)
	}
	if _, err := store.AddGroup(exp.ID, GroupInput{GroupKey: "control"}); err == nil {
		t.Fatal("duplicate group key accepted")
	}
	results = make(chan error, 2)
	for _, group := range exp.Groups {
		wg.Add(1)
		go func(groupID int64) {
			defer wg.Done()
			_, err := store.DeleteGroup(exp.ID, groupID)
			results <- err
		}(group.ID)
	}
	wg.Wait()
	close(results)
	succeeded = 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !errors.Is(err, ErrGroupConflict) {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatal("concurrent deletes removed all groups", succeeded)
	}
}

func TestGroupMutationsPostgresRollbackAndRetry(t *testing.T) {
	store, database := mutationPostgres(t)
	exp := mutationExperiment(t, store, "rollback", StatusRunning)
	forced, err := store.AddOverride(exp.ID, OverrideInput{SubjectType: "device", SubjectID: "phone", Action: OverrideForceGroup, GroupKey: "variant"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetExperiment(exp.ID)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("experiment version update unavailable")
	callback := "test:fail_experiment_version"
	if err := database.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "ab_experiments" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Callback().Update().Remove(callback) })
	if _, err := store.UpdateGroup(exp.ID, exp.Groups[1].ID, UpdateGroupRequest{Config: map[string]interface{}{"flag": "not committed"}}); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if _, err := store.DeleteGroup(exp.ID, exp.Groups[1].ID); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if _, err := store.DeleteOverride(exp.ID, forced.ID); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	after, err := store.GetExperiment(exp.ID)
	if err != nil || len(after.Groups) != 2 || len(after.Overrides) != 1 || after.Version != before.Version || after.Groups[1].Config["flag"] != true {
		t.Fatal("failed transaction left partial edits", after, err)
	}
	database.Callback().Update().Remove(callback)
	if after, err := store.DeleteGroup(exp.ID, exp.Groups[1].ID); err != nil || len(after.Groups) != 1 || len(after.Overrides) != 0 || after.Version != before.Version+1 {
		t.Fatal("retry did not commit group and override deletion together", after, err)
	}
}
