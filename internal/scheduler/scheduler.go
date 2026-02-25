package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fachebot/talk-trace-bot/internal/config"
	"github.com/fachebot/talk-trace-bot/internal/ent"
	"github.com/fachebot/talk-trace-bot/internal/ent/dailyrun"
	"github.com/fachebot/talk-trace-bot/internal/ent/task"
	"github.com/fachebot/talk-trace-bot/internal/logger"
	"github.com/fachebot/talk-trace-bot/internal/model"
	"github.com/fachebot/talk-trace-bot/internal/notify"
	"github.com/fachebot/talk-trace-bot/internal/summarizer"
	"github.com/robfig/cron/v3"
	"github.com/zelenin/go-tdlib/client"
)

type Scheduler struct {
	cron          *cron.Cron
	summarizer    *summarizer.Summarizer
	notifier      *notify.Notifier
	tdClient      *client.Client
	messageModel  *model.MessageModel
	taskModel     *model.TaskModel
	dailyRunModel *model.DailyRunModel
	config        *config.Summary
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// locUTC UTC 标准时间（UTC）
var locUTC = time.UTC

func NewScheduler(
	summarizer *summarizer.Summarizer,
	notifier *notify.Notifier,
	tdClient *client.Client,
	messageModel *model.MessageModel,
	taskModel *model.TaskModel,
	dailyRunModel *model.DailyRunModel,
	cfg *config.Summary,
) *Scheduler {
	return &Scheduler{
		cron:          cron.New(cron.WithLocation(locUTC)),
		summarizer:    summarizer,
		notifier:      notifier,
		tdClient:      tdClient,
		messageModel:  messageModel,
		taskModel:     taskModel,
		dailyRunModel: dailyRunModel,
		config:        cfg,
	}
}

// Start 启动调度器
func (s *Scheduler) Start() error {
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.mu.Unlock()

	// 注册每日总结任务
	_, err := s.cron.AddFunc(s.config.Cron, s.runDailySummary)
	if err != nil {
		return fmt.Errorf("注册每日总结任务失败: %w", err)
	}

	s.cron.Start()
	logger.Infof("[Scheduler] 调度器已启动，每日总结任务: %s", s.config.Cron)

	// 启动时恢复未完成的任务
	go s.recoverDailySummary()

	return nil
}

// Stop 停止调度器
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Unlock()

	ctx := s.cron.Stop()
	<-ctx.Done()
	logger.Infof("[Scheduler] 调度器已停止")
}

// recoverDailySummary 恢复每日总结（未完成的 DailyRun、缺失的当日、未完成的 Task）
func (s *Scheduler) recoverDailySummary() {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()

	logger.Infof("[Scheduler] 开始恢复每日总结")

	// 1. 恢复未完成的 DailyRun
	incompleteRuns, err := s.dailyRunModel.GetIncompleteRuns(ctx)
	if err != nil {
		logger.Errorf("[Scheduler] 查询未完成 DailyRun 失败: %v", err)
	} else {
		for _, run := range incompleteRuns {
			select {
			case <-ctx.Done():
				logger.Infof("[Scheduler] 恢复已取消")
				return
			default:
			}
			logger.Infof("[Scheduler] 恢复未完成 DailyRun: startTime=%s, endTime=%s", run.StartTime.Format("2006-01-02"), run.EndTime.Format("2006-01-02"))
			if err := s.executeDailySummaryForRange(ctx, run.StartTime, run.EndTime); err != nil {
				logger.Errorf("[Scheduler] 恢复 DailyRun 失败: %v", err)
				_ = s.dailyRunModel.MarkFailed(ctx, run.ID, err.Error())
			} else {
				_ = s.dailyRunModel.MarkCompleted(ctx, run.ID)
			}
		}
	}

	// 2. 检查缺失的当日：若当日区间无 DailyRun 记录，视为漏跑并执行
	rangeDays := s.config.RangeDays
	if rangeDays <= 0 {
		rangeDays = 1
	}
	now := time.Now().In(locUTC)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, locUTC)
	endTime := todayStart
	startTime := todayStart.AddDate(0, 0, -rangeDays)

	_, err = s.dailyRunModel.GetByDateRange(ctx, startTime, endTime)
	if err != nil && ent.IsNotFound(err) {
		logger.Infof("[Scheduler] 当日无 DailyRun 记录，补跑: %s ~ %s", startTime.Format("2006-01-02"), endTime.Format("2006-01-02"))
		run, createErr := s.dailyRunModel.Create(ctx, startTime, endTime, dailyrun.StatusInProgress)
		if createErr != nil {
			logger.Errorf("[Scheduler] 创建 DailyRun 失败: %v", createErr)
		} else {
			if execErr := s.executeDailySummaryForRange(ctx, startTime, endTime); execErr != nil {
				logger.Errorf("[Scheduler] 补跑 DailyRun 失败: %v", execErr)
				_ = s.dailyRunModel.MarkFailed(ctx, run.ID, execErr.Error())
			} else {
				_ = s.dailyRunModel.MarkCompleted(ctx, run.ID)
			}
		}
	}

	// 3. 恢复未完成的 Task
	s.recoverPendingTasks(ctx)

	logger.Infof("[Scheduler] 每日总结恢复完成")
}

