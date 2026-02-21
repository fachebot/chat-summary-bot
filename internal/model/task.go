package model

import (
	"context"
	"time"

	"github.com/fachebot/talk-trace-bot/internal/ent"
	"github.com/fachebot/talk-trace-bot/internal/ent/task"
)

type TaskModel struct {
	client *ent.TaskClient
}

func NewTaskModel(client *ent.TaskClient) *TaskModel {
	return &TaskModel{client: client}
}

// CreateTask 创建任务
func (m *TaskModel) CreateTask(ctx context.Context, chatID int64, startTime, endTime time.Time, status task.Status) (*ent.Task, error) {
	create := m.client.Create().
		SetChatID(chatID).
		SetStartTime(startTime).
		SetEndTime(endTime).
		SetStatus(status)

	return create.Save(ctx)
}

// GetOrCreateTask 获取或创建任务（如果已存在则返回现有任务）
func (m *TaskModel) GetOrCreateTask(ctx context.Context, chatID int64, startTime, endTime time.Time, status task.Status) (*ent.Task, error) {
	// 先尝试查询现有任务
	existing, err := m.client.Query().
		Where(
			task.ChatIDEQ(chatID),
			task.StartTimeEQ(startTime),
			task.EndTimeEQ(endTime),
		).
		First(ctx)

	if err == nil {
		// 任务已存在，返回现有任务
		return existing, nil
	}

	if !ent.IsNotFound(err) {
		// 查询出错
		return nil, err
	}

	// 任务不存在，创建新任务
	return m.CreateTask(ctx, chatID, startTime, endTime, status)
}

// UpdateTaskStatus 更新任务状态（统一使用 UTC）
func (m *TaskModel) UpdateTaskStatus(ctx context.Context, taskID int, status task.Status, errorMsg *string) error {
	update := m.client.UpdateOneID(taskID).SetStatus(status)
	
	if status == task.StatusCompleted {
		update.SetCompletedAt(time.Now().UTC())
	}
	
	if errorMsg != nil {
		update.SetErrorMessage(*errorMsg)
	}

	return update.Exec(ctx)
}

// GetPendingTasks 查询所有待处理的任务
func (m *TaskModel) GetPendingTasks(ctx context.Context) ([]*ent.Task, error) {
	return m.client.Query().
		Where(task.StatusEQ(task.StatusPending)).
		Order(task.ByCreateTime()).
		All(ctx)
}

// GetProcessingTasks 查询所有处理中的任务
func (m *TaskModel) GetProcessingTasks(ctx context.Context) ([]*ent.Task, error) {
	return m.client.Query().
		Where(task.StatusEQ(task.StatusProcessing)).
		Order(task.ByCreateTime()).
		All(ctx)
}

// GetPendingOrProcessingTasks 查询所有待处理或处理中的任务
func (m *TaskModel) GetPendingOrProcessingTasks(ctx context.Context) ([]*ent.Task, error) {
	return m.client.Query().
		Where(
			task.Or(
				task.StatusEQ(task.StatusPending),
				task.StatusEQ(task.StatusProcessing),
			),
		).
		Order(task.ByCreateTime()).
		All(ctx)
}

// GetTaskByChatAndDateRange 查询指定群组和日期范围的任务
func (m *TaskModel) GetTaskByChatAndDateRange(ctx context.Context, chatID int64, startTime, endTime time.Time) (*ent.Task, error) {
	return m.client.Query().
		Where(
			task.ChatIDEQ(chatID),
			task.StartTimeEQ(startTime),
			task.EndTimeEQ(endTime),
		).
		First(ctx)
}

// MarkTaskCompleted 标记任务完成
func (m *TaskModel) MarkTaskCompleted(ctx context.Context, taskID int) error {
	return m.UpdateTaskStatus(ctx, taskID, task.StatusCompleted, nil)
}

// MarkTaskFailed 标记任务失败
func (m *TaskModel) MarkTaskFailed(ctx context.Context, taskID int, errorMsg string) error {
	return m.UpdateTaskStatus(ctx, taskID, task.StatusFailed, &errorMsg)
}

// ResetTaskToPending 将任务重置为待处理状态（用于恢复）
func (m *TaskModel) ResetTaskToPending(ctx context.Context, taskID int) error {
	return m.client.UpdateOneID(taskID).
		SetStatus(task.StatusPending).
		ClearCompletedAt().
		ClearErrorMessage().
		Exec(ctx)
}

// SetSummaryContent 保存已生成待发送的摘要内容（发送通知前持久化，崩溃恢复时仅重试发送）
func (m *TaskModel) SetSummaryContent(ctx context.Context, taskID int, content string) error {
	return m.client.UpdateOneID(taskID).SetSummaryContent(content).Exec(ctx)
}

// ClearSummaryContent 清除任务的摘要内容（发送成功后调用）
func (m *TaskModel) ClearSummaryContent(ctx context.Context, taskID int) error {
	return m.client.UpdateOneID(taskID).ClearSummaryContent().Exec(ctx)
}
