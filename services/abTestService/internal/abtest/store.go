package abtest

import (
	"Betterfly2/shared/db"
	"errors"
	"fmt"
	"strings"
	"time"
)

type GormStore struct{}

func NewGormStore() *GormStore {
	_ = db.DB(&db.ABExperiment{}, &db.ABExperimentGroup{}, &db.ABExperimentOverride{})
	return &GormStore{}
}

func (s *GormStore) ListExperiments() ([]Experiment, error) {
	var experiments []db.ABExperiment
	if err := db.DB().Order("id desc").Find(&experiments).Error; err != nil {
		return nil, err
	}
	return s.loadExperiments(experiments)
}

func (s *GormStore) ListEvaluationExperiments() ([]Experiment, error) {
	var experiments []db.ABExperiment
	if err := db.DB().Where("status IN ?", []string{StatusRunning, StatusRolledOut}).Order("id asc").Find(&experiments).Error; err != nil {
		return nil, err
	}
	return s.loadExperiments(experiments)
}

func (s *GormStore) GetExperiment(id int64) (Experiment, error) {
	var model db.ABExperiment
	result := db.DB().First(&model, "id = ?", id)
	if result.Error != nil {
		return Experiment{}, result.Error
	}
	experiments, err := s.loadExperiments([]db.ABExperiment{model})
	if err != nil {
		return Experiment{}, err
	}
	if len(experiments) == 0 {
		return Experiment{}, errors.New("experiment not found")
	}
	return experiments[0], nil
}

func (s *GormStore) CreateExperiment(req CreateExperimentRequest) (Experiment, error) {
	if err := normalizeCreateExperiment(&req); err != nil {
		return Experiment{}, err
	}
	targetingJSON, err := mapToJSON(req.Targeting)
	if err != nil {
		return Experiment{}, err
	}
	now := NowString()
	model := db.ABExperiment{
		ExperimentKey:   req.ExperimentKey,
		Name:            req.Name,
		Description:     req.Description,
		ExperimentType:  req.ExperimentType,
		Status:          req.Status,
		StartTime:       req.StartTime,
		DurationSeconds: req.DurationSeconds,
		EndTime:         req.EndTime,
		Salt:            req.Salt,
		TargetingJSON:   targetingJSON,
		Version:         1,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	tx := db.DB().Begin()
	if tx.Error != nil {
		return Experiment{}, tx.Error
	}
	if err := tx.Create(&model).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}
	for _, groupReq := range req.Groups {
		if err := normalizeGroupInput(&groupReq); err != nil {
			tx.Rollback()
			return Experiment{}, err
		}
		configJSON, err := mapToJSON(groupReq.Config)
		if err != nil {
			tx.Rollback()
			return Experiment{}, err
		}
		group := db.ABExperimentGroup{
			ExperimentID:       model.ID,
			GroupKey:           groupReq.GroupKey,
			TrafficBasisPoints: groupReq.TrafficBasisPoints,
			ConfigJSON:         configJSON,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := tx.Create(&group).Error; err != nil {
			tx.Rollback()
			return Experiment{}, err
		}
	}
	if err := tx.Commit().Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(model.ID)
}

func (s *GormStore) UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error) {
	current, err := s.GetExperiment(id)
	if err != nil {
		return Experiment{}, err
	}
	updates := map[string]interface{}{
		"updated_at": NowString(),
		"version":    current.Version + 1,
	}
	if req.Name != nil {
		updates["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.ExperimentType != nil {
		experimentType := normalizeExperimentType(*req.ExperimentType)
		if experimentType == "" {
			return Experiment{}, errors.New("invalid experiment_type")
		}
		updates["experiment_type"] = experimentType
	}
	if req.Status != nil {
		status := normalizeStatus(*req.Status)
		if status == "" {
			return Experiment{}, errors.New("invalid status")
		}
		if status == StatusRolledOut && strings.TrimSpace(current.RolloutGroupKey) == "" {
			return Experiment{}, errors.New("rolled_out status requires push_full group")
		}
		updates["status"] = status
		if status != StatusRolledOut {
			updates["rollout_group_key"] = ""
		}
	}
	if req.StartTime != nil {
		updates["start_time"] = strings.TrimSpace(*req.StartTime)
	}
	if req.DurationSeconds != nil {
		updates["duration_seconds"] = *req.DurationSeconds
	}
	if req.EndTime != nil {
		updates["end_time"] = strings.TrimSpace(*req.EndTime)
	}
	if req.Salt != nil {
		updates["salt"] = strings.TrimSpace(*req.Salt)
	}
	if req.Targeting != nil {
		targetingJSON, err := mapToJSON(req.Targeting)
		if err != nil {
			return Experiment{}, err
		}
		updates["targeting_json"] = targetingJSON
	}

	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) SetExperimentStatus(id int64, status string) (Experiment, error) {
	status = normalizeStatus(status)
	if status == "" {
		return Experiment{}, errors.New("invalid status")
	}
	current, err := s.GetExperiment(id)
	if err != nil {
		return Experiment{}, err
	}
	if status == StatusRolledOut && strings.TrimSpace(current.RolloutGroupKey) == "" {
		return Experiment{}, errors.New("rolled_out status requires push_full group")
	}
	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":            status,
		"rollout_group_key": rolloutGroupKeyForStatus(status, current.RolloutGroupKey),
		"version":           current.Version + 1,
		"updated_at":        NowString(),
	}).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) PushFullGroup(experimentID, groupID int64) (Experiment, error) {
	current, err := s.GetExperiment(experimentID)
	if err != nil {
		return Experiment{}, err
	}

	tx := db.DB().Begin()
	if tx.Error != nil {
		return Experiment{}, tx.Error
	}

	var target db.ABExperimentGroup
	if err := tx.First(&target, "id = ? AND experiment_id = ?", groupID, experimentID).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}

	now := NowString()
	if err := tx.Model(&db.ABExperimentGroup{}).
		Where("experiment_id = ?", experimentID).
		Updates(map[string]interface{}{
			"traffic_basis_points": 0,
			"updated_at":           now,
		}).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}
	if err := tx.Model(&db.ABExperimentGroup{}).
		Where("id = ? AND experiment_id = ?", groupID, experimentID).
		Updates(map[string]interface{}{
			"traffic_basis_points": 10000,
			"updated_at":           now,
		}).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}
	if err := tx.Model(&db.ABExperiment{}).Where("id = ?", experimentID).Updates(map[string]interface{}{
		"status":            StatusRolledOut,
		"rollout_group_key": target.GroupKey,
		"version":           current.Version + 1,
		"updated_at":        now,
	}).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}
	if err := tx.Commit().Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(experimentID)
}

