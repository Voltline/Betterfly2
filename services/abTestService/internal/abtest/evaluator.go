package abtest

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"golang.org/x/sync/singleflight"
)

type Service struct {
	store          Store
	cacheTTL       time.Duration
	now            func() time.Time
	snapshot       atomic.Pointer[evaluationSnapshot]
	generation     atomic.Uint64
	reload         singleflight.Group
	overrideReload singleflight.Group
	bus            InvalidationBus
	ctx            context.Context
	cancel         context.CancelFunc
	overrideTTL    time.Duration
	overrideMax    int
	overrideMu     sync.Mutex
	overrides      map[string]overrideCacheEntry
}

type overrideCacheEntry struct {
	overrides []Override
	loadedAt  time.Time
}

func NewService(store Store) *Service {
	return NewServiceWithContext(context.TODO(), store, nil, evaluationCacheTTL())
}

func NewServiceWithInvalidation(store Store, bus InvalidationBus, cacheTTL time.Duration) *Service {
	return NewServiceWithContext(context.TODO(), store, bus, cacheTTL)
}

func NewServiceWithContext(parent context.Context, store Store, bus InvalidationBus, cacheTTL time.Duration) *Service {
	if parent == nil {
		parent = context.TODO()
	}
	if cacheTTL <= 0 {
		cacheTTL = evaluationCacheTTL()
	}
	if cacheTTL > 5*time.Second {
		cacheTTL = 5 * time.Second
	}
	ctx, cancel := context.WithCancel(parent)
	service := &Service{
		store: store, cacheTTL: cacheTTL, now: time.Now, bus: bus, ctx: ctx, cancel: cancel,
		overrideTTL: overrideCacheTTL(cacheTTL), overrideMax: overrideCacheMaxEntries(), overrides: make(map[string]overrideCacheEntry),
	}
	if bus != nil {
		go func() {
			if err := bus.Subscribe(ctx, service.invalidateLocal); err != nil && ctx.Err() == nil {
				logger.Sugar().Warnw("AB Test跨副本缓存失效订阅退出", "error", err)
			}
		}()
	}
	return service
}

func (s *Service) ListExperiments() ([]Experiment, error) {
	return s.store.ListExperiments()
}

func (s *Service) GetExperiment(id int64) (Experiment, error) {
	return s.store.GetExperiment(id)
}

func (s *Service) CreateExperiment(req CreateExperimentRequest) (Experiment, error) {
	value, err := s.store.CreateExperiment(req)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error) {
	value, err := s.store.UpdateExperiment(id, req)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) SetExperimentStatus(id int64, status string) (Experiment, error) {
	value, err := s.store.SetExperimentStatus(id, status)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) PushFullGroup(experimentID, groupID int64) (Experiment, error) {
	value, err := s.store.PushFullGroup(experimentID, groupID)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) WithdrawExperiment(id int64) (Experiment, error) {
	value, err := s.store.WithdrawExperiment(id)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	value, err := s.store.AddGroup(experimentID, req)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	value, err := s.store.AddOverride(experimentID, req)
	s.invalidateAfterWrite(err)
	return value, err
}

func (s *Service) Close() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Service) invalidateLocal() {
	s.generation.Add(1)
	s.snapshot.Store(nil)
	s.overrideMu.Lock()
	s.overrides = make(map[string]overrideCacheEntry)
	s.overrideMu.Unlock()
}

func (s *Service) invalidateAfterWrite(err error) {
	if err != nil {
		return
	}
	s.invalidateLocal()
	if s.bus == nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, time.Second)
	defer cancel()
	if publishErr := s.bus.Publish(ctx); publishErr != nil {
		logger.Sugar().Warnw("发布AB Test缓存失效通知失败，将依赖短TTL收敛", "error", publishErr)
	}
}

