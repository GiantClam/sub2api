package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Task type constants
const (
	TaskTypeVerifyCode        = "verify_code"
	TaskTypePasswordReset     = "password_reset"
	TaskTypeNotificationBatch = "notification_batch"
)

// EmailTask 邮件发送任务
type EmailTask struct {
	Email         string
	SiteName      string
	TaskType      string // "verify_code", "password_reset", or "notification_batch"
	ResetURL      string // Only used for password_reset task type
	Locale        string // Optional Accept-Language locale hint
	Notifications []NotificationEmailSendInput
}

// EmailQueueService 异步邮件队列服务
type EmailQueueService struct {
	emailService *EmailService
	taskChan     chan EmailTask
	wg           sync.WaitGroup
	stopChan     chan struct{}
	workers      int
}

// NewEmailQueueService 创建邮件队列服务
func NewEmailQueueService(emailService *EmailService, workers int) *EmailQueueService {
	if workers <= 0 {
		workers = 3 // 默认3个工作协程
	}

	service := &EmailQueueService{
		emailService: emailService,
		taskChan:     make(chan EmailTask, 100), // 缓冲100个任务
		stopChan:     make(chan struct{}),
		workers:      workers,
	}

	// 启动工作协程
	service.start()

	return service
}

// start 启动工作协程
func (s *EmailQueueService) start() {
	for i := 0; i < s.workers; i++ {
		s.wg.Add(1)
		go s.worker(i)
	}
	logger.LegacyPrintf("service.email_queue", "[EmailQueue] Started %d workers", s.workers)
}

// worker 工作协程
func (s *EmailQueueService) worker(id int) {
	defer s.wg.Done()

	for {
		select {
		case task := <-s.taskChan:
			s.processTask(id, task)
		case <-s.stopChan:
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d stopping", id)
			return
		}
	}
}

// processTask 处理任务
func (s *EmailQueueService) processTask(workerID int, task EmailTask) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch task.TaskType {
	case TaskTypeVerifyCode:
		if err := s.emailService.SendVerifyCode(ctx, task.Email, task.SiteName, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send verify code to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent verify code to %s", workerID, task.Email)
		}
	case TaskTypePasswordReset:
		if err := s.emailService.SendPasswordResetEmailWithCooldown(ctx, task.Email, task.SiteName, task.ResetURL, task.Locale); err != nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send password reset to %s: %v", workerID, task.Email, err)
		} else {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent password reset to %s", workerID, task.Email)
		}
	case TaskTypeNotificationBatch:
		if s.emailService == nil || s.emailService.notificationEmailService == nil {
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d cannot send notification batch: notification email service is not configured", workerID)
			return
		}
		for _, input := range task.Notifications {
			itemCtx, itemCancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := s.emailService.notificationEmailService.Send(itemCtx, input)
			itemCancel()
			if err != nil {
				logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d failed to send notification event %s to recipient hash %s: %v", workerID, input.Event, notificationEmailHash(input.RecipientEmail), err)
				continue
			}
			logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d sent notification event %s to recipient hash %s", workerID, input.Event, notificationEmailHash(input.RecipientEmail))
		}
	default:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Worker %d unknown task type: %s", workerID, task.TaskType)
	}
}

// EnqueueNotifications adds one asynchronous batch to the existing email
// queue. Each notification keeps its own delivery key, so retries remain
// idempotent at the notification service layer.
func (s *EmailQueueService) EnqueueNotifications(inputs []NotificationEmailSendInput) error {
	if len(inputs) == 0 {
		return nil
	}
	copied := make([]NotificationEmailSendInput, 0, len(inputs))
	for _, input := range inputs {
		clone := input
		if input.Variables != nil {
			clone.Variables = make(map[string]string, len(input.Variables))
			for key, value := range input.Variables {
				clone.Variables[key] = value
			}
		}
		if input.RawHTMLVariables != nil {
			clone.RawHTMLVariables = make(map[string]string, len(input.RawHTMLVariables))
			for key, value := range input.RawHTMLVariables {
				clone.RawHTMLVariables[key] = value
			}
		}
		copied = append(copied, clone)
	}

	task := EmailTask{TaskType: TaskTypeNotificationBatch, Notifications: copied}
	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued notification batch with %d messages", len(copied))
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// EnqueueVerifyCode 将验证码发送任务加入队列
func (s *EmailQueueService) EnqueueVerifyCode(email, siteName string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypeVerifyCode,
		Locale:   firstEmailLocale(locale),
	}

	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued verify code task for %s", email)
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// EnqueuePasswordReset 将密码重置邮件任务加入队列
func (s *EmailQueueService) EnqueuePasswordReset(email, siteName, resetURL string, locale ...string) error {
	task := EmailTask{
		Email:    email,
		SiteName: siteName,
		TaskType: TaskTypePasswordReset,
		ResetURL: resetURL,
		Locale:   firstEmailLocale(locale),
	}

	select {
	case s.taskChan <- task:
		logger.LegacyPrintf("service.email_queue", "[EmailQueue] Enqueued password reset task for %s", email)
		return nil
	default:
		return fmt.Errorf("email queue is full")
	}
}

// Stop 停止队列服务
func (s *EmailQueueService) Stop() {
	close(s.stopChan)
	s.wg.Wait()
	logger.LegacyPrintf("service.email_queue", "%s", "[EmailQueue] All workers stopped")
}