// recoverPendingTasks 恢复未完成的 Task
func (s *Scheduler) recoverPendingTasks(ctx context.Context) {
	tasks, err := s.taskModel.GetPendingOrProcessingTasks(ctx)
	if err != nil {
		logger.Errorf("[Scheduler] 查询未完成任务失败: %v", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	logger.Infof("[Scheduler] 找到 %d 个未完成的任务，开始恢复", len(tasks))
	cutoffTime := time.Now().In(locUTC).AddDate(0, 0, -7)

	for _, t := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if t.StartTime.Before(cutoffTime) {
			logger.Warnf("[Scheduler] 跳过过期任务: chatID=%d, startTime=%s", t.ChatID, t.StartTime.Format("2006-01-02"))
			continue
		}
		if err := s.taskModel.ResetTaskToPending(ctx, t.ID); err != nil {
			logger.Errorf("[Scheduler] 重置任务状态失败 (taskID=%d): %v", t.ID, err)
			continue
		}
		if err := s.taskModel.UpdateTaskStatus(ctx, t.ID, task.StatusProcessing, nil); err != nil {
			logger.Errorf("[Scheduler] 更新任务状态失败 (taskID=%d): %v", t.ID, err)
			continue
		}
		// 若已有待发送摘要（程序曾在发送阶段退出），只重试发送通知
		if t.SummaryContent != "" {
			logger.Infof("[Scheduler] 恢复任务仅重试发送通知: chatID=%d, taskID=%d", t.ChatID, t.ID)
			sent, sendErr := s.sendTaskNotification(ctx, t.SummaryContent, t.ChatID)
			if sendErr != nil {
				logger.Errorf("[Scheduler] 恢复发送通知失败 (chatID=%d): %v", t.ChatID, sendErr)
				_ = s.taskModel.MarkTaskFailed(ctx, t.ID, sendErr.Error())
				continue
			}
			if sent {
				_ = s.taskModel.ClearSummaryContent(ctx, t.ID)
			}
			_ = s.taskModel.MarkTaskCompleted(ctx, t.ID)
			continue
		}
		logger.Infof("[Scheduler] 恢复处理任务: chatID=%d, startTime=%s, endTime=%s", t.ChatID, t.StartTime.Format("2006-01-02"), t.EndTime.Format("2006-01-02"))
		if err := s.processTask(ctx, t.ChatID, t.StartTime, t.EndTime, t.ID); err != nil {
			logger.Errorf("[Scheduler] 恢复处理任务失败 (chatID=%d): %v", t.ChatID, err)
			_ = s.taskModel.MarkTaskFailed(ctx, t.ID, err.Error())
			continue
		}
		_ = s.taskModel.MarkTaskCompleted(ctx, t.ID)
	}
}

// runDailySummary 执行每日总结任务（cron 触发）
func (s *Scheduler) runDailySummary() {
	s.mu.Lock()
	ctx := s.ctx
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		logger.Infof("[Scheduler] 任务已取消，退出")
		return
	default:
	}

	rangeDays := s.config.RangeDays
	if rangeDays <= 0 {
		rangeDays = 1
	}
	now := time.Now().In(locUTC)
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, locUTC)
	endTime := todayStart
	startTime := todayStart.AddDate(0, 0, -rangeDays)

	dateRange := fmt.Sprintf("%s ~ %s", startTime.Format("2006-01-02"), endTime.AddDate(0, 0, -1).Format("2006-01-02"))
	logger.Infof("[Scheduler] 开始执行每日总结任务，区间: %s", dateRange)

	// 在查询前创建 DailyRun 记录，便于崩溃恢复
	run, err := s.dailyRunModel.GetOrCreate(ctx, startTime, endTime, dailyrun.StatusInProgress)
	if err != nil {
		logger.Errorf("[Scheduler] 获取或创建 DailyRun 失败: %v", err)
		return
	}
	// 若已存在且完成，跳过
	if run.Status == dailyrun.StatusCompleted {
		logger.Infof("[Scheduler] 当日 DailyRun 已完成，跳过")
		return
	}

	if err := s.executeDailySummaryForRange(ctx, startTime, endTime); err != nil {
		logger.Errorf("[Scheduler] 每日总结执行失败: %v", err)
		_ = s.dailyRunModel.MarkFailed(ctx, run.ID, err.Error())
		return
	}
	_ = s.dailyRunModel.MarkCompleted(ctx, run.ID)
	logger.Infof("[Scheduler] 每日总结任务完成")
}

