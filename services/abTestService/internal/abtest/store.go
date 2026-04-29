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
	if err := db.DB().Where("status = ?", StatusRunning).Order("id asc").Find(&experiments).Error; err != nil {
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
	req.Groups = applyDefaultGroupTraffic(req.Groups)
	if err := validateGroupTrafficInputs(req.Groups); err != nil {
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
		updates["status"] = status
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
	if req.EndTime == nil && (req.StartTime != nil || req.DurationSeconds != nil) {
		endTime, err := updatedExperimentEndTime(current, req)
		if err != nil {
			return Experiment{}, err
		}
		updates["end_time"] = endTime
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
	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"status":     status,
		"version":    current.Version + 1,
		"updated_at": NowString(),
	}).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) RenewExperiment(id int64, req RenewExperimentRequest) (Experiment, error) {
	current, err := s.GetExperiment(id)
	if err != nil {
		return Experiment{}, err
	}
	endTime, durationSeconds, err := renewedExperimentTimes(current, req.DurationSeconds, time.Now().UTC())
	if err != nil {
		return Experiment{}, err
	}
	if err := db.DB().Model(&db.ABExperiment{}).Where("id = ?", id).Updates(map[string]interface{}{
		"duration_seconds": durationSeconds,
		"end_time":         endTime,
		"version":          current.Version + 1,
		"updated_at":       NowString(),
	}).Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(id)
}

func (s *GormStore) WithdrawExperiment(id int64) (Experiment, error) {
	return s.SetExperimentStatus(id, StatusStopped)
}

func (s *GormStore) AddGroup(experimentID int64, req GroupInput) (Group, error) {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return Group{}, err
	}
	if err := normalizeGroupInput(&req); err != nil {
		return Group{}, err
	}
	if err := s.validateGroupTrafficTotal(experimentID, 0, req.TrafficBasisPoints); err != nil {
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

func (s *GormStore) UpdateGroup(experimentID, groupID int64, req GroupInput) (Group, error) {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return Group{}, err
	}
	if err := normalizeGroupInput(&req); err != nil {
		return Group{}, err
	}
	if err := s.validateGroupTrafficTotal(experimentID, groupID, req.TrafficBasisPoints); err != nil {
		return Group{}, err
	}
	configJSON, err := mapToJSON(req.Config)
	if err != nil {
		return Group{}, err
	}

	tx := db.DB().Begin()
	if tx.Error != nil {
		return Group{}, tx.Error
	}

	var current db.ABExperimentGroup
	if err := tx.First(&current, "id = ? AND experiment_id = ?", groupID, experimentID).Error; err != nil {
		tx.Rollback()
		return Group{}, err
	}

	now := NowString()
	if err := tx.Model(&db.ABExperimentGroup{}).
		Where("id = ? AND experiment_id = ?", groupID, experimentID).
		Updates(map[string]interface{}{
			"group_key":            req.GroupKey,
			"traffic_basis_points": req.TrafficBasisPoints,
			"config_json":          configJSON,
			"updated_at":           now,
		}).Error; err != nil {
		tx.Rollback()
		return Group{}, err
	}

	if current.GroupKey != req.GroupKey {
		if err := tx.Model(&db.ABExperimentOverride{}).
			Where("experiment_id = ? AND action = ? AND group_key = ?", experimentID, OverrideForceGroup, current.GroupKey).
			Updates(map[string]interface{}{
				"group_key":  req.GroupKey,
				"updated_at": now,
			}).Error; err != nil {
			tx.Rollback()
			return Group{}, err
		}
	}

	if err := tx.Commit().Error; err != nil {
		return Group{}, err
	}
	_, _ = s.bumpExperimentVersion(experimentID)

	var model db.ABExperimentGroup
	if err := db.DB().First(&model, "id = ? AND experiment_id = ?", groupID, experimentID).Error; err != nil {
		return Group{}, err
	}
	return groupFromModel(model), nil
}

