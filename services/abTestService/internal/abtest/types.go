package abtest

import "time"

const (
	ExperimentTypeAll    = "all"
	ExperimentTypeClient = "client"
	ExperimentTypeServer = "server"

	StatusDraft   = "draft"
	StatusRunning = "running"
	StatusPaused  = "paused"
	StatusStopped = "stopped"

	SubjectTypeDevice = "device"

	OverrideForceGroup  = "force_group"
	OverrideExclude     = "exclude"
	OverrideMergeConfig = "merge_config"
)

type Experiment struct {
	ID              int64                  `json:"id"`
	ExperimentKey   string                 `json:"experiment_key"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description,omitempty"`
	ExperimentType  string                 `json:"experiment_type"`
	Status          string                 `json:"status"`
	StartTime       string                 `json:"start_time"`
	DurationSeconds int64                  `json:"duration_seconds"`
	EndTime         string                 `json:"end_time"`
	Salt            string                 `json:"salt,omitempty"`
	Targeting       map[string]interface{} `json:"targeting,omitempty"`
	Version         int64                  `json:"version"`
	Groups          []Group                `json:"groups,omitempty"`
	Overrides       []Override             `json:"overrides,omitempty"`
	CreatedAt       string                 `json:"created_at,omitempty"`
	UpdatedAt       string                 `json:"updated_at,omitempty"`
}

type Group struct {
	ID                 int64                  `json:"id"`
	ExperimentID       int64                  `json:"experiment_id"`
	GroupKey           string                 `json:"group_key"`
	TrafficBasisPoints int                    `json:"traffic_basis_points"`
	Config             map[string]interface{} `json:"config,omitempty"`
	CreatedAt          string                 `json:"created_at,omitempty"`
	UpdatedAt          string                 `json:"updated_at,omitempty"`
}

type Override struct {
	ID           int64                  `json:"id"`
	ExperimentID int64                  `json:"experiment_id"`
	SubjectType  string                 `json:"subject_type"`
	SubjectID    string                 `json:"subject_id"`
	Action       string                 `json:"action"`
	GroupKey     string                 `json:"group_key,omitempty"`
	Config       map[string]interface{} `json:"config,omitempty"`
	CreatedAt    string                 `json:"created_at,omitempty"`
	UpdatedAt    string                 `json:"updated_at,omitempty"`
}

type CreateExperimentRequest struct {
	ExperimentKey   string                 `json:"experiment_key"`
	Name            string                 `json:"name"`
	Description     string                 `json:"description,omitempty"`
	ExperimentType  string                 `json:"experiment_type,omitempty"`
	Status          string                 `json:"status,omitempty"`
	StartTime       string                 `json:"start_time"`
	DurationSeconds int64                  `json:"duration_seconds"`
	EndTime         string                 `json:"end_time,omitempty"`
	Salt            string                 `json:"salt,omitempty"`
	Targeting       map[string]interface{} `json:"targeting,omitempty"`
	Groups          []GroupInput           `json:"groups,omitempty"`
}

type UpdateExperimentRequest struct {
	Name            *string                `json:"name,omitempty"`
	Description     *string                `json:"description,omitempty"`
	ExperimentType  *string                `json:"experiment_type,omitempty"`
	Status          *string                `json:"status,omitempty"`
	StartTime       *string                `json:"start_time,omitempty"`
	DurationSeconds *int64                 `json:"duration_seconds,omitempty"`
	EndTime         *string                `json:"end_time,omitempty"`
	Salt            *string                `json:"salt,omitempty"`
	Targeting       map[string]interface{} `json:"targeting,omitempty"`
}

type GroupInput struct {
	GroupKey           string                 `json:"group_key"`
	TrafficBasisPoints int                    `json:"traffic_basis_points,omitempty"`
	TrafficRatio       float64                `json:"traffic_ratio,omitempty"`
	Config             map[string]interface{} `json:"config,omitempty"`
}

type OverrideInput struct {
	SubjectType string                 `json:"subject_type"`
	SubjectID   string                 `json:"subject_id"`
	Action      string                 `json:"action"`
	GroupKey    string                 `json:"group_key,omitempty"`
	Config      map[string]interface{} `json:"config,omitempty"`
}

type EvaluateRequest struct {
	SubjectType string            `json:"subject_type"`
	SubjectID   string            `json:"subject_id"`
	Context     map[string]string `json:"context,omitempty"`
}

type EvaluateResponse struct {
	ServerTime   string                 `json:"server_time"`
	MergedConfig map[string]interface{} `json:"merged_config"`
	Experiments  []Assignment           `json:"experiments"`
}

type Assignment struct {
	ExperimentID    int64                  `json:"experiment_id"`
	ExperimentKey   string                 `json:"experiment_key"`
	ExperimentType  string                 `json:"experiment_type"`
	GroupKey        string                 `json:"group_key"`
	Version         int64                  `json:"version"`
	StartTime       string                 `json:"start_time"`
	EndTime         string                 `json:"end_time"`
	DurationSeconds int64                  `json:"duration_seconds"`
	Config          map[string]interface{} `json:"config"`
	OverrideApplied bool                   `json:"override_applied,omitempty"`
}

type Store interface {
	ListExperiments() ([]Experiment, error)
	GetExperiment(id int64) (Experiment, error)
	CreateExperiment(req CreateExperimentRequest) (Experiment, error)
	UpdateExperiment(id int64, req UpdateExperimentRequest) (Experiment, error)
	SetExperimentStatus(id int64, status string) (Experiment, error)
	AddGroup(experimentID int64, req GroupInput) (Group, error)
	AddOverride(experimentID int64, req OverrideInput) (Override, error)
	ListEvaluationExperiments() ([]Experiment, error)
}

func NowString() string {
	return time.Now().UTC().Format(time.RFC3339)
}