func (s *GormStore) WithdrawExperiment(id int64) (Experiment, error) {
	current, err := s.GetExperiment(id)
	if err != nil {
		return Experiment{}, err
	}
	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":            StatusStopped,
		"rollout_group_key": "",
		"version":           current.Version + 1,
		"updated_at":        NowString(),
	}).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return Group{}, err
	}
	if err := normalizeGroupInput(&req); err != nil {
		return Group{}, err
	}
	configJSON, err := mapToJSON(req.Config)
	if err != nil {
		return Group{}, err
	}
	now := NowString()
	model := db.ABExperimentGroup{
		ExperimentID:       experimentID,
		GroupKey:           req.GroupKey,
		TrafficBasisPoints: req.TrafficBasisPoints,
		ConfigJSON:         configJSON,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := db.DB().Create(&model).Error; err != nil {
		return Group{}, err
	}
	_, _ = s.bumpExperimentVersion(experimentID)
	return groupFromModel(model), nil
}

func (s *GormStore) AddOverride(experimentID int64, req OverrideInput) (Override, error) {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return Override{}, err
	}
	if err := normalizeOverrideInput(&req); err != nil {
		return Override{}, err
	}
	configJSON, err := mapToJSON(req.Config)
	if err != nil {
		return Override{}, err
	}
	now := NowString()
	model := db.ABExperimentOverride{
		ExperimentID: experimentID,
		SubjectType:  req.SubjectType,
		SubjectID:    req.SubjectID,
		Action:       req.Action,
		GroupKey:     req.GroupKey,
		ConfigJSON:   configJSON,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.DB().Create(&model).Error; err != nil {
		return Override{}, err
	}
	_, _ = s.bumpExperimentVersion(experimentID)
	return overrideFromModel(model), nil
}

func (s *GormStore) bumpExperimentVersion(id int64) (Experiment, error) {
	current, err := s.GetExperiment(id)
	if err != nil {
		return Experiment{}, err
	}
	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"version":    current.Version + 1,
		"updated_at": NowString(),
	}).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) loadExperiments(models []db.ABExperiment) ([]Experiment, error) {
	if len(models) == 0 {
		return []Experiment{}, nil
	}

	ids := make([]int64, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}

	var groups []db.ABExperimentGroup
	if err := db.DB().Where("experiment_id IN ?", ids).Order("id asc").Find(&groups).Error; err != nil {
		return nil, err
	}
	var overrides []db.ABExperimentOverride
	if err := db.DB().Where("experiment_id IN ?", ids).Order("id asc").Find(&overrides).Error; err != nil {
		return nil, err
	}

	groupsByExperiment := make(map[int64][]Group)
	for _, group := range groups {
		groupsByExperiment[group.ExperimentID] = append(groupsByExperiment[group.ExperimentID], groupFromModel(group))
	}
	overridesByExperiment := make(map[int64][]Override)
	for _, override := range overrides {
		overridesByExperiment[override.ExperimentID] = append(overridesByExperiment[override.ExperimentID], overrideFromModel(override))
	}

	experiments := make([]Experiment, 0, len(models))
	for _, model := range models {
		experiment := experimentFromModel(model)
		experiment.Groups = groupsByExperiment[model.ID]
		experiment.Overrides = overridesByExperiment[model.ID]
		experiments = append(experiments, experiment)
	}
	return experiments, nil
}