// executeDailySummaryForRange 对指定日期区间执行完整总结流程（查询、创建任务、处理、清理）
func (s *Scheduler) executeDailySummaryForRange(ctx context.Context, startTime, endTime time.Time) error {
	retryTimes := s.config.RetryTimes
	if retryTimes <= 0 {
		retryTimes = 3
	}
	retryInterval := time.Duration(s.config.RetryInterval) * time.Second
	if retryInterval <= 0 {
		retryInterval = 60 * time.Second
	}

	// 1. 查询 chatIDs（带重试）
	var chatIDs []int64
	var err error
	for attempt := 1; attempt <= retryTimes; attempt++ {
		select {
		case <-ctx.Done():
			return fmt.Errorf("任务已取消")
		default:
		}
		chatIDs, err = s.messageModel.GetChatIDsByDateRange(ctx, startTime, endTime)
		if err == nil {
			break
		}
		logger.Warnf("[Scheduler] 查询群组列表失败 (第 %d/%d 次): %v", attempt, retryTimes, err)
		if attempt < retryTimes {
			select {
			case <-ctx.Done():
				return fmt.Errorf("任务已取消")
			case <-time.After(retryInterval):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("查询群组列表失败，已重试 %d 次: %w", retryTimes, err)
	}

	// 白名单/黑名单过滤
	chatIDs = s.filterChatIDs(chatIDs)

	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}

	if len(chatIDs) == 0 {
		logger.Infof("[Scheduler] 区间内无消息，跳过总结")
		s.cleanupMessages(ctx)
		return nil
	}

	logger.Infof("[Scheduler] 找到 %d 个群组需要处理", len(chatIDs))

	// 2. 批量创建任务
	successCount := 0
	failCount := 0
	var tasksToProcess []*ent.Task
	for _, chatID := range chatIDs {
		select {
		case <-ctx.Done():
			return fmt.Errorf("任务已取消")
		default:
		}
		taskRecord, err := s.taskModel.GetOrCreateTask(ctx, chatID, startTime, endTime, task.StatusPending)
		if err != nil {
			logger.Errorf("[Scheduler] 创建任务失败 (chatID=%d): %v", chatID, err)
			failCount++
			continue
		}
		if taskRecord.Status == task.StatusCompleted {
			successCount++
			continue
		}
		tasksToProcess = append(tasksToProcess, taskRecord)
	}

	// 3. 处理任务
	for _, taskRecord := range tasksToProcess {
		select {
		case <-ctx.Done():
			return fmt.Errorf("任务已取消")
		default:
		}
		if err := s.taskModel.UpdateTaskStatus(ctx, taskRecord.ID, task.StatusProcessing, nil); err != nil {
			failCount++
			continue
		}
		if err := s.processTask(ctx, taskRecord.ChatID, taskRecord.StartTime, taskRecord.EndTime, taskRecord.ID); err != nil {
			_ = s.taskModel.MarkTaskFailed(ctx, taskRecord.ID, err.Error())
			failCount++
			continue
		}
		if err := s.taskModel.MarkTaskCompleted(ctx, taskRecord.ID); err == nil {
			successCount++
		}
	}

	logger.Infof("[Scheduler] 群组处理完成: 成功 %d 个，失败 %d 个", successCount, failCount)

	select {
	case <-ctx.Done():
		return fmt.Errorf("任务已取消")
	default:
	}
	s.cleanupMessages(ctx)
	return nil
}

// generateSummaryForTask 阶段一：生成总结。内含摘要重试循环；无消息或空内容时返回 summary=="" 且 err==nil 表示跳过通知。
func (s *Scheduler) generateSummaryForTask(ctx context.Context, chatID int64, startTime, endTime time.Time) (summary string, err error) {
	startDate := startTime.Format("2006-01-02")
	endDate := endTime.AddDate(0, 0, -1).Format("2006-01-02")

	retryTimes := s.config.RetryTimes
	if retryTimes <= 0 {
		retryTimes = 3
	}
	retryInterval := time.Duration(s.config.RetryInterval) * time.Second
	if retryInterval <= 0 {
		retryInterval = 60 * time.Second
	}

	var result *summarizer.SummaryResult
	for attempt := 1; attempt <= retryTimes; attempt++ {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("任务已取消")
		default:
		}

		logger.Debugf("[Scheduler] 群组 %d: 尝试生成摘要 (第 %d/%d 次)", chatID, attempt, retryTimes)
		result, err = s.summarizer.SummarizeRange(ctx, chatID, startTime, endTime)
		if err == nil {
			logger.Infof("[Scheduler] 群组 %d: 摘要生成成功", chatID)
			break
		}

		logger.Warnf("[Scheduler] 群组 %d: 摘要生成失败 (第 %d/%d 次): %v", chatID, attempt, retryTimes, err)
		if attempt < retryTimes {
			logger.Debugf("[Scheduler] 群组 %d: %v 后进行重试...", chatID, retryInterval)
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("任务已取消")
			case <-time.After(retryInterval):
			}
		}
	}

	if err != nil {
		return "", fmt.Errorf("摘要生成失败，已重试 %d 次: %w", retryTimes, err)
	}

	if result == nil {
		logger.Infof("[Scheduler] 群组 %d: 区间内无消息，跳过通知", chatID)
		return "", nil
	}

	chatTitle := s.getChatTitle(chatID)
	summary = summarizer.FormatSummaryForDisplay(result, chatID, chatTitle, startDate, endDate)
	if summary == "" {
		logger.Infof("[Scheduler] 群组 %d: 总结内容为空，跳过通知", chatID)
		return "", nil
	}

	return summary, nil
}