func (s *Service) Evaluate(req EvaluateRequest) (EvaluateResponse, error) {
	req.SubjectType = strings.TrimSpace(req.SubjectType)
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	if req.SubjectType == "" || req.SubjectID == "" {
		return EvaluateResponse{}, errors.New("subject_type and subject_id are required")
	}
	if req.Context == nil {
		req.Context = map[string]string{}
	}

	snapshot, err := s.evaluationSnapshot()
	if err != nil {
		return EvaluateResponse{}, err
	}
	overrides, err := s.subjectOverrides(req.SubjectType, req.SubjectID, snapshot)
	if err != nil {
		return EvaluateResponse{}, err
	}
	overridesByExperiment := make(map[int64][]Override, len(overrides))
	for _, override := range overrides {
		overridesByExperiment[override.ExperimentID] = append(overridesByExperiment[override.ExperimentID], cloneOverride(override))
	}

	now := s.now().UTC()
	resp := EvaluateResponse{
		ServerTime:   now.Format(time.RFC3339),
		MergedConfig: map[string]interface{}{},
		Experiments:  []Assignment{},
	}

	for _, immutableExperiment := range snapshot.experiments {
		experiment := cloneExperiment(immutableExperiment)
		experiment.Overrides = overridesByExperiment[experiment.ID]
		assignment, ok := evaluateExperiment(experiment, req, now)
		if !ok {
			continue
		}
		resp.Experiments = append(resp.Experiments, assignment)
		mergeConfig(resp.MergedConfig, assignment.Config)
	}

	return resp, nil
}

func (s *Service) subjectOverrides(subjectType, subjectID string, snapshot *evaluationSnapshot) ([]Override, error) {
	key := fmt.Sprintf("%d:%s:%s", snapshot.generation, subjectType, subjectID)
	now := s.now()
	if overrides, ok := s.cachedSubjectOverrides(key, now); ok {
		metrics.RecordABTestCache("override_hit")
		return overrides, nil
	}
	metrics.RecordABTestCache("override_miss")

	value, err, _ := s.overrideReload.Do(key, func() (any, error) {
		if overrides, ok := s.cachedSubjectOverrides(key, s.now()); ok {
			return overrides, nil
		}
		started := time.Now()
		overrides, loadErr := s.store.ListOverridesForSubject(subjectType, subjectID, snapshot.experimentIDs)
		metrics.RecordABTestDatabaseQuery("subject_overrides", started)
		if loadErr != nil {
			return nil, loadErr
		}
		immutable := cloneOverrides(overrides)
		s.overrideMu.Lock()
		s.evictOverrideEntryLocked(now)
		s.overrides[key] = overrideCacheEntry{overrides: immutable, loadedAt: s.now()}
		s.overrideMu.Unlock()
		return immutable, nil
	})
	if err != nil {
		return nil, err
	}
	return cloneOverrides(value.([]Override)), nil
}

func (s *Service) cachedSubjectOverrides(key string, now time.Time) ([]Override, bool) {
	s.overrideMu.Lock()
	defer s.overrideMu.Unlock()
	cached, ok := s.overrides[key]
	if !ok || now.Sub(cached.loadedAt) >= s.overrideTTL {
		return nil, false
	}
	return cloneOverrides(cached.overrides), true
}

func (s *Service) evictOverrideEntryLocked(now time.Time) {
	if len(s.overrides) < s.overrideMax {
		return
	}
	var oldestKey string
	var oldest time.Time
	for key, entry := range s.overrides {
		if now.Sub(entry.loadedAt) >= s.overrideTTL {
			delete(s.overrides, key)
			if len(s.overrides) < s.overrideMax {
				return
			}
			continue
		}
		if oldestKey == "" || entry.loadedAt.Before(oldest) {
			oldestKey, oldest = key, entry.loadedAt
		}
	}
	if oldestKey != "" {
		delete(s.overrides, oldestKey)
	}
}

func cloneOverrides(values []Override) []Override {
	result := make([]Override, 0, len(values))
	for _, value := range values {
		result = append(result, cloneOverride(value))
	}
	return result
}

