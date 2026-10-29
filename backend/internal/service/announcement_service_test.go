package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type announcementRepoStub struct {
	item *Announcement
}

type announcementRecipientRepoStub struct {
	UserRepository
	users       []User
	lastFilters UserListFilters
}

func (s *announcementRecipientRepoStub) ListWithFilters(_ context.Context, _ pagination.PaginationParams, filters UserListFilters) ([]User, *pagination.PaginationResult, error) {
	s.lastFilters = filters
	users := make([]User, 0, len(s.users))
	for _, user := range s.users {
		if filters.Status != "" && user.Status != filters.Status {
			continue
		}
		if filters.Role != "" && user.Role != filters.Role {
			continue
		}
		users = append(users, user)
	}
	return users, &pagination.PaginationResult{Total: int64(len(users))}, nil
}

func (s *announcementRepoStub) Create(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (s *announcementRepoStub) GetByID(_ context.Context, _ int64) (*Announcement, error) {
	if s.item == nil {
		return nil, ErrAnnouncementNotFound
	}
	return s.item, nil
}

func (s *announcementRepoStub) Update(_ context.Context, a *Announcement) error {
	s.item = a
	return nil
}

func (*announcementRepoStub) Delete(context.Context, int64) error {
	return nil
}

func (*announcementRepoStub) List(context.Context, pagination.PaginationParams, AnnouncementListFilters) ([]Announcement, *pagination.PaginationResult, error) {
	return nil, nil, nil
}

func (*announcementRepoStub) ListActive(context.Context, time.Time) ([]Announcement, error) {
	return nil, nil
}

func TestAnnouncementServiceCreateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:      "公告",
		Content:    "内容",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModePopup,
		StartsAt:   &now,
		EndsAt:     &now,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

func TestAnnouncementServiceCreateNormalizesLocalizedFields(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:     "MiniMax-H3 上线",
		Content:   "中文兼容回退内容",
		TitleZh:   "  MiniMax-H3 已上线  ",
		TitleEn:   "  MiniMax-H3 is now live  ",
		ContentZh: "  中文公告正文  ",
		ContentEn: "  English announcement body  ",
	})

	require.NoError(t, err)
	require.NotNil(t, repo.item)
	require.Equal(t, "MiniMax-H3 已上线", repo.item.TitleZh)
	require.Equal(t, "MiniMax-H3 is now live", repo.item.TitleEn)
	require.Equal(t, "中文公告正文", repo.item.ContentZh)
	require.Equal(t, "English announcement body", repo.item.ContentEn)
}

func TestAnnouncementServiceRejectsOversizedLocalizedTitle(t *testing.T) {
	repo := &announcementRepoStub{}
	svc := NewAnnouncementService(repo, nil, nil, nil)

	_, err := svc.Create(context.Background(), &CreateAnnouncementInput{
		Title:   "公告",
		Content: "内容",
		TitleEn: string(make([]byte, 201)),
	})

	require.ErrorIs(t, err, ErrAnnouncementInvalidTitle)
}

func TestAnnouncementServiceUpdatePreservesOmittedLocalizedFields(t *testing.T) {
	repo := &announcementRepoStub{item: &Announcement{
		ID: 1, Title: "Legacy title", Content: "Legacy content",
		TitleZh: "中文标题", TitleEn: "English title", ContentZh: "中文正文", ContentEn: "English body",
	}}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	updatedTitle := "  Updated English title  "
	clearedContent := "  "

	updated, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{
		TitleEn: &updatedTitle, ContentEn: &clearedContent,
	})

	require.NoError(t, err)
	require.Equal(t, "Updated English title", updated.TitleEn)
	require.Empty(t, updated.ContentEn)
	require.Equal(t, "中文标题", updated.TitleZh)
	require.Equal(t, "中文正文", updated.ContentZh)
	require.Equal(t, "Legacy title", updated.Title)
	require.Equal(t, "Legacy content", updated.Content)
}