// sendTaskNotification 阶段二：发送通知。仅重试 Notify，不会重新生成总结；通知失败不影响任务完成状态。
// 返回 (sent, err)：sent 表示是否发送成功，err 表示是否应中止（如 ctx 取消）。
func (s *Scheduler) sendTaskNotification(ctx context.Context, summary string, chatID int64) (sent bool, err error) {
	retryInterval := time.Duration(s.config.RetryInterval) * time.Second
	if retryInterval <= 0 {
		retryInterval = 60 * time.Second
	}

	notifyRetryTimes := 2
	for attempt := 1; attempt <= notifyRetryTimes; attempt++ {
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("任务已取消")
		default:
		}

		notifyErr := s.notifier.Notify(ctx, summary, chatID)
		if notifyErr == nil {
			logger.Infof("[Scheduler] 群组 %d: 通知发送成功", chatID)
			return true, nil
		}
		logger.Warnf("[Scheduler] 群组 %d: 通知发送失败 (第 %d/%d 次): %v", chatID, attempt, notifyRetryTimes, notifyErr)
		if attempt < notifyRetryTimes {
			select {
			case <-ctx.Done():
				return false, fmt.Errorf("任务已取消")
			case <-time.After(retryInterval / 2):
			}
		}
	}

	logger.Errorf("[Scheduler] 群组 %d: 通知发送失败，已重试 %d 次", chatID, notifyRetryTimes)
	// 通知失败不影响任务完成状态，因为摘要已生成；返回 sent=false 以便不清除 summary_content，恢复时只重试发送
	return false, nil
}