type evaluationSnapshot struct {
	experiments   []Experiment
	experimentIDs []int64
	loadedAt      time.Time
	generation    uint64
}

func (s *Service) evaluationSnapshot() (*evaluationSnapshot, error) {
	for {
		now := s.now()
		generation := s.generation.Load()
		if current := s.snapshot.Load(); current != nil && current.generation == generation && now.Sub(current.loadedAt) < s.cacheTTL {
			metrics.RecordABTestCache("hit")
			metrics.SetABTestSnapshotAge(now.Sub(current.loadedAt))
			return current, nil
		}
		metrics.RecordABTestCache("miss")
		value, err, _ := s.reload.Do(fmt.Sprintf("snapshot-%d", generation), func() (any, error) {
			started := time.Now()
			experiments, loadErr := s.store.ListEvaluationExperiments()
			metrics.RecordABTestDatabaseQuery("snapshot", started)
			if loadErr != nil {
				return nil, loadErr
			}
			immutable := make([]Experiment, 0, len(experiments))
			ids := make([]int64, 0, len(experiments))
			for _, experiment := range experiments {
				experiment.Overrides = nil
				immutable = append(immutable, cloneExperiment(experiment))
				ids = append(ids, experiment.ID)
			}
			metrics.RecordABTestCacheReload()
			return &evaluationSnapshot{experiments: immutable, experimentIDs: ids, loadedAt: s.now(), generation: generation}, nil
		})
		if err != nil {
			return nil, err
		}
		if generation != s.generation.Load() {
			continue
		}
		loaded := value.(*evaluationSnapshot)
		s.snapshot.Store(loaded)
		metrics.SetABTestSnapshotAge(0)
		return loaded, nil
	}
}

func evaluationCacheTTL() time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv("ABTEST_CACHE_MAX_STALENESS")))
	if err != nil || value <= 0 || value > 5*time.Second {
		return 5 * time.Second
	}
	return value
}

func overrideCacheTTL(maxStaleness time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv("ABTEST_OVERRIDE_CACHE_TTL")))
	if err != nil || value <= 0 {
		value = 3 * time.Second
	}
	if value > maxStaleness {
		value = maxStaleness
	}
	return value
}

func overrideCacheMaxEntries() int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ABTEST_OVERRIDE_CACHE_MAX_ENTRIES")))
	if err != nil || value <= 0 {
		return 10000
	}
	if value > 100000 {
		return 100000
	}
	return value
}

func evaluateExperiment(experiment Experiment, req EvaluateRequest, now time.Time) (Assignment, bool) {
	if !experimentTypeMatches(experiment.ExperimentType, req.SubjectType) {
		return Assignment{}, false
	}
	if experiment.Status == StatusRolledOut {
		group, ok := rolloutGroup(experiment)
		if !ok {
			return Assignment{}, false
		}
		return buildAssignment(experiment, group, false), true
	}
	if experiment.Status != StatusRunning {
		return Assignment{}, false
	}
	if !experimentWindowMatches(experiment, now) {
		return Assignment{}, false
	}
	if !targetingMatches(experiment.Targeting, req.Context) {
		return Assignment{}, false
	}

	override := findOverride(experiment.Overrides, req.SubjectType, req.SubjectID)
	if override != nil && override.Action == OverrideExclude {
		return Assignment{}, false
	}

	group, ok := selectGroup(experiment, req, override)
	if !ok {
		if override != nil && override.Action == OverrideMergeConfig {
			return buildAssignment(experiment, Group{GroupKey: "override", Config: override.Config}, true), true
		}
		return Assignment{}, false
	}

	config := cloneMap(group.Config)
	overrideApplied := false
	if override != nil {
		switch override.Action {
		case OverrideForceGroup:
			overrideApplied = true
			mergeConfig(config, override.Config)
		case OverrideMergeConfig:
			overrideApplied = true
			mergeConfig(config, override.Config)
		}
	}

	group.Config = config
	return buildAssignment(experiment, group, overrideApplied), true
}