func TestAnnouncementServiceUpdateRejectsOversizedLocalizedTitle(t *testing.T) {
	repo := &announcementRepoStub{item: &Announcement{ID: 1, Title: "Legacy", Content: "Body"}}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	oversized := string(make([]byte, 201))

	_, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{TitleZh: &oversized})

	require.ErrorIs(t, err, ErrAnnouncementInvalidTitle)
}

func TestAnnouncementServiceUpdateRejectsEqualStartEndTimes(t *testing.T) {
	repo := &announcementRepoStub{
		item: &Announcement{
			ID:         1,
			Title:      "公告",
			Content:    "内容",
			Status:     AnnouncementStatusActive,
			NotifyMode: AnnouncementNotifyModePopup,
		},
	}
	svc := NewAnnouncementService(repo, nil, nil, nil)
	now := time.Unix(1776790020, 0)
	startsAt := &now
	endsAt := &now

	_, err := svc.Update(context.Background(), 1, &UpdateAnnouncementInput{
		StartsAt: &startsAt,
		EndsAt:   &endsAt,
	})
	require.ErrorIs(t, err, ErrAnnouncementInvalidSchedule)
}

func TestAnnouncementServiceQueuesBilingualNotificationForActiveUsers(t *testing.T) {
	repo := &announcementRepoStub{item: &Announcement{
		ID:         42,
		Title:      "Fallback title",
		Content:    "Fallback content",
		TitleZh:    "中文公告",
		TitleEn:    "English announcement",
		ContentZh:  "中文正文",
		ContentEn:  "English body",
		Status:     AnnouncementStatusActive,
		NotifyMode: AnnouncementNotifyModePopup,
	}}
	users := &announcementRecipientRepoStub{users: []User{
		{ID: 1, Email: "active@example.com", Username: "Active", Role: RoleUser, Status: StatusActive},
		{ID: 2, Email: "disabled@example.com", Username: "Disabled", Role: RoleUser, Status: StatusDisabled},
		{ID: 3, Email: "admin@example.com", Username: "Admin", Role: RoleAdmin, Status: StatusActive},
	}}
	queue := &EmailQueueService{taskChan: make(chan EmailTask, 1)}
	svc := NewAnnouncementService(repo, nil, users, nil)
	svc.SetNotificationEmailService(NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil))
	svc.SetEmailQueueService(queue)

	queued, err := svc.QueuePublishedNotification(context.Background(), repo.item.ID)
	require.NoError(t, err)
	require.Equal(t, 1, queued)
	require.Equal(t, StatusActive, users.lastFilters.Status)
	require.Equal(t, RoleUser, users.lastFilters.Role)

	task := <-queue.taskChan
	require.Equal(t, TaskTypeNotificationBatch, task.TaskType)
	require.Len(t, task.Notifications, 1)
	require.Equal(t, "active@example.com", task.Notifications[0].RecipientEmail)
	require.Equal(t, "中文公告", task.Notifications[0].Variables["announcement_title_zh"])
	require.Equal(t, "English body", task.Notifications[0].Variables["announcement_content_en"])
}

func TestAnnouncementServiceQueuePublishedNotificationRejectsBackpressure(t *testing.T) {
	repo := &announcementRepoStub{item: &Announcement{ID: 7, Title: "公告", Content: "正文", Status: AnnouncementStatusActive}}
	users := &announcementRecipientRepoStub{users: []User{{ID: 1, Email: "active@example.com", Role: RoleUser, Status: StatusActive}}}
	queue := &EmailQueueService{taskChan: make(chan EmailTask)}
	svc := NewAnnouncementService(repo, nil, users, nil)
	svc.SetNotificationEmailService(NewNotificationEmailService(newNotificationEmailMemorySettingRepo(), nil))
	svc.SetEmailQueueService(queue)

	_, err := svc.QueuePublishedNotification(context.Background(), repo.item.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "email queue is full")
}