// processTask 处理单个任务：先生成总结，再发送通知；通知重试仅重试发送，不重试总结。
// taskID > 0 时在发送前将摘要持久化到任务，程序在发送期间退出后恢复时只会重试发送；发送成功后清除。
func (s *Scheduler) processTask(ctx context.Context, chatID int64, startTime, endTime time.Time, taskID int) error {
	dateRange := fmt.Sprintf("%s ~ %s", startTime.Format("2006-01-02"), endTime.AddDate(0, 0, -1).Format("2006-01-02"))
	logger.Infof("[Scheduler] 处理群组 %d，区间: %s", chatID, dateRange)

	// 阶段一：生成总结
	summary, err := s.generateSummaryForTask(ctx, chatID, startTime, endTime)
	if err != nil {
		return err
	}
	if summary == "" {
		return nil
	}

	// 发送前持久化摘要：之后无论首次发送还是重试时崩溃，重启后都只重试发送，不会重新生成摘要
	if taskID > 0 {
		if err := s.taskModel.SetSummaryContent(ctx, taskID, summary); err != nil {
			logger.Warnf("[Scheduler] 保存摘要内容失败 (taskID=%d): %v，继续发送", taskID, err)
		}
	}

	// 阶段二：发送通知（仅重试发送，不重新生成总结）
	sent, err := s.sendTaskNotification(ctx, summary, chatID)
	if err != nil {
		return err
	}
	if sent && taskID > 0 {
		_ = s.taskModel.ClearSummaryContent(ctx, taskID)
	}
	return nil
}

// cleanupMessages 执行消息清理
func (s *Scheduler) cleanupMessages(ctx context.Context) {
	cutoffDate := time.Now().In(locUTC).AddDate(0, 0, -s.config.RetentionDays-1)
	cutoffDate = time.Date(cutoffDate.Year(), cutoffDate.Month(), cutoffDate.Day(), 0, 0, 0, 0, locUTC)

	logger.Infof("[Scheduler] 开始清理 %s 之前的消息", cutoffDate.Format("2006-01-02"))
	deleted, err := s.messageModel.DeleteBefore(ctx, cutoffDate)
	if err != nil {
		logger.Errorf("[Scheduler] 清理消息失败: %v", err)
	} else {
		logger.Infof("[Scheduler] 已清理 %d 条消息", deleted)
	}
}

// getChatTitle 获取群组名称
func (s *Scheduler) getChatTitle(chatID int64) string {
	if s.tdClient == nil {
		return ""
	}
	chat, err := s.tdClient.GetChat(&client.GetChatRequest{ChatId: chatID})
	if err != nil {
		logger.Warnf("[Scheduler] 获取群组信息失败: %v", err)
		return ""
	}
	return chat.Title
}

// filterChatIDs 根据白名单/黑名单过滤群组ID
func (s *Scheduler) filterChatIDs(chatIDs []int64) []int64 {
	cfg := s.config
	whitelist := cfg.Whitelist
	blacklist := cfg.Blacklist

	// 白名单优先
	if len(whitelist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			for _, wid := range whitelist {
				if id == wid {
					filtered = append(filtered, id)
					break
				}
			}
		}
		logger.Infof("[Scheduler] 白名单过滤: %d -> %d 个群组", len(chatIDs), len(filtered))
		return filtered
	}

	// 黑名单过滤
	if len(blacklist) > 0 {
		filtered := make([]int64, 0)
		for _, id := range chatIDs {
			blocked := false
			for _, bid := range blacklist {
				if id == bid {
					blocked = true
					break
				}
			}
			if !blocked {
				filtered = append(filtered, id)
			}
		}
		logger.Infof("[Scheduler] 黑名单过滤: %d -> %d 个群组", len(chatIDs), len(filtered))
		return filtered
	}

	return chatIDs
}