func buildAssignment(experiment Experiment, group Group, overrideApplied bool) Assignment {
	return Assignment{
		ExperimentID:    experiment.ID,
		ExperimentKey:   experiment.ExperimentKey,
		ExperimentType:  experiment.ExperimentType,
		GroupKey:        group.GroupKey,
		Version:         experiment.Version,
		StartTime:       experiment.StartTime,
		EndTime:         experiment.EndTime,
		DurationSeconds: experiment.DurationSeconds,
		Config:          group.Config,
		OverrideApplied: overrideApplied,
	}
}

func experimentTypeMatches(experimentType, subjectType string) bool {
	if experimentType == "" || experimentType == ExperimentTypeAll {
		return true
	}
	if subjectType == SubjectTypeDevice {
		return experimentType == ExperimentTypeClient
	}
	return experimentType == ExperimentTypeServer
}

func experimentWindowMatches(experiment Experiment, now time.Time) bool {
	start, err := time.Parse(time.RFC3339, experiment.StartTime)
	if err != nil {
		return false
	}
	end := start.Add(time.Duration(experiment.DurationSeconds) * time.Second)
	if experiment.EndTime != "" {
		parsedEnd, err := time.Parse(time.RFC3339, experiment.EndTime)
		if err == nil {
			end = parsedEnd
		}
	}
	return !now.Before(start) && now.Before(end)
}

func findOverride(overrides []Override, subjectType, subjectID string) *Override {
	for i := range overrides {
		override := &overrides[i]
		if override.SubjectType == subjectType && override.SubjectID == subjectID {
			return override
		}
	}
	return nil
}

func selectGroup(experiment Experiment, req EvaluateRequest, override *Override) (Group, bool) {
	groups := append([]Group(nil), experiment.Groups...)
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].ID < groups[j].ID
	})

	if override != nil && override.Action == OverrideForceGroup {
		for _, group := range groups {
			if group.GroupKey == override.GroupKey {
				return group, true
			}
		}
		return Group{}, false
	}

	bucket := stableBucket(req.SubjectID, experiment.ExperimentKey, experiment.Salt)
	upper := 0
	for _, group := range groups {
		upper += group.TrafficBasisPoints
		if bucket < upper {
			return group, true
		}
	}
	return Group{}, false
}

func rolloutGroup(experiment Experiment) (Group, bool) {
	key := strings.TrimSpace(experiment.RolloutGroupKey)
	if key == "" {
		return Group{}, false
	}
	for _, group := range experiment.Groups {
		if group.GroupKey == key {
			return group, true
		}
	}
	return Group{}, false
}

func stableBucket(subjectID, experimentKey, salt string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(subjectID))
	_, _ = hash.Write([]byte(":"))
	_, _ = hash.Write([]byte(experimentKey))
	_, _ = hash.Write([]byte(":"))
	_, _ = hash.Write([]byte(salt))
	return int(hash.Sum32() % 10000)
}

func targetingMatches(targeting map[string]interface{}, context map[string]string) bool {
	if len(targeting) == 0 {
		return true
	}

	if !stringSetMatches(targeting, context, "platform", "platforms") {
		return false
	}
	if !stringSetMatchesAny(targeting, context, "os", "os", "oses", "operating_systems") {
		return false
	}
	if !stringSetMatches(targeting, context, "app_version", "app_versions") {
		return false
	}
	if !stringSetMatches(targeting, context, "system_version", "system_versions") {
		return false
	}
	if !versionRangeMatches(context["app_version"], stringFromTargeting(targeting, "min_app_version"), stringFromTargeting(targeting, "max_app_version")) {
		return false
	}
	if !versionRangeMatches(context["system_version"], stringFromTargeting(targeting, "min_system_version"), stringFromTargeting(targeting, "max_system_version")) {
		return false
	}
	if !genericIncludeExcludeMatches(targeting, context) {
		return false
	}

	return true
}

