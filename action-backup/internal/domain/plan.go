package domain

import "time"

// PlanAction 计划条目动作。
type PlanAction string

const (
	PlanUpload   PlanAction = "upload"
	PlanSkip     PlanAction = "skip"
	PlanConflict PlanAction = "conflict"
)

// PlanItem 计划中的一个文件。
type PlanItem struct {
	VolumeType      VolumeType `json:"volume"`
	RelativePath    string     `json:"path"`
	Size            int64      `json:"size"`
	Action          PlanAction `json:"action"`
	Reason          string     `json:"reason"`
	DestinationPath string     `json:"destination,omitempty"`

	// 内部字段，不输出
	File     SourceFile `json:"-"`
	VolumeID int64      `json:"-"`
}

// PlanSummary 某一类动作的汇总。
type PlanSummary struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Plan 一次扫描后的备份计划，dry-run 与 API 预览共用。
type Plan struct {
	GeneratedAt time.Time `json:"generated_at"`
	Mode        string    `json:"mode"`
	Devices     int       `json:"devices"`
	Volumes     int       `json:"volumes"`
	Scanned     int       `json:"scanned_files"`
	// TargetChecked 为 false 表示目标存储不可用，跳过判断未经目标核对，所有已完成文件按上传处理。
	TargetChecked bool        `json:"target_checked"`
	Upload        PlanSummary `json:"upload"`
	Skip          PlanSummary `json:"skip"`
	Conflict      PlanSummary `json:"conflict"`
	Items         []PlanItem  `json:"items"`
	Warnings      []string    `json:"warnings"`
	DurationMs    int64       `json:"duration_ms"`
}

// Add 把条目计入汇总。
func (p *Plan) Add(it PlanItem) {
	p.Items = append(p.Items, it)
	switch it.Action {
	case PlanUpload:
		p.Upload.Files++
		p.Upload.Bytes += it.Size
	case PlanSkip:
		p.Skip.Files++
		p.Skip.Bytes += it.Size
	case PlanConflict:
		p.Conflict.Files++
		p.Conflict.Bytes += it.Size
	}
}
