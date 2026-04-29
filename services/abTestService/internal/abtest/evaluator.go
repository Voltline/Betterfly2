package abtest

import (
	"errors"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) ListExperiments() ([]Experiment, error) {
	return s.store.ListExperiments()
}

func (s *Service) GetExperiment(id int64) (Experiment, error) {
	return s.store.GetExperiment(id)
}

func (s *Service) CreateExperiment(req CreateExperimentRequest) (Experiment, error) {
	return s.store.CreateExperiment(req)
}

func (s *Service) UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error) {
	return s.store.UpdateExperiment(id, req)
}

func (s *Service) SetExperimentStatus(id int64, status string) (Experiment, error) {
	return s.store.SetExperimentStatus(id, status)
}

func (s *Service) RenewExperiment(id int64, req RenewExperimentRequest) (Experiment, error) {
	return s.store.RenewExperiment(id, req)
}

func (s *Service) WithdrawExperiment(id int64) (Experiment, error) {
	return s.store.WithdrawExperiment(id)
}

func (s *Service) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	return s.store.AddGroup(experimentID, req)
}

func (s *Service) UpdateGroup(experimentID, groupID int64, req GroupInput) (Group, error) {
	return s.store.UpdateGroup(experimentID, groupID, req)
}

func (s *Service) DeleteGroup(experimentID, groupID int64) error {
	return s.store.DeleteGroup(experimentID, groupID)
}

func (s *Service) PushFullGroup(experimentID, groupID int64) (Experiment, error) {
	return s.store.PushFullGroup(experimentID, groupID)
}

func (s *Service) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	return s.store.AddOverride(experimentID, req)
}

func (s *Service) UpdateOverride(experimentID, overrideID int64, req OverrideInput) (Override, error) {
	return s.store.UpdateOverride(experimentID, overrideID, req)
}

func (s *Service) DeleteOverride(experimentID, overrideID int64) error {
	return s.store.DeleteOverride(experimentID, overrideID)
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

	experiments, err := s.store.ListEvaluationExperiments()
	if err != nil {
		return EvaluateResponse{}, err
	}

	now := time.Now().UTC()
	resp := EvaluateResponse{
		ServerTime:   now.Format(time.RFC3339),
		MergedConfig: map[string]interface{}{},
		Experiments:  []Assignment{},
	}

	for _, experiment := range experiments {
		assignment, ok := evaluateExperiment(experiment, req, now)
		if !ok {
			continue
		}
		resp.Experiments = append(resp.Experiments, assignment)
		mergeConfig(resp.MergedConfig, assignment.Config)
	}

	return resp, nil
}

func evaluateExperiment(experiment Experiment, req EvaluateRequest, now time.Time) (Assignment, bool) {
	if experiment.Status != StatusRunning {
		return Assignment{}, false
	}
	if !experimentTypeMatches(experiment.ExperimentType, req.SubjectType) {
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
	if len(groups) > 0 {
		return groups[0], true
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
		target[key] = value
	}
	return target
}

func mergeConfig(target, source map[string]interface{}) {
	for key, value := range source {
		target[key] = value
	}
}