func (s *GormStore) DeleteGroup(experimentID, groupID int64) error {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return err
	}

	tx := db.DB().Begin()
	if tx.Error != nil {
		return tx.Error
	}

	var current db.ABExperimentGroup
	if err := tx.First(&current, "id = ? AND experiment_id = ?", groupID, experimentID).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("id = ? AND experiment_id = ?", groupID, experimentID).Delete(&db.ABExperimentGroup{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("experiment_id = ? AND action = ? AND group_key = ?", experimentID, OverrideForceGroup, current.GroupKey).
		Delete(&db.ABExperimentOverride{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}
	_, _ = s.bumpExperimentVersion(experimentID)
	return nil
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
		"version":    current.Version + 1,
		"updated_at": now,
	}).Error; err != nil {
		tx.Rollback()
		return Experiment{}, err
	}
	if err := tx.Commit().Error; err != nil {
		return Experiment{}, err
	}
	return s.GetExperiment(experimentID)
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

func (s *GormStore) UpdateOverride(experimentID, overrideID int64, req OverrideInput) (Override, error) {
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
	result := db.DB().Model(&db.ABExperimentOverride{}).
		Where("id = ? AND experiment_id = ?", overrideID, experimentID).
		Updates(map[string]interface{}{
			"subject_type": req.SubjectType,
			"subject_id":   req.SubjectID,
			"action":       req.Action,
			"group_key":    strings.TrimSpace(req.GroupKey),
			"config_json":  configJSON,
			"updated_at":   NowString(),
		})
	if result.Error != nil {
		return Override{}, result.Error
	}
	if result.RowsAffected == 0 {
		return Override{}, errors.New("override not found")
	}
	_, _ = s.bumpExperimentVersion(experimentID)

	var model db.ABExperimentOverride
	if err := db.DB().First(&model, "id = ? AND experiment_id = ?", overrideID, experimentID).Error; err != nil {
		return Override{}, err
	}
	return overrideFromModel(model), nil
}

func (s *GormStore) DeleteOverride(experimentID, overrideID int64) error {
	if _, err := s.GetExperiment(experimentID); err != nil {
		return err
	}
	result := db.DB().Where("id = ? AND experiment_id = ?", overrideID, experimentID).Delete(&db.ABExperimentOverride{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("override not found")
	}
	_, _ = s.bumpExperimentVersion(experimentID)
	return nil
}

func (s *GormStore) validateGroupTrafficTotal(experimentID, excludingGroupID int64, nextTraffic int) error {
	var total int64
	query := db.DB().Model(&db.ABExperimentGroup{}).Where("experiment_id = ?", experimentID)
	if excludingGroupID > 0 {
		query = query.Where("id <> ?", excludingGroupID)
	}
	if err := query.Select("COALESCE(SUM(traffic_basis_points), 0)").Scan(&total).Error; err != nil {
		return err
	}
	if int(total)+nextTraffic > 10000 {
		return fmt.Errorf("group traffic total exceeds 10000 basis points: current=%d next=%d", total, nextTraffic)
	}
	return nil
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

func validateGroupTrafficInputs(groups []GroupInput) error {
	total := 0
	for i := range groups {
		if err := normalizeGroupInput(&groups[i]); err != nil {
			return err
		}
		total += groups[i].TrafficBasisPoints
	}
	if total > 10000 {
		return fmt.Errorf("group traffic total exceeds 10000 basis points: total=%d", total)
	}
	return nil
}

func renewedExperimentTimes(experiment Experiment, additionalSeconds int64, now time.Time) (string, int64, error) {
	if additionalSeconds <= 0 {
		return "", 0, errors.New("duration_seconds must be positive")
	}
	start, err := time.Parse(time.RFC3339, experiment.StartTime)
	if err != nil {
		return "", 0, fmt.Errorf("invalid start_time: %w", err)
	}

	baseEnd := start.Add(time.Duration(experiment.DurationSeconds) * time.Second)
	if experiment.EndTime != "" {
		if parsedEnd, err := time.Parse(time.RFC3339, experiment.EndTime); err == nil {
			baseEnd = parsedEnd
		}
	}
	if baseEnd.Before(now) {
		baseEnd = now
	}

	nextEnd := baseEnd.Add(time.Duration(additionalSeconds) * time.Second).UTC()
	nextDuration := int64(nextEnd.Sub(start).Seconds())
	if nextDuration <= 0 {
		return "", 0, errors.New("renewed duration must be positive")
	}
	return nextEnd.Format(time.RFC3339), nextDuration, nil
}

func updatedExperimentEndTime(current Experiment, req UpdateExperimentRequest) (string, error) {
	startValue := current.StartTime
	if req.StartTime != nil {
		startValue = strings.TrimSpace(*req.StartTime)
	}
	start, err := time.Parse(time.RFC3339, startValue)
	if err != nil {
		return "", fmt.Errorf("invalid start_time: %w", err)
	}

	durationSeconds := current.DurationSeconds
	if req.DurationSeconds != nil {
		durationSeconds = *req.DurationSeconds
	}
	if durationSeconds <= 0 {
		return "", errors.New("duration_seconds must be positive")
	}

	return start.Add(time.Duration(durationSeconds) * time.Second).UTC().Format(time.RFC3339), nil
}

func applyDefaultGroupTraffic(groups []GroupInput) []GroupInput {
	if len(groups) == 0 {
		return groups
	}
	hasExplicitTraffic := false
	for _, group := range groups {
		if group.TrafficBasisPoints != 0 || group.TrafficRatio > 0 {
			hasExplicitTraffic = true
			break
		}
	}
	if hasExplicitTraffic {
		return groups
	}

	share := 10000 / len(groups)
	remainder := 10000 % len(groups)
	for i := range groups {
		groups[i].TrafficBasisPoints = share
		if i < remainder {
			groups[i].TrafficBasisPoints++
		}
	}
	return groups
}

func normalizeOverrideInput(req *OverrideInput) error {
	req.SubjectType = strings.TrimSpace(req.SubjectType)
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	req.Action = strings.TrimSpace(req.Action)
	req.GroupKey = strings.TrimSpace(req.GroupKey)
	if req.SubjectType == "" || req.SubjectID == "" || req.Action == "" {
		return errors.New("subject_type, subject_id and action are required")
	}
	switch req.Action {
	case OverrideForceGroup:
		if req.GroupKey == "" {
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
	default:
		return ""
	}
}
