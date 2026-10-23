// Package syncengine 对比扫描结果与历史记录，生成增量备份计划。
package syncengine

import (
	"context"
	"errors"
	"fmt"
	"os"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
)

// 计划原因常量。
const (
	ReasonNotBackedUp          = "not_backed_up"
	ReasonChanged              = "changed"
	ReasonIncomplete           = "incomplete"
	ReasonTargetMissing        = "target_missing"
	ReasonTargetUnverified     = "target_unverified"
	ReasonAlreadyBackedUp      = "already_backed_up"
	ReasonDestExistsNoRecord   = "destination_exists_without_record"
	ReasonChangedDestExists    = "changed_and_destination_exists"
	ReasonTargetSizeMismatch   = "target_size_mismatch"
	ReasonDestinationRenderErr = "destination_render_error"
)

// TargetStat 查询目标上正式文件的信息。
type TargetStat interface {
	Stat(rel string) (os.FileInfo, error)
}

// Comparator 单文件决策。
type Comparator struct {
	Files  repository.SourceFiles
	Tasks  repository.Tasks
	Target TargetStat
	// TargetChecked 为 false 时不访问目标。
	TargetChecked bool
}

// Decide 对一个源文件给出动作与原因。dest 为按模板渲染的相对路径。
func (c Comparator) Decide(ctx context.Context, volumeID int64, f domain.SourceFile, dest string) (domain.PlanAction, string, error) {
	var row *repository.FileRow
	if volumeID > 0 {
		var err error
		row, err = c.Files.LatestByPath(ctx, volumeID, f.RelativePath)
		if err != nil {
			return "", "", fmt.Errorf("query source file: %w", err)
		}
	}
	destExists, destSize, err := c.statDest(dest)
	if err != nil {
		return "", "", err
	}
	if row == nil {
		if c.TargetChecked && destExists {
			return domain.PlanConflict, ReasonDestExistsNoRecord, nil
		}
		return domain.PlanUpload, ReasonNotBackedUp, nil
	}
	if row.Fingerprint != f.Fingerprint {
		if c.TargetChecked && destExists {
			return domain.PlanConflict, ReasonChangedDestExists, nil
		}
		return domain.PlanUpload, ReasonChanged, nil
	}
	task, err := c.Tasks.LatestCompleted(ctx, row.ID)
	if err != nil {
		return "", "", fmt.Errorf("query task: %w", err)
	}
	if task == nil {
		if c.TargetChecked && destExists {
			return domain.PlanConflict, ReasonDestExistsNoRecord, nil
		}
		return domain.PlanUpload, ReasonIncomplete, nil
	}
	if !c.TargetChecked {
		return domain.PlanUpload, ReasonTargetUnverified, nil
	}
	// 已完成记录按其保存的实际目标路径核对，模板变更不影响历史
	exists, size, err := c.statDest(task.DestinationPath)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return domain.PlanUpload, ReasonTargetMissing, nil
	}
	if size != f.Size {
		return domain.PlanConflict, ReasonTargetSizeMismatch, nil
	}
	_ = destSize
	return domain.PlanSkip, ReasonAlreadyBackedUp, nil
}

func (c Comparator) statDest(rel string) (bool, int64, error) {
	if !c.TargetChecked || c.Target == nil || rel == "" {
		return false, 0, nil
	}
	st, err := c.Target.Stat(rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("stat destination %s: %w", rel, err)
	}
	return true, st.Size(), nil
}