func experimentFromModel(model db.ABExperiment) Experiment {
	return Experiment{
		ID:              model.ID,
		ExperimentKey:   model.ExperimentKey,
		Name:            model.Name,
		Description:     model.Description,
		ExperimentType:  model.ExperimentType,
		Status:          model.Status,
		StartTime:       model.StartTime,
		DurationSeconds: model.DurationSeconds,
		EndTime:         model.EndTime,
		Salt:            model.Salt,
		RolloutGroupKey: model.RolloutGroupKey,
		Targeting:       jsonToMap(model.TargetingJSON),
		Version:         model.Version,
		CreatedAt:       model.CreatedAt,
		UpdatedAt:       model.UpdatedAt,
	}
}

func groupFromModel(model db.ABExperimentGroup) Group {
	return Group{
		ID:                 model.ID,
		ExperimentID:       model.ExperimentID,
		GroupKey:           model.GroupKey,
		TrafficBasisPoints: model.TrafficBasisPoints,
		Config:             jsonToMap(model.ConfigJSON),
		CreatedAt:          model.CreatedAt,
		UpdatedAt:          model.UpdatedAt,
	}
}

func overrideFromModel(model db.ABExperimentOverride) Override {
	return Override{
		ID:           model.ID,
		ExperimentID: model.ExperimentID,
		SubjectType:  model.SubjectType,
		SubjectID:    model.SubjectID,
		Action:       model.Action,
		GroupKey:     model.GroupKey,
		Config:       jsonToMap(model.ConfigJSON),
		CreatedAt:    model.CreatedAt,
		UpdatedAt:    model.UpdatedAt,
	}
}

func normalizeCreateExperiment(req *CreateExperimentRequest) error {
	req.ExperimentKey = strings.TrimSpace(req.ExperimentKey)
	req.Name = strings.TrimSpace(req.Name)
	if req.ExperimentKey == "" || req.Name == "" {
		return errors.New("experiment_key and name are required")
	}
	req.ExperimentType = normalizeExperimentType(req.ExperimentType)
	if req.ExperimentType == "" {
		return errors.New("invalid experiment_type")
	}
	req.Status = normalizeStatus(req.Status)
	if req.Status == "" {
		req.Status = StatusDraft
	}
	if req.Status == StatusRolledOut {
		return errors.New("rolled_out status requires push_full group")
	}
	if req.DurationSeconds <= 0 {
		return errors.New("duration_seconds must be positive")
	}
	if req.StartTime == "" {
		req.StartTime = NowString()
	}
	start, err := time.Parse(time.RFC3339, req.StartTime)
	if err != nil {
		return fmt.Errorf("invalid start_time: %w", err)
	}
	if req.EndTime == "" {
		req.EndTime = start.Add(time.Duration(req.DurationSeconds) * time.Second).UTC().Format(time.RFC3339)
	}
	if req.Salt == "" {
		req.Salt = req.ExperimentKey
	}
	return nil
}

func normalizeGroupInput(req *GroupInput) error {
	req.GroupKey = strings.TrimSpace(req.GroupKey)
	if req.GroupKey == "" {
		return errors.New("group_key is required")
	}
	if req.TrafficBasisPoints == 0 && req.TrafficRatio > 0 {
		req.TrafficBasisPoints = int(req.TrafficRatio * 100)
	}
	if req.TrafficBasisPoints < 0 || req.TrafficBasisPoints > 10000 {
		return errors.New("traffic_basis_points must be between 0 and 10000")
	}
	return nil
}

func normalizeOverrideInput(req *OverrideInput) error {
	req.SubjectType = strings.TrimSpace(req.SubjectType)
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	req.Action = strings.TrimSpace(req.Action)
	if req.SubjectType == "" || req.SubjectID == "" || req.Action == "" {
		return errors.New("subject_type, subject_id and action are required")
	}
	switch req.Action {
	case OverrideForceGroup:
		if strings.TrimSpace(req.GroupKey) == "" {
			return errors.New("group_key is required for force_group override")
		}
	case OverrideExclude, OverrideMergeConfig:
	default:
		return errors.New("invalid override action")
	}
	return nil
}

func normalizeExperimentType(value string) string {
	switch strings.TrimSpace(value) {
	case "", ExperimentTypeClient:
		return ExperimentTypeClient
	case ExperimentTypeServer:
		return ExperimentTypeServer
	case ExperimentTypeAll:
		return ExperimentTypeAll
	default:
		return ""
	}
}

func normalizeStatus(value string) string {
	switch strings.TrimSpace(value) {
	case "", StatusDraft:
		return StatusDraft
	case StatusRunning:
		return StatusRunning
	case StatusPaused:
		return StatusPaused
	case StatusStopped:
		return StatusStopped
	case StatusRolledOut:
		return StatusRolledOut
	default:
		return ""
	}
}

func rolloutGroupKeyForStatus(status, currentGroupKey string) string {
	if status == StatusRolledOut {
		return currentGroupKey
	}
	return ""
}
