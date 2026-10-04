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