func stringSetMatches(targeting map[string]interface{}, context map[string]string, contextKey, targetingKey string) bool {
	allowed := stringSliceFromTargeting(targeting, targetingKey)
	if len(allowed) == 0 {
		return true
	}
	value := strings.TrimSpace(context[contextKey])
	for _, item := range allowed {
		if item == value {
			return true
		}
	}
	return false
}

func stringSetMatchesAny(targeting map[string]interface{}, context map[string]string, contextKey string, targetingKeys ...string) bool {
	for _, targetingKey := range targetingKeys {
		if _, ok := targeting[targetingKey]; !ok {
			continue
		}
		return stringSetMatches(targeting, context, contextKey, targetingKey)
	}
	return true
}

func versionRangeMatches(version, minVersion, maxVersion string) bool {
	version = strings.TrimSpace(version)
	if version == "" && (minVersion != "" || maxVersion != "") {
		return false
	}
	if minVersion != "" && compareVersion(version, minVersion) < 0 {
		return false
	}
	if maxVersion != "" && compareVersion(version, maxVersion) > 0 {
		return false
	}
	return true
}

func genericIncludeExcludeMatches(targeting map[string]interface{}, context map[string]string) bool {
	if include, ok := stringRuleMap(targeting["include"]); ok {
		for key, rawAllowed := range include {
			if len(rawAllowed) > 0 && !containsString(rawAllowed, context[key]) {
				return false
			}
		}
	}
	if exclude, ok := stringRuleMap(targeting["exclude"]); ok {
		for key, rawDenied := range exclude {
			if containsString(rawDenied, context[key]) {
				return false
			}
		}
	}
	return true
}

func stringRuleMap(raw interface{}) (map[string][]string, bool) {
	switch value := raw.(type) {
	case map[string][]string:
		return value, true
	case map[string]interface{}:
		result := make(map[string][]string, len(value))
		for key, rawValues := range value {
			result[key] = stringSlice(rawValues)
		}
		return result, true
	default:
		return nil, false
	}
}

func stringFromTargeting(targeting map[string]interface{}, key string) string {
	value, _ := targeting[key].(string)
	return strings.TrimSpace(value)
}

func stringSliceFromTargeting(targeting map[string]interface{}, key string) []string {
	return stringSlice(targeting[key])
}

func stringSlice(raw interface{}) []string {
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return []string{strings.TrimSpace(value)}
	case []string:
		return value
	case []interface{}:
		items := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok {
				items = append(items, strings.TrimSpace(s))
			}
		}
		return items
	default:
		return nil
	}
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func compareVersion(a, b string) int {
	as := splitVersion(a)
	bs := splitVersion(b)
	maxLen := len(as)
	if len(bs) > maxLen {
		maxLen = len(bs)
	}
	for i := 0; i < maxLen; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av = as[i]
		}
		if i < len(bs) {
			bv = bs[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}
	return 0
}

func splitVersion(version string) []int {
	parts := strings.FieldsFunc(version, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		value, _ := strconv.Atoi(part)
		values = append(values, value)
	}
	return values
}

func cloneMap(source map[string]interface{}) map[string]interface{} {
	target := make(map[string]interface{}, len(source))
	for key, value := range source {
		target[key] = cloneValue(value)
	}
	return target
}

func cloneValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneMap(typed)
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index := range typed {
			result[index] = cloneValue(typed[index])
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func cloneExperiment(source Experiment) Experiment {
	result := source
	result.Targeting = cloneMap(source.Targeting)
	result.Groups = make([]Group, len(source.Groups))
	for index, group := range source.Groups {
		result.Groups[index] = group
		result.Groups[index].Config = cloneMap(group.Config)
	}
	result.Overrides = make([]Override, len(source.Overrides))
	for index, override := range source.Overrides {
		result.Overrides[index] = cloneOverride(override)
	}
	return result
}

func cloneOverride(source Override) Override {
	result := source
	result.Config = cloneMap(source.Config)
	return result
}

func mergeConfig(target, source map[string]interface{}) {
	for key, value := range source {
		target[key] = value
	}
}
